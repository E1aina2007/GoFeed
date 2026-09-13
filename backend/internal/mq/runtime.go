package mq

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"gofeed/internal/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

var errRuntimeNotInitialized = errors.New("mq: runtime is not initialized")

// ErrRuntimeClosed 表示 runtime 已进入终止状态，不能再建立连接或创建信道
var ErrRuntimeClosed = errors.New("mq: runtime is closed")

// ConsumerChannel 抽象消费信道的注册与关闭，供 runtime 在连接重建后提供新信道
type ConsumerChannel interface {
	Consume(queue string) (<-chan amqp.Delivery, error)
	Close() error
}

// BrokerConnection 抽象一条 broker 连接的生命周期，隔离具体 AMQP 实现
type BrokerConnection interface {
	// DeclareTopology 声明本连接所需的全部拓扑，重复调用幂等
	DeclareTopology() error
	// NewConfirmingPublisher 返回绑定本连接的确认发布器
	NewConfirmingPublisher() (confirmingPublisher, error)
	// NewConsumerChannel 返回已设置预取的消费信道
	NewConsumerChannel(prefetch int) (ConsumerChannel, error)
	// IsClosed 在连接断开后返回 true
	IsClosed() bool
	// Close 关闭连接
	Close() error
}

// Dialer 建立 broker 连接；单测注入假实现以模拟断线与重连
type Dialer func(config.RabbitMQConfig) (BrokerConnection, error)

// RuntimeOption 覆盖 runtime 的可注入依赖
type RuntimeOption func(*Runtime)

// WithDialer 注入自定义连接建立函数
func WithDialer(dial Dialer) RuntimeOption {
	return func(r *Runtime) {
		if dial != nil {
			r.dial = dial
		}
	}
}

// Runtime 管理 AMQP 连接与发布器生命周期
// 连接断开后自动重新建立并恢复拓扑，避免运行中依赖人工重启
type Runtime struct {
	cfg  config.RabbitMQConfig
	dial Dialer

	mu            sync.Mutex
	conn          BrokerConnection
	pub           *publisher
	closed        bool
	connectedOnce bool
}

// NewRuntime 构造运行时；未注入 dialer 时使用真实 AMQP 连接
func NewRuntime(cfg config.RabbitMQConfig, options ...RuntimeOption) *Runtime {
	runtime := &Runtime{cfg: cfg, dial: dialAMQP}
	for _, option := range options {
		if option != nil {
			option(runtime)
		}
	}
	return runtime
}

// EnsureConnected 确保当前存在可用连接并已声明拓扑，供启动期快速暴露依赖故障
func (r *Runtime) EnsureConnected() error {
	if r == nil {
		return errRuntimeNotInitialized
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.ensureLocked()
	return err
}

// Publish 在可用连接上发布消息，失败时丢弃当前连接交由下次调用重建
func (r *Runtime) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return r.PublishWithHeaders(ctx, exchange, routingKey, payload, nil)
}

// PublishWithHeaders 在 Publish 基础上附加消息头
func (r *Runtime) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
	if r == nil {
		return errRuntimeNotInitialized
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	conn, err := r.ensureLocked()
	if err != nil {
		return err
	}
	publisher, err := r.publisherLocked(conn)
	if err != nil {
		_ = r.dropLocked()
		return err
	}
	if err := publisher.PublishWithHeaders(ctx, exchange, routingKey, payload, headers); err != nil {
		// 发布失败可能是信道或连接已失效，丢弃后由下次调用重新建立
		_ = r.dropLocked()
		return err
	}
	return nil
}

