package mq

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
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

// 测试目标：在既有假连接上附加队列深度读取能力
// 预期效果：QueueDepth 用例可断言查询的队列名与返回深度
type inspectableBrokerConnection struct {
	*fakeBrokerConnection

	mu       sync.Mutex
	depth    int
	depthErr error
	queried  []string
}

func (c *inspectableBrokerConnection) QueueDepth(queue string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queried = append(c.queried, queue)
	return c.depth, c.depthErr
}

func (c *inspectableBrokerConnection) queriedQueues() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queried...)
}

// 测试目标：记录每次 dial 并返回可检查队列深度的连接
// 预期效果：深度用例可统计建连次数、注入 dial 故障并按序号取回连接
type inspectableDialer struct {
	mu        sync.Mutex
	conns     []*inspectableBrokerConnection
	err       error
	depth     int
	callCount int
}

func (d *inspectableDialer) dial(_ config.RabbitMQConfig) (BrokerConnection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.callCount++
	if d.err != nil {
		return nil, d.err
	}
	conn := &inspectableBrokerConnection{fakeBrokerConnection: &fakeBrokerConnection{}, depth: d.depth}
	d.conns = append(d.conns, conn)
	return conn, nil
}

func (d *inspectableDialer) dialCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callCount
}

func (d *inspectableDialer) setErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

func (d *inspectableDialer) connection(index int) *inspectableBrokerConnection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[index]
}

// 测试目标：捕获标准日志输出并在用例结束后恢复
// 预期效果：重连日志断言不污染其他用例输出
func captureRuntimeLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var output strings.Builder
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	return &output
}

// 测试目标：验证 QueueDepth 从支持检查的连接读取队列消息数
// 预期效果：返回预设深度且查询队列名透传，健康连接复用不重复建连
func TestRuntimeQueueDepthReturnsCount(t *testing.T) {
	dialer := &inspectableDialer{depth: 5}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	// 测试目标：首建连接的深度返回预设值
	// 预期效果：深度为五且查询队列名原样透传
	first, err := runtime.QueueDepth("video.process.dead")
	if err != nil || first != 5 {
		t.Fatalf("队列深度错误 got=%d err=%v", first, err)
	}
	if queues := dialer.connection(0).queriedQueues(); len(queues) != 1 || queues[0] != "video.process.dead" {
		t.Fatalf("应查询指定队列 got=%v", queues)
	}

	// 测试目标：健康连接复用
	// 预期效果：第二次读取不重新建连
	if _, err := runtime.QueueDepth("video.process.dead"); err != nil {
		t.Fatalf("第二次读取失败: %v", err)
	}
	if dialer.dialCalls() != 1 {
		t.Fatalf("健康连接不应重复建立 got=%d", dialer.dialCalls())
	}
}

// 测试目标：验证连接不支持 QueueDepthReader 时返回明确错误
// 预期效果：错误说明连接不支持队列检查，且不因能力缺失触发重连
func TestRuntimeQueueDepthRejectsUninspectableConnection(t *testing.T) {
	dialer := &dialerRecorder{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	for i := 0; i < 2; i++ {
		_, err := runtime.QueueDepth("video.process.dead")
		if err == nil || !strings.Contains(err.Error(), "does not support queue inspection") {
			t.Fatalf("应返回不支持检查的明确错误 got=%v", err)
		}
	}
	if dialer.dialCalls() != 1 {
		t.Fatalf("检查能力缺失不应重建连接 got=%d", dialer.dialCalls())
	}
}

// 测试目标：验证 QueueDepth 在 runtime 关闭与未初始化时的边界
// 预期效果：Close 后返回 ErrRuntimeClosed，空 runtime 返回未初始化错误
func TestRuntimeQueueDepthRejectsClosedOrMissingRuntime(t *testing.T) {
	dialer := &inspectableDialer{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	if _, err := runtime.QueueDepth("q"); err != nil {
		t.Fatalf("关闭前读取失败: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("关闭 runtime 失败: %v", err)
	}
	if _, err := runtime.QueueDepth("q"); !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("关闭后应返回 ErrRuntimeClosed got=%v", err)
	}

	var missing *Runtime
	if _, err := missing.QueueDepth("q"); !errors.Is(err, errRuntimeNotInitialized) {
		t.Fatalf("空 runtime 应返回未初始化错误 got=%v", err)
	}
}

// 测试目标：验证旧连接断开重连时记录 mq_reconnect 成败日志
// 预期效果：失败日志带 duration_ms 与 error 字段，成功日志带 duration_ms 字段
func TestRuntimeQueueDepthLogsReconnect(t *testing.T) {
	logs := captureRuntimeLogs(t)
	dialer := &inspectableDialer{}
	runtime := NewRuntime(config.RabbitMQConfig{}, WithDialer(dialer.dial))
	defer runtime.Close()

	if _, err := runtime.QueueDepth("q"); err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	if err := dialer.connection(0).Close(); err != nil {
		t.Fatalf("关闭底层连接失败: %v", err)
	}

	// 测试目标：重连 dial 失败时记录 failed 事件
	// 预期效果：日志包含 result=failed、duration_ms 与 error 字段
	dialer.setErr(errors.New("broker unreachable"))
	if _, err := runtime.QueueDepth("q"); err == nil {
		t.Fatal("重连失败应返回错误")
	}
	// 测试目标：重连成功后恢复深度读取
	// 预期效果：日志包含 result=success 与 duration_ms 字段
	dialer.setErr(nil)
	if _, err := runtime.QueueDepth("q"); err != nil {
		t.Fatalf("重连后读取失败: %v", err)
	}

	output := logs.String()
	for _, field := range []string{
		"event=mq_reconnect result=failed duration_ms=",
		`error="broker unreachable"`,
		"event=mq_reconnect result=success duration_ms=",
	} {
		if !strings.Contains(output, field) {
			t.Fatalf("重连日志缺少 %s got=%q", field, output)
		}
	}
}
