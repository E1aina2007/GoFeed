package mq

import (
	"context"
	"errors"
	"sync"
	"testing"

	"gofeed/internal/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 测试目标：模拟一条可被关闭的 broker 连接
// 预期效果：记录拓扑声明、发布器与消费信道创建次数，供断言重连行为
type fakeBrokerConnection struct {
	mu             sync.Mutex
	closed         bool
	topologyCalls  int
	publisherCalls int
	consumerCalls  int
	topologyErr    error
	publisherErr   error
	consumerErr    error
	seam           *fakePublishChannel
}

func (c *fakeBrokerConnection) DeclareTopology() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.topologyCalls++
	return c.topologyErr
}

func (c *fakeBrokerConnection) NewConfirmingPublisher() (confirmingPublisher, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publisherCalls++
	if c.publisherErr != nil {
		return nil, c.publisherErr
	}
	if c.seam == nil {
		c.seam = &fakePublishChannel{confirmed: true}
	}
	return c.seam, nil
}

func (c *fakeBrokerConnection) NewConsumerChannel(prefetch int) (ConsumerChannel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consumerCalls++
	if c.consumerErr != nil {
		return nil, c.consumerErr
	}
	return &fakeConsumerChannel{queue: "fake"}, nil
}

func (c *fakeBrokerConnection) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *fakeBrokerConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *fakeBrokerConnection) calls() (topology, publisher, consumer int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.topologyCalls, c.publisherCalls, c.consumerCalls
}

// 测试目标：提供空的投递通道以满足 ConsumerChannel 契约
// 预期效果：消费信道用例可断言创建次数而不需要真实投递
type fakeConsumerChannel struct {
	queue string
}

func (c *fakeConsumerChannel) Consume(_ string) (<-chan amqp.Delivery, error) {
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil
}

func (c *fakeConsumerChannel) Close() error { return nil }

// 测试目标：记录每次 dial 并返回预设连接
// 预期效果：用例可统计建连次数并按序号取回具体连接
type dialerRecorder struct {
	mu        sync.Mutex
	conns     []*fakeBrokerConnection
	err       error
	callCount int
}

func (d *dialerRecorder) dial(_ config.RabbitMQConfig) (BrokerConnection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.callCount++
	if d.err != nil {
		return nil, d.err
	}
	conn := &fakeBrokerConnection{}
	d.conns = append(d.conns, conn)
	return conn, nil
}

// 测试目标：让每次 dial 都返回拓扑声明失败的连接
// 预期效果：用例可验证失败连接不被缓存
type dialerWithFailingTopology struct {
	mu        sync.Mutex
	callCount int
}

func (d *dialerWithFailingTopology) dial(_ config.RabbitMQConfig) (BrokerConnection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.callCount++
	return &fakeBrokerConnection{topologyErr: errors.New("declare failed")}, nil
}

func (d *dialerWithFailingTopology) dialCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callCount
}

func (d *dialerRecorder) dialCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callCount
}

func (d *dialerRecorder) connection(index int) *fakeBrokerConnection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[index]
}

// 测试目标：验证 runtime 复用健康连接且不重复建立
// 预期效果：多次发布只建立一次连接与一次拓扑声明
func TestRuntimeReusesHealthyConnection(t *testing.T) {
	dialer := &dialerRecorder{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	for i := 0; i < 3; i++ {
		if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{"n": i}); err != nil {
			t.Fatalf("发布失败: %v", err)
		}
	}
	if dialer.dialCalls() != 1 {
		t.Fatalf("健康连接不应重复建立 got=%d", dialer.dialCalls())
	}
	topology, publisher, _ := dialer.connection(0).calls()
	if topology != 1 || publisher != 1 {
		t.Fatalf("拓扑与发布器应只创建一次 topology=%d publisher=%d", topology, publisher)
	}
}

// 测试目标：验证关闭 runtime 当前持有的底层连接后自动重连
// 预期效果：下一次发布重新 dial，新连接重新声明拓扑并重建发布器
func TestRuntimeReconnectsAfterHeldConnectionClosed(t *testing.T) {
	dialer := &dialerRecorder{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); err != nil {
		t.Fatalf("首次发布失败: %v", err)
	}
	held := dialer.connection(0)
	if err := held.Close(); err != nil {
		t.Fatalf("关闭底层连接失败: %v", err)
	}
	if !held.IsClosed() {
		t.Fatal("关闭后的底层连接应视为已关闭")
	}

	if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); err != nil {
		t.Fatalf("重连后发布失败: %v", err)
	}
	if dialer.dialCalls() != 2 {
		t.Fatalf("底层连接关闭后应重新建立连接 got=%d", dialer.dialCalls())
	}
	topology, publisher, _ := dialer.connection(1).calls()
	if topology != 1 || publisher != 1 {
		t.Fatalf("新连接应重新声明拓扑并重建发布器 topology=%d publisher=%d", topology, publisher)
	}
}