// ConsumerChannel 返回绑定当前连接的消费信道，连接失效时先重建连接
func (r *Runtime) ConsumerChannel(prefetch int) (ConsumerChannel, error) {
	if r == nil {
		return nil, errRuntimeNotInitialized
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	conn, err := r.ensureLocked()
	if err != nil {
		return nil, err
	}
	// 单条信道创建失败不丢弃连接：连接真正断开时由下次 IsClosed 判定重建
	return conn.NewConsumerChannel(prefetch)
}

// QueueDepth 返回指定队列当前可见消息数，供运维快照采集使用
func (r *Runtime) QueueDepth(queue string) (int, error) {
	if r == nil {
		return 0, errRuntimeNotInitialized
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	conn, err := r.ensureLocked()
	if err != nil {
		return 0, err
	}
	inspector, ok := conn.(QueueDepthReader)
	if !ok {
		return 0, errors.New("mq: broker connection does not support queue inspection")
	}
	return inspector.QueueDepth(queue)
}

// Close 永久关闭 runtime 并释放当前连接，后续调用不会重新建立连接
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.dropLocked()
}

// ensureLocked 返回可用连接；缺失或已关闭时重新建立并恢复拓扑
func (r *Runtime) ensureLocked() (BrokerConnection, error) {
	if r == nil || r.dial == nil {
		return nil, errRuntimeNotInitialized
	}
	if r.closed {
		return nil, ErrRuntimeClosed
	}
	if r.conn != nil && !r.conn.IsClosed() {
		return r.conn, nil
	}
	// 旧连接已失效：先关闭再重建，发布器必须随连接一起丢弃
	_ = r.dropLocked()

	reconnecting := r.connectedOnce
	startedAt := time.Now()
	conn, err := r.dial(r.cfg)
	if err != nil {
		if reconnecting {
			log.Printf("event=%s result=failed duration_ms=%d error=%q", ObservationEventReconnect, time.Since(startedAt).Milliseconds(), err)
		}
		return nil, err
	}
	if err := conn.DeclareTopology(); err != nil {
		_ = conn.Close()
		if reconnecting {
			log.Printf("event=%s result=failed duration_ms=%d error=%q", ObservationEventReconnect, time.Since(startedAt).Milliseconds(), err)
		}
		return nil, err
	}
	r.conn = conn
	r.connectedOnce = true
	if reconnecting {
		log.Printf("event=%s result=success duration_ms=%d", ObservationEventReconnect, time.Since(startedAt).Milliseconds())
	}
	return conn, nil
}

// publisherLocked 返回绑定当前连接的发布器，连接重建后重新创建
func (r *Runtime) publisherLocked(conn BrokerConnection) (*publisher, error) {
	if r.pub != nil {
		return r.pub, nil
	}
	seam, err := conn.NewConfirmingPublisher()
	if err != nil {
		return nil, err
	}
	r.pub = newPublisherWithSeam(seam)
	return r.pub, nil
}

// dropLocked 关闭并清空当前连接与发布器
func (r *Runtime) dropLocked() error {
	var err error
	if r.conn != nil {
		err = r.conn.Close()
	}
	r.conn = nil
	r.pub = nil
	return err
}

// amqpBrokerConnection 将 *amqp.Connection 适配为 BrokerConnection
type amqpBrokerConnection struct {
	conn *amqp.Connection
}

// dialAMQP 使用真实 AMQP 建立连接
func dialAMQP(cfg config.RabbitMQConfig) (BrokerConnection, error) {
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
	return &amqpBrokerConnection{conn: conn}, nil
}

func (c *amqpBrokerConnection) DeclareTopology() error {
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("打开拓扑信道失败: %w", err)
	}
	defer ch.Close()
	return DeclareTopology(ch)
}

func (c *amqpBrokerConnection) NewConfirmingPublisher() (confirmingPublisher, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("打开发布信道失败: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("开启发布确认失败: %w", err)
	}
	return &amqpChannelPublisher{ch: ch}, nil
}

func (c *amqpBrokerConnection) NewConsumerChannel(prefetch int) (ConsumerChannel, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("打开消费信道失败: %w", err)
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("设置消费预取失败: %w", err)
	}
	return &amqpConsumerChannel{ch: ch}, nil
}

func (c *amqpBrokerConnection) IsClosed() bool {
	return c.conn == nil || c.conn.IsClosed()
}

func (c *amqpBrokerConnection) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *amqpBrokerConnection) QueueDepth(queue string) (int, error) {
	if c == nil || c.conn == nil {
		return 0, errRuntimeNotInitialized
	}
	ch, err := c.conn.Channel()
	if err != nil {
		return 0, fmt.Errorf("打开队列检查信道失败: %w", err)
	}
	defer ch.Close()
	info, err := ch.QueueInspect(queue)
	if err != nil {
		return 0, fmt.Errorf("检查队列 %s 失败: %w", queue, err)
	}
	return info.Messages, nil
}

// amqpConsumerChannel 将 *amqp.Channel 适配为 ConsumerChannel
type amqpConsumerChannel struct {
	ch *amqp.Channel
}

func (c *amqpConsumerChannel) Consume(queue string) (<-chan amqp.Delivery, error) {
	return c.ch.Consume(queue, "", false, false, false, false, nil)
}

func (c *amqpConsumerChannel) Close() error {
	return c.ch.Close()
}
