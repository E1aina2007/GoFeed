package mq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gofeed/internal/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 拓扑常量：交换机、队列与路由键在进程间共享，改名视为破坏性变更
const (
	EventsExchange         = "gofeed.events"
	VideoProcessRoutingKey = "video.process"
	VideoProcessQueue      = "video.process"
	DeadLetterExchange     = "gofeed.dlx"
	VideoProcessDeadQueue  = "video.process.dead"

	// SchemaVersion 是处理消息体的结构版本，消费方据此拒绝不认识的载荷
	SchemaVersion = 1
)

// Connection 只管理 AMQP 连接；Channel 由 relay 与 consumer 各自持有
type Connection struct {
	conn *amqp.Connection
}

// NewConnection 按配置建立 AMQP 连接
func NewConnection(cfg config.RabbitMQConfig) (*Connection, error) {
	url := fmt.Sprintf("amqp://%s:%s@%s:%s/",
		cfg.Username,
		cfg.Password,
		cfg.Host,
		strconv.Itoa(cfg.Port),
	)
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("连接 RabbitMQ 失败: %w", err)
	}
	return &Connection{conn: conn}, nil
}

// Channel 创建独立信道，发布与消费不共享信道
func (c *Connection) Channel() (*amqp.Channel, error) {
	if c == nil || c.conn == nil {
		return nil, errors.New("rabbitmq connection is not initialized")
	}
	return c.conn.Channel()
}

// Close 关闭底层连接
func (c *Connection) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// declareChannel 抽象拓扑声明所需的信道能力，便于单测注入
type declareChannel interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
}

// DeclareTopology 声明事件交换机、处理队列与死信拓扑，重复声明为幂等操作
func DeclareTopology(ch declareChannel) error {
	if ch == nil {
		return errors.New("channel is not initialized")
	}
	if err := ch.ExchangeDeclare(EventsExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明交换机失败: %w", err)
	}
	// 死信路由键显式固定，死信在死信队列中不依赖原始路由键
	if _, err := ch.QueueDeclare(VideoProcessQueue, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    DeadLetterExchange,
		"x-dead-letter-routing-key": VideoProcessDeadQueue,
	}); err != nil {
		return fmt.Errorf("声明处理队列失败: %w", err)
	}
	if err := ch.QueueBind(VideoProcessQueue, VideoProcessRoutingKey, EventsExchange, false, nil); err != nil {
		return fmt.Errorf("绑定处理队列失败: %w", err)
	}
	if err := ch.ExchangeDeclare(DeadLetterExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明死信交换机失败: %w", err)
	}
	if _, err := ch.QueueDeclare(VideoProcessDeadQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("声明死信队列失败: %w", err)
	}
	return ch.QueueBind(VideoProcessDeadQueue, VideoProcessDeadQueue, DeadLetterExchange, false, nil)
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

// Publisher 在 confirm 模式信道上发布 JSON 消息，confirm 失败视为发布失败
type Publisher struct {
	ch confirmingPublisher
}

// NewPublisher 将信道切换到 confirm 模式并构造发布器
func NewPublisher(ch *amqp.Channel) (*Publisher, error) {
	if ch == nil {
		return nil, errors.New("channel is not initialized")
	}
	if err := ch.Confirm(false); err != nil {
		return nil, fmt.Errorf("开启发布确认失败: %w", err)
	}
	return &Publisher{ch: &amqpChannelPublisher{ch: ch}}, nil
}

// newPublisherWithSeam 供单测注入发布接缝
func newPublisherWithSeam(ch confirmingPublisher) *Publisher {
	return &Publisher{ch: ch}
}

// Publish 序列化载荷并等待 broker 确认；上下文取消或 nack 返回错误
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return p.PublishWithHeaders(ctx, exchange, routingKey, payload, nil)
}

// PublishWithHeaders 在 Publish 基础上附加自定义消息头，供重试计数等场景使用
func (p *Publisher) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
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