// 测试目标：验证 Close 后 runtime 进入终止状态且不再建立连接
// 预期效果：EnsureConnected、Publish、ConsumerChannel 均返回 ErrRuntimeClosed，dial 次数不再增加
func TestRuntimeCloseRejectsFurtherOperations(t *testing.T) {
	dialer := &dialerRecorder{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))

	if err := runtime.EnsureConnected(); err != nil {
		t.Fatalf("启动期建连失败: %v", err)
	}
	before := dialer.dialCalls()
	if err := runtime.Close(); err != nil {
		t.Fatalf("关闭 runtime 失败: %v", err)
	}
	if !dialer.connection(0).IsClosed() {
		t.Fatal("Close 应关闭 runtime 当前持有的连接")
	}

	if err := runtime.EnsureConnected(); !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("Close 后 EnsureConnected 应返回 ErrRuntimeClosed got=%v", err)
	}
	if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("Close 后 Publish 应返回 ErrRuntimeClosed got=%v", err)
	}
	if _, err := runtime.ConsumerChannel(16); !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("Close 后 ConsumerChannel 应返回 ErrRuntimeClosed got=%v", err)
	}
	if dialer.dialCalls() != before {
		t.Fatalf("Close 后不应再建立连接 got=%d want=%d", dialer.dialCalls(), before)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("重复 Close 不应报错: %v", err)
	}
	if dialer.dialCalls() != before {
		t.Fatalf("重复 Close 不应建立连接 got=%d want=%d", dialer.dialCalls(), before)
	}
}

// 测试目标：验证发布失败时丢弃连接，避免复用已失效信道
// 预期效果：下一次发布重新建立连接并成功
func TestRuntimeDropsConnectionWhenPublishFails(t *testing.T) {
	dialer := &dialerRecorder{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); err != nil {
		t.Fatalf("首次发布失败: %v", err)
	}
	dialer.connection(0).seam.publishErr = errors.New("channel closed")

	if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); err == nil {
		t.Fatal("发布失败应返回错误")
	}
	if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, map[string]any{}); err != nil {
		t.Fatalf("重建连接后发布失败: %v", err)
	}
	if dialer.dialCalls() != 2 {
		t.Fatalf("发布失败后应重建连接 got=%d", dialer.dialCalls())
	}
}

// 测试目标：验证拓扑声明失败时不缓存连接，下一次调用重新尝试
// 预期效果：返回错误且每次调用都重新 dial
func TestRuntimeRetriesWhenTopologyFails(t *testing.T) {
	dialer := &dialerWithFailingTopology{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	if err := runtime.EnsureConnected(); err == nil {
		t.Fatal("拓扑声明失败应返回错误")
	}
	if err := runtime.EnsureConnected(); err == nil {
		t.Fatal("拓扑声明失败应再次返回错误")
	}
	if dialer.dialCalls() != 2 {
		t.Fatalf("失败连接不应被缓存 got=%d", dialer.dialCalls())
	}
}

// 测试目标：验证消费信道从当前连接获取并在连接重建后重新获取
// 预期效果：底层连接关闭前后各创建一个消费信道，且不因信道创建失败丢弃连接
func TestRuntimeConsumerChannelFollowsConnection(t *testing.T) {
	dialer := &dialerRecorder{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	if _, err := runtime.ConsumerChannel(16); err != nil {
		t.Fatalf("首次获取消费信道失败: %v", err)
	}
	if err := dialer.connection(0).Close(); err != nil {
		t.Fatalf("关闭底层连接失败: %v", err)
	}
	if _, err := runtime.ConsumerChannel(16); err != nil {
		t.Fatalf("重连后获取消费信道失败: %v", err)
	}
	if dialer.dialCalls() != 2 {
		t.Fatalf("底层连接关闭后应重建连接 got=%d", dialer.dialCalls())
	}
	_, _, consumer := dialer.connection(1).calls()
	if consumer != 1 {
		t.Fatalf("新连接应创建消费信道 got=%d", consumer)
	}
}

// 测试目标：验证 dial 失败时返回错误且不缓存半成品连接
// 预期效果：连续调用持续重试并透传错误
func TestRuntimePropagatesDialError(t *testing.T) {
	dialer := &dialerRecorder{err: errors.New("broker unreachable")}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	for i := 0; i < 2; i++ {
		if err := runtime.EnsureConnected(); err == nil {
			t.Fatal("dial 失败应返回错误")
		}
	}
	if dialer.dialCalls() != 2 {
		t.Fatalf("每次调用都应尝试 dial got=%d", dialer.dialCalls())
	}
}

// 测试目标：验证未注入 dialer 的空 runtime 不会空指针
// 预期效果：显式返回初始化错误，Close 幂等返回
func TestRuntimeNilSafe(t *testing.T) {
	var runtime *Runtime
	if err := runtime.EnsureConnected(); err == nil {
		t.Fatal("空 runtime 应返回错误")
	}
	if err := runtime.Publish(context.Background(), EventsExchange, VideoProcessRoutingKey, nil); err == nil {
		t.Fatal("空 runtime 发布应返回错误")
	}
	if _, err := runtime.ConsumerChannel(1); err == nil {
		t.Fatal("空 runtime 获取消费信道应返回错误")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("空 runtime 关闭不应报错: %v", err)
	}
}

// 测试目标：验证真实 AMQP 适配器满足 BrokerConnection 契约
// 预期效果：类型断言成立，连接关闭后 IsClosed 为真
func TestAmqpBrokerConnectionContract(t *testing.T) {
	var _ BrokerConnection = (*amqpBrokerConnection)(nil)
	conn := &amqpBrokerConnection{}
	if !conn.IsClosed() {
		t.Fatal("空连接应视为已关闭")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("关闭空连接不应报错: %v", err)
	}
}

// 测试目标：验证 WithDialer 忽略 nil 且保留默认实现
// 预期效果：注入 nil 后 runtime 仍可构造
func TestWithDialerIgnoresNil(t *testing.T) {
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(nil))
	if runtime.dial == nil {
		t.Fatal("默认 dialer 不应被 nil 覆盖")
	}
}
