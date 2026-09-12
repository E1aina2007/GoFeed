package mq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 拓扑常量：交换机、队列与路由键在进程间共享，改名视为破坏性变更
const (
	EventsExchange         = "gofeed.events"
	VideoProcessRoutingKey = "video.process"
	VideoProcessQueue      = "video.process"
	DeadLetterExchange     = "gofeed.dlx"

	// SchemaVersion 是处理消息体的结构版本，消费方据此拒绝不认识的载荷
	SchemaVersion = 1
)

// VideoProcessEventSpec 返回视频处理事件的发布目标
func VideoProcessEventSpec() EventSpec {
	return EventSpec{
		EventType:  "video.process",
		Exchange:   EventsExchange,
		RoutingKey: VideoProcessRoutingKey,
	}
}

// VideoProcessSpec 返回视频处理队列的消费契约，供 runtime 与 worker 共用
func VideoProcessSpec() ConsumerSpec {
	event := VideoProcessEventSpec()
	retryDelays := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	return ConsumerSpec{
		Event:    event,
		Queue:    VideoProcessQueue,
		Prefetch: 16,
		Retry:    RetryPolicy{MaxRetries: len(retryDelays), Delays: retryDelays},
	}
}

// declareChannel 抽象拓扑声明所需的信道能力，便于单测注入
type declareChannel interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
}

// DeclareTopology 声明事件交换机、处理队列、分级重试队列与死信拓扑，重复声明为幂等操作
// 重试队列各自以消息 TTL 到期后经死信路由回主队列实现退避，无需插件
func DeclareTopology(ch declareChannel) error {
	if ch == nil {
		return errors.New("channel is not initialized")
	}
	spec := VideoProcessSpec()
	if err := spec.Validate(); err != nil {
		return err
	}
	if err := ch.ExchangeDeclare(spec.Event.Exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明交换机失败: %w", err)
	}
	// 死信路由键显式固定，死信在死信队列中不依赖原始路由键
	if _, err := ch.QueueDeclare(spec.Queue, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    DeadLetterExchange,
		"x-dead-letter-routing-key": spec.DeadLetterQueueName(),
	}); err != nil {
		return fmt.Errorf("声明处理队列失败: %w", err)
	}
	if err := ch.QueueBind(spec.Queue, spec.Event.RoutingKey, spec.Event.Exchange, false, nil); err != nil {
		return fmt.Errorf("绑定处理队列失败: %w", err)
	}
	// 重试队列不被主交换机绑定：消费端直接投递到队列，到期后经死信路由回主队列
	for index := range spec.Retry.Delays {
		delay := spec.Retry.Delay(index)
		if _, err := ch.QueueDeclare(spec.RetryQueueName(index), true, false, false, false, amqp.Table{
			"x-message-ttl":             int64(delay / time.Millisecond),
			"x-dead-letter-exchange":    spec.Event.Exchange,
			"x-dead-letter-routing-key": spec.Event.RoutingKey,
		}); err != nil {
			return fmt.Errorf("声明重试队列 %s 失败: %w", spec.RetryQueueName(index), err)
		}
	}
	if err := ch.ExchangeDeclare(DeadLetterExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明死信交换机失败: %w", err)
	}
	if _, err := ch.QueueDeclare(spec.DeadLetterQueueName(), true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明死信队列失败: %w", err)
	}
	return ch.QueueBind(spec.DeadLetterQueueName(), spec.DeadLetterQueueName(), DeadLetterExchange, false, nil)
}

// confirmingPublisher 抽象「发布并等待 broker 确认」一次调用的信道能力
// amqp.Channel 的确认句柄为具体类型，抽象后单测可注入假实现
type confirmingPublisher interface {
	publishAndWait(ctx context.Context, exchange, routingKey string, headers amqp.Table, msg amqp.Publishing) error
}

// amqpChannelPublisher 将 *amqp.Channel 适配为带确认的发布接缝
type amqpChannelPublisher struct {
	ch *amqp.Channel
}

func (a *amqpChannelPublisher) publishAndWait(ctx context.Context, exchange, routingKey string, headers amqp.Table, msg amqp.Publishing) error {
	msg.Headers = headers
	confirmation, err := a.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey, false, false, msg)
	if err != nil {
		return fmt.Errorf("发布消息失败: %w", err)
	}
	if acked, err := confirmation.WaitContext(ctx); err != nil {
		return fmt.Errorf("等待发布确认失败: %w", err)
	} else if !acked {
		return errors.New("broker 未确认消息")
	}
	return nil
}

// publisher 在 confirm 模式信道上发布 JSON 消息，confirm 失败视为发布失败
type publisher struct {
	ch confirmingPublisher
}

// newPublisherWithSeam 供单测注入发布接缝
func newPublisherWithSeam(ch confirmingPublisher) *publisher {
	return &publisher{ch: ch}
}

// Publish 序列化载荷并等待 broker 确认；上下文取消或 nack 返回错误
func (p *publisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return p.PublishWithHeaders(ctx, exchange, routingKey, payload, nil)
}

// PublishWithHeaders 在 Publish 基础上附加自定义消息头，供重试计数等场景使用
func (p *publisher) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
	if p == nil || p.ch == nil {
		return errors.New("publisher is not initialized")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}
	return p.ch.publishAndWait(ctx, exchange, routingKey, headers, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Body:         body,
	})
}
