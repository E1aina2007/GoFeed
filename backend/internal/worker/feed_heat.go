package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"time"

	domainfeed "gofeed/internal/domain/feed"
	domaininteraction "gofeed/internal/domain/interaction"
	"gofeed/internal/mq"

	amqp "github.com/rabbitmq/amqp091-go"
)

const heatProcessingTimeout = 5 * time.Second

type HeatHandler interface {
	ApplyChanged(context.Context, domaininteraction.ChangedEvent) (domainfeed.HeatResult, error)
}

type HeatConsumer struct {
	handler   HeatHandler
	publisher EventPublisher // 重试或死信发布确认后才 ACK 原消息
	spec      mq.ConsumerSpec
}

func NewHeatConsumer(handler HeatHandler, publisher EventPublisher) (*HeatConsumer, error) {
	if handler == nil || publisher == nil {
		return nil, errors.New("heat consumer requires handler and publisher")
	}
	return &HeatConsumer{handler: handler, publisher: publisher, spec: mq.InteractionHeatSpec()}, nil
}

// Run 独立消费热度队列，处理成功或重试发布确认后才确认原投递
func (c *HeatConsumer) Run(ctx context.Context, source ConsumerSource) {
	for ctx.Err() == nil {
		channel, err := source.ConsumerChannel(c.spec.Prefetch)
		if err != nil {
			log.Printf("event=feed_heat result=channel_failed")
			sleepContext(ctx, consumerReconnectDelay)
			continue
		}
		deliveries, err := channel.Consume(c.spec.Queue)
		if err != nil {
			_ = channel.Close()
			log.Printf("event=feed_heat result=consume_failed")
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
				log.Printf("event=feed_heat result=settlement_failed")
				break consume
			}
		}
		_ = channel.Close()
		sleepContext(ctx, consumerReconnectDelay)
	}
}

func decodeInteractionMessage(body []byte) (domaininteraction.ChangedEvent, error) {
	if len(body) > 4096 {
		return domaininteraction.ChangedEvent{}, domainfeed.ErrInvalidHeatEvent
	}
	var message InteractionChangedMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return domaininteraction.ChangedEvent{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return domaininteraction.ChangedEvent{}, domainfeed.ErrInvalidHeatEvent
	}
	event := domaininteraction.ChangedEvent{
		EventID:              message.EventID,
		SchemaVersion:        message.SchemaVersion,
		EventType:            message.EventType,
		VideoID:              message.VideoID,
		Kind:                 message.Kind,
		InteractionID:        message.InteractionID,
		Delta:                message.Delta,
		OccurredAt:           message.OccurredAt,
		InteractionCreatedAt: message.InteractionCreatedAt,
	}
	return event, event.Validate()
}

func (c *HeatConsumer) handleDelivery(ctx context.Context, delivery amqp.Delivery) mq.HandlerResult {
	started := time.Now()
	event, err := decodeInteractionMessage(delivery.Body)
	if err != nil {
		log.Printf("event=feed_heat result=dead_letter reason=invalid_payload")
		return mq.ResultDeadLetter
	}
	attempt, err := warmDeliveryAttempt(delivery, c.spec.Retry.MaxRetries)
	if err != nil {
		log.Printf("event=feed_heat event_id=%s result=dead_letter reason=invalid_retry_header", event.EventID)
		return mq.ResultDeadLetter
	}
	opCtx, cancel := context.WithTimeout(ctx, heatProcessingTimeout)
	defer cancel()
	result, err := c.handler.ApplyChanged(opCtx, event)
	if ctx.Err() != nil {
		return mq.ResultRetry
	}
	if err != nil {
		outcome := mq.ResultRetry
		label, reason := "retry", "dependency_failed"
		switch {
		case errors.Is(err, domainfeed.ErrInvalidHeatEvent), errors.Is(err, domainfeed.ErrHeatEventConflict):
			outcome, label, reason = mq.ResultDeadLetter, "dead_letter", "invalid_event"
		case c.spec.Retry.Exhausted(attempt):
			outcome, label, reason = mq.ResultDeadLetter, "dead_letter", "retry_exhausted"
		case errors.Is(err, domainfeed.ErrHeatCapacity):
			reason = "minute_capacity"
		case errors.Is(err, domainfeed.ErrHeatPolicyConflict):
			reason = "generation_policy_conflict"
		case errors.Is(err, domainfeed.ErrHeatFutureEvent):
			reason = "future_event"
		}
		log.Printf("event=feed_heat event_id=%s video_id=%d result=%s reason=%s attempt=%d duration_ms=%d coverage=unverified",
			event.EventID, event.VideoID, label, reason, attempt, time.Since(started).Milliseconds())
		return outcome
	}
	if result != domainfeed.HeatApplied && result != domainfeed.HeatDuplicate && result != domainfeed.HeatSkippedExpired {
		if c.spec.Retry.Exhausted(attempt) {
			return mq.ResultDeadLetter
		}
		return mq.ResultRetry
	}
	log.Printf("event=feed_heat event_id=%s video_id=%d result=%s attempt=%d duration_ms=%d lag_ms=%d coverage=unverified",
		event.EventID, event.VideoID, result, attempt, time.Since(started).Milliseconds(), max(int64(0), time.Since(event.OccurredAt).Milliseconds()))
	return mq.ResultAck
}

func (c *HeatConsumer) retryDelivery(ctx context.Context, delivery amqp.Delivery) error {
	attempt, err := warmDeliveryAttempt(delivery, c.spec.Retry.MaxRetries)
	if err != nil {
		return err
	}
	if c.spec.Retry.Exhausted(attempt) {
		return errors.New("heat retry policy exhausted")
	}
	opCtx, cancel := context.WithTimeout(ctx, heatProcessingTimeout)
	defer cancel()
	queue := c.spec.RetryQueueName(attempt)
	if err := c.publisher.PublishWithHeaders(opCtx, "", queue, json.RawMessage(delivery.Body), amqp.Table{retryHeader: attempt + 1}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	log.Printf("event=feed_heat result=retry_confirmed attempt=%d queue=%s", attempt+1, queue)
	return delivery.Ack(false)
}
