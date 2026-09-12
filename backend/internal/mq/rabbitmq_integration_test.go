package mq

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"gofeed/internal/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 测试目标：读取集成测试使用的真实 RabbitMQ 配置
// 预期效果：未配置时跳过并保留可见的跳过原因
func integrationRabbitMQConfig(t *testing.T) config.RabbitMQConfig {
	t.Helper()
	if os.Getenv("RABBITMQ_HOST") == "" {
		t.Skip("需要真实 RabbitMQ：设置 RABBITMQ_HOST（及 RABBITMQ_PORT、RABBITMQ_DEFAULT_USER、RABBITMQ_DEFAULT_PASS）后重跑")
	}
	cfg := config.Config{}
	config.OverrideWithEnv(&cfg)
	if cfg.RabbitMQ.Port == 0 {
		cfg.RabbitMQ.Port = 5672
	}
	return cfg.RabbitMQ
}

// 测试目标：构造测试专用原生 AMQP 连接地址
// 预期效果：用户名与密码按 URL 规则转义，与 runtime 指向同一 broker
func integrationAMQPURL(cfg config.RabbitMQConfig) string {
	return (&url.URL{
		Scheme: "amqp",
		User:   url.UserPassword(cfg.Username, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:   "/",
	}).String()
}

// 测试目标：建立测试专用的原生 AMQP 连接
// 预期效果：提供拓扑检查能力，broker 不可达时跳过
func integrationAdminConnection(t *testing.T) *amqp.Connection {
	t.Helper()
	cfg := integrationRabbitMQConfig(t)
	conn, err := amqp.Dial(integrationAMQPURL(cfg))
	if err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// 测试目标：包装真实 broker 连接以统计拓扑声明次数
// 预期效果：保留全部 BrokerConnection 能力并额外暴露声明计数
type recordingBrokerConnection struct {
	BrokerConnection
	declares int32
}

func (c *recordingBrokerConnection) DeclareTopology() error {
	atomic.AddInt32(&c.declares, 1)
	return c.BrokerConnection.DeclareTopology()
}

func (c *recordingBrokerConnection) declarations() int {
	return int(atomic.LoadInt32(&c.declares))
}

// 测试目标：记录 runtime 在真实 broker 上建立的每条连接
// 预期效果：用例可按序号取回连接并关闭底层连接
type recordingDialer struct {
	mu    sync.Mutex
	conns []*recordingBrokerConnection
}

func (d *recordingDialer) dial(cfg config.RabbitMQConfig) (BrokerConnection, error) {
	conn, err := dialAMQP(cfg)
	if err != nil {
		return nil, err
	}
	wrapped := &recordingBrokerConnection{BrokerConnection: conn}
	d.mu.Lock()
	d.conns = append(d.conns, wrapped)
	d.mu.Unlock()
	return wrapped, nil
}

func (d *recordingDialer) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

func (d *recordingDialer) connection(index int) *recordingBrokerConnection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[index]
}

// 测试目标：验证重连后主队列、分级重试队列与死信拓扑在 broker 上齐备
// 预期效果：被动声明全部成功，说明新连接恢复了完整拓扑
func assertTopologyPresent(t *testing.T, conn *amqp.Connection, spec ConsumerSpec) {
	t.Helper()
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("创建拓扑检查信道失败: %v", err)
	}
	defer channel.Close()

	queues := []string{spec.Queue, spec.DeadLetterQueueName()}
	for index := range spec.Retry.Delays {
		queues = append(queues, spec.RetryQueueName(index))
	}
	for _, queue := range queues {
		if _, err := channel.QueueDeclarePassive(queue, true, false, false, false, nil); err != nil {
			t.Fatalf("队列 %s 拓扑缺失: %v", queue, err)
		}
	}
}

// 测试目标：验证真实 broker 上底层连接被关闭后 runtime 自动重连并重新声明拓扑
// 预期效果：下一次发布触发重新 dial，新连接完成拓扑声明，队列与重试死信拓扑仍可访问
func TestRuntimeReconnectsAgainstRealBroker(t *testing.T) {
	cfg := integrationRabbitMQConfig(t)
	spec := VideoProcessSpec()
	admin := integrationAdminConnection(t)

	dialer := &recordingDialer{}
	runtime := NewRuntime(cfg, WithDialer(dialer.dial))
	defer func() { _ = runtime.Close() }()
	if err := runtime.EnsureConnected(); err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}
	if dialer.calls() != 1 {
		t.Fatalf("启动期应只建立一条连接 got=%d", dialer.calls())
	}

	// 测试目标：使用不匹配任何绑定的探针路由键发布
	// 预期效果：只验证发布确认与重连，不污染 worker 集成用例共享的处理队列
	probeKey := spec.Event.RoutingKey + ".probe"
	ctx := context.Background()
	if err := runtime.Publish(ctx, spec.Event.Exchange, probeKey, map[string]any{"probe": 1}); err != nil {
		t.Fatalf("首次发布失败: %v", err)
	}
	held := dialer.connection(0)
	if held.declarations() != 1 {
		t.Fatalf("首次连接应声明一次拓扑 got=%d", held.declarations())
	}

	// 测试目标：关闭 runtime 当前持有的底层连接模拟意外断线
	// 预期效果：runtime 不感知人工关闭，下一次调用自行重建
	if err := held.Close(); err != nil {
		t.Fatalf("关闭底层连接失败: %v", err)
	}
	if !held.IsClosed() {
		t.Fatal("关闭后的底层连接应视为已关闭")
	}

	if err := runtime.Publish(ctx, spec.Event.Exchange, probeKey, map[string]any{"probe": 2}); err != nil {
		t.Fatalf("重连后发布失败: %v", err)
	}
	if dialer.calls() != 2 {
		t.Fatalf("断线后应重新建立连接 got=%d", dialer.calls())
	}
	reconnected := dialer.connection(1)
	if reconnected.declarations() != 1 {
		t.Fatalf("新连接应重新声明拓扑 got=%d", reconnected.declarations())
	}
	assertTopologyPresent(t, admin, spec)
}
