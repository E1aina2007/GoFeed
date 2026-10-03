package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/mq"
	"gofeed/internal/video"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

const cardWarmupTimeout = 5 * time.Second

var errPublishedSchemaVersion = errors.New("unsupported published schema version")

type PublishedMessage struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	VideoID       uint   `json:"video_id"`
}

func (m PublishedMessage) validate() error {
	if m.SchemaVersion != mq.VideoPublishedSchemaVersion {
		return errPublishedSchemaVersion
	}
	if m.VideoID == 0 {
		return errors.New("published video id is zero")
	}
	if id, err := uuid.Parse(m.EventID); err != nil || id == uuid.Nil {
		return errors.New("invalid published event id")
	}
	return nil
}

// VideoPublishedRoute 按持久化事件标识派发，不套用处理状态的终态收口
func VideoPublishedRoute() RelayRoute {
	return RelayRoute{Event: mq.VideoPublishedEventSpec(), Prepare: func(dispatch video.OutboxDispatch) (RelayPreparation, error) {
		msg := PublishedMessage{SchemaVersion: mq.VideoPublishedSchemaVersion, EventID: dispatch.Event.EventID, VideoID: dispatch.Event.VideoID}
		if err := msg.validate(); err != nil {
			return RelayPreparation{}, err
		}
		return RelayPreparation{Payload: msg}, nil
	}}
}

type CardWarmupHandler interface {
	WarmCard(context.Context, uint) (applicationfeed.CardWarmupResult, error)
}

type CardWarmConsumer struct {
	handler   CardWarmupHandler
	publisher EventPublisher
	spec      mq.ConsumerSpec
}

func NewCardWarmConsumer(handler CardWarmupHandler, publisher EventPublisher) (*CardWarmConsumer, error) {
	if handler == nil || publisher == nil {
		return nil, errors.New("card warm consumer requires handler and publisher")
	}
	return &CardWarmConsumer{handler: handler, publisher: publisher, spec: mq.FeedCardWarmSpec()}, nil
}

// Run 使用独立消费信道，失败重发确认后才确认原投递
func (c *CardWarmConsumer) Run(ctx context.Context, source ConsumerSource) {
	for ctx.Err() == nil {
		channel, err := source.ConsumerChannel(c.spec.Prefetch)
		if err != nil {
			log.Printf("event=feed_card_warm result=channel_failed")
			sleepContext(ctx, consumerReconnectDelay)
			continue
		}
		deliveries, err := channel.Consume(c.spec.Queue)
		if err != nil {
			_ = channel.Close()
			log.Printf("event=feed_card_warm result=consume_failed")
			sleepContext(ctx, consumerReconnectDelay)
			continue
		}
	consume:
		for {
			var delivery amqp.Delivery
			var ok bool
			select {
			case <-ctx.Done():
				_ = channel.Close()
				return
			case delivery, ok = <-deliveries:
				if !ok {
					break consume
				}
			}
			if ctx.Err() != nil {
				_ = channel.Close()
				return
			}
			result := c.handleDelivery(ctx, delivery)
			if ctx.Err() != nil {
				_ = channel.Close()
				return
			}
			switch result {
			case mq.ResultAck:
				err = delivery.Ack(false)
			case mq.ResultDeadLetter:
				err = delivery.Nack(false, false)
			case mq.ResultRetry:
				err = c.retryDelivery(ctx, delivery)
			}
			if err != nil {
				log.Printf("event=feed_card_warm result=settlement_failed")
				break consume
			}
		}
		_ = channel.Close()
		sleepContext(ctx, consumerReconnectDelay)
	}
}

func decodePublishedMessage(body []byte) (PublishedMessage, error) {
	if len(body) > 1024 {
		return PublishedMessage{}, errors.New("published payload exceeds limit")
	}
	var msg PublishedMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&msg); err != nil {
		return msg, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return msg, errors.New("trailing published payload")
	}
	return msg, msg.validate()
}

func warmDeliveryAttempt(delivery amqp.Delivery, maxRetries int) (int, error) {
	value, exists := delivery.Headers[retryHeader]
	if !exists {
		return 0, nil
	}
	var number int64
	switch v := value.(type) {
	case int:
		number = int64(v)
	case int32:
		number = int64(v)
	case int64:
		number = v
	default:
		return 0, errors.New("invalid retry header type")
	}
	if number < 0 || number > int64(maxRetries) {
		return 0, errors.New("retry header outside bounded policy")
	}
	return int(number), nil
}

func (c *CardWarmConsumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) mq.HandlerResult {
	started := time.Now()
	msg, err := decodePublishedMessage(delivery.Body)
	if err != nil {
		reason := "invalid_payload"
		if errors.Is(err, errPublishedSchemaVersion) {
			reason = "unsupported_schema_version"
		}
		log.Printf("event=feed_card_warm event_type=%s result=dead_letter reason=%s schema_version=%d", c.spec.Event.EventType, reason, msg.SchemaVersion)
		return mq.ResultDeadLetter
	}
	attempt, err := warmDeliveryAttempt(delivery, c.spec.Retry.MaxRetries)
	if err != nil {
		log.Printf("event=feed_card_warm event_type=%s event_id=%s video_id=%d result=dead_letter reason=invalid_retry_header", c.spec.Event.EventType, msg.EventID, msg.VideoID)
		return mq.ResultDeadLetter
	}
	opCtx, cancel := context.WithTimeout(ctx, cardWarmupTimeout)
	defer cancel()
	result, err := c.handler.WarmCard(opCtx, msg.VideoID)
	if err != nil {
		if ctx.Err() != nil {
			return mq.ResultRetry
		}
		outcome := mq.ResultRetry
		label := "retry"
		reason := "dependency_failed"
		if c.spec.Retry.Exhausted(attempt) {
			outcome = mq.ResultDeadLetter
			label = "dead_letter"
			reason = "retry_exhausted"
		}
		log.Printf("event=feed_card_warm event_type=%s event_id=%s video_id=%d result=%s reason=%s attempt=%d duration_ms=%d", c.spec.Event.EventType, msg.EventID, msg.VideoID, label, reason, attempt, time.Since(started).Milliseconds())
		return outcome
	}
	if ctx.Err() != nil {
		return mq.ResultRetry
	}
	log.Printf("event=feed_card_warm event_type=%s event_id=%s video_id=%d result=%s attempt=%d duration_ms=%d", c.spec.Event.EventType, msg.EventID, msg.VideoID, result, attempt, time.Since(started).Milliseconds())
	return mq.ResultAck
}

func (c *CardWarmConsumer) retryDelivery(ctx context.Context, delivery amqp.Delivery) error {
	msg, err := decodePublishedMessage(delivery.Body)
	if err != nil {
		return err
	}
	attempt, err := warmDeliveryAttempt(delivery, c.spec.Retry.MaxRetries)
	if err != nil {
		return err
	}
	if c.spec.Retry.Exhausted(attempt) {
		return errors.New("card warm retry policy exhausted")
	}
	opCtx, cancel := context.WithTimeout(ctx, cardWarmupTimeout)
	defer cancel()
	queue := c.spec.RetryQueueName(attempt)
	if err := c.publisher.PublishWithHeaders(opCtx, "", queue, msg, amqp.Table{retryHeader: attempt + 1}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	log.Printf("event=feed_card_warm event_type=%s event_id=%s video_id=%d result=retry_confirmed attempt=%d queue=%s", c.spec.Event.EventType, msg.EventID, msg.VideoID, attempt+1, queue)
	return delivery.Ack(false)
}
