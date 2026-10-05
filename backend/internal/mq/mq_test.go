package mq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"

	"gofeed/internal/config"
)

// 测试目标：记录发布调用并模拟 broker 确认行为
// 预期效果：用例可断言消息体、路由键与消息头，也可注入未确认或信道错误
type fakePublishChannel struct {
	published   []amqp.Publishing
	routingKeys []string
	headers     []amqp.Table
	confirmed   bool
	publishErr  error
}

func (c *fakePublishChannel) publishAndWait(_ context.Context, _, routingKey string, headers amqp.Table, msg amqp.Publishing) error {
	if c.publishErr != nil {
		return c.publishErr
	}
	c.published = append(c.published, msg)
	c.routingKeys = append(c.routingKeys, routingKey)
	c.headers = append(c.headers, headers)
	if !c.confirmed {
		return errors.New("broker 未确认消息")
	}
	return nil
}

// 测试目标：测试进程启动时补充加载 backend/.env 的集成测试配置
// 预期效果：真实 RabbitMQ 集成用例在标准测试命令下不再因缺少环境变量而跳过，已注入的环境变量优先级不变
func TestMain(m *testing.M) {
	loadBackendDotEnv()
	os.Exit(m.Run())
}

// 测试目标：通过测试源文件位置定位 backend/.env
// 预期效果：不依赖测试进程工作目录即可找到配置文件，文件缺失时静默跳过并保留原有跳过行为
func loadBackendDotEnv() {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return
	}
	// CI 通过显式环境变量注入配置，缺失文件属正常场景
	_ = godotenv.Load(filepath.Join(filepath.Dir(file), "..", "..", ".env"))
}

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

// 测试目标：在超时内轮询到期望的队列深度
// 预期效果：broker 计数最终一致时不产生抖动失败
func waitForQueueDepth(t *testing.T, runtime *Runtime, queue string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		depth, err := runtime.QueueDepth(queue)
		if err != nil {
			t.Fatalf("读取队列深度失败: %v", err)
		}
		if depth == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("队列 %s 深度未达期望 got=%d want=%d", queue, depth, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// 测试目标：验证真实 QueueInspect 能读取隔离临时队列深度
// 预期效果：放入探针消息后随机队列深度增加，删除队列后不残留 broker 状态
func TestRuntimeQueueDepthReadsRealDedicatedQueue(t *testing.T) {
	cfg := integrationRabbitMQConfig(t)

	admin := integrationAdminConnection(t)
	channel, err := admin.Channel()
	if err != nil {
		t.Fatalf("创建拓扑信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	queue := fmt.Sprintf("gofeed.test.queue-depth.%d", time.Now().UnixNano())
	if _, err := channel.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		t.Fatalf("声明隔离测试队列失败: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := admin.Channel()
		if cleanupErr != nil {
			t.Errorf("创建队列删除信道失败: %v", cleanupErr)
			return
		}
		defer cleanup.Close()
		if _, cleanupErr = cleanup.QueueDelete(queue, false, false, false); cleanupErr != nil {
			t.Errorf("删除隔离测试队列失败: %v", cleanupErr)
		}
	})

	runtime := NewRuntime(cfg)
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.EnsureConnected(); err != nil {
		t.Skipf("RabbitMQ 不可达，集成测试跳过: %v", err)
	}

	baseline, err := runtime.QueueDepth(queue)
	if err != nil {
		t.Fatalf("读取基线深度失败: %v", err)
	}
	if baseline != 0 {
		t.Fatalf("隔离测试队列初始深度应为零 got=%d", baseline)
	}

	// 测试目标：向隔离队列放入一条测试消息
	// 预期效果：QueueInspect 读到的深度为一
	if err := channel.PublishWithContext(context.Background(), "", queue, false, false,
		amqp.Publishing{ContentType: "application/json", Body: []byte(`{"probe":"queue-depth"}`)}); err != nil {
		t.Fatalf("发布探针消息失败: %v", err)
	}
	waitForQueueDepth(t, runtime, queue, 1)
}

// warmExchangeRecord 记录一次交换机声明的完整参数
type warmExchangeRecord struct {
	name    string
	kind    string
	durable bool
}

// warmQueueRecord 记录一次队列声明的完整参数
type warmQueueRecord struct {
	name       string
	durable    bool
	autoDelete bool
	exclusive  bool
	args       amqp.Table
}

// warmBindRecord 记录一次队列绑定
type warmBindRecord struct {
	exchange string
	key      string
	queue    string
}

// warmRecordingChannel 记录拓扑声明所需的全部信道调用与参数
type warmRecordingChannel struct {
	exchanges []warmExchangeRecord
	queues    []warmQueueRecord
	binds     []warmBindRecord
}

// warmTab 读取 amqp.Table 中的键，同时兼容未设置与类型不符两种情况
func warmTab(args amqp.Table, key string) any {
	switch typed := any(args).(type) {
	case nil:
		return nil
	case amqp.Table:
		if typed == nil {
			return nil
		}
		return typed[key]
	default:
		return nil
	}
}

// warmExchangeIndex 返回指定交换机名的声明下标，缺失时返回负一
func (c *warmRecordingChannel) warmExchangeIndex(name string) int {
	for index, record := range c.exchanges {
		if record.name == name {
			return index
		}
	}
	return -1
}

// warmExchangeCount 统计同名交换机的声明次数
func (c *warmRecordingChannel) warmExchangeCount(name string) int {
	count := 0
	for _, record := range c.exchanges {
		if record.name == name {
			count++
		}
	}
	return count
}

// warmQueue 返回指定队列名的声明记录，缺失时返回 false
func (c *warmRecordingChannel) warmQueue(name string) (warmQueueRecord, bool) {
	for _, record := range c.queues {
		if record.name == name {
			return record, true
		}
	}
	return warmQueueRecord{}, false
}

// warmQueueNames 返回本次声明涉及的全部队列名
func (c *warmRecordingChannel) warmQueueNames() map[string]struct{} {
	names := make(map[string]struct{}, len(c.queues))
	for _, record := range c.queues {
		names[record.name] = struct{}{}
	}
	return names
}

// warmQueueArgsText 返回指定队列的声明参数文本，便于失败时定位差异
func (c *warmRecordingChannel) warmQueueArgsText(name string) string {
	record, ok := c.warmQueue(name)
	if !ok {
		return "<队列未声明>"
	}
	return fmt.Sprintf("%v", record.args)
}

// warmBindExists 判断指定绑定是否存在
func (c *warmRecordingChannel) warmBindExists(exchange, key, queue string) bool {
	for _, record := range c.binds {
		if record.exchange == exchange && record.key == key && record.queue == queue {
			return true
		}
	}
	return false
}

// warmBoundQueues 返回某个交换机上被绑定过的队列名集合
func (c *warmRecordingChannel) warmBoundQueues(exchange string) map[string]struct{} {
	queues := make(map[string]struct{})
	for _, record := range c.binds {
		if record.exchange == exchange {
			queues[record.queue] = struct{}{}
		}
	}
	return queues
}

func (c *warmRecordingChannel) ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error {
	c.exchanges = append(c.exchanges, warmExchangeRecord{name: name, kind: kind, durable: durable})
	return nil
}

func (c *warmRecordingChannel) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	c.queues = append(c.queues, warmQueueRecord{name: name, durable: durable, autoDelete: autoDelete, exclusive: exclusive, args: args})
	return amqp.Queue{Name: name}, nil
}

func (c *warmRecordingChannel) QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error {
	c.binds = append(c.binds, warmBindRecord{exchange: exchange, key: key, queue: name})
	return nil
}

// warmRecordingBrokerConnection 记录 runtime 在每条连接上执行的拓扑恢复调用
type warmRecordingBrokerConnection struct {
	BrokerConnection

	mu             sync.Mutex
	closed         bool
	topologyCalls  int
	specCalls      [][]ConsumerSpec
	publisherCalls int
	seam           *fakePublishChannel
}

func (c *warmRecordingBrokerConnection) DeclareTopology() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.topologyCalls++
	return nil
}

func (c *warmRecordingBrokerConnection) DeclareConsumerTopology(specs ...ConsumerSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.specCalls = append(c.specCalls, append([]ConsumerSpec(nil), specs...))
	return nil
}

func (c *warmRecordingBrokerConnection) NewConfirmingPublisher() (confirmingPublisher, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publisherCalls++
	if c.seam == nil {
		c.seam = &fakePublishChannel{confirmed: true}
	}
	return c.seam, nil
}

func (c *warmRecordingBrokerConnection) NewConsumerChannel(prefetch int) (ConsumerChannel, error) {
	return &fakeConsumerChannel{queue: "warm"}, nil
}

func (c *warmRecordingBrokerConnection) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *warmRecordingBrokerConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// warmSpecsSnapshot 返回逐次拓扑恢复收到的消费规格，恢复时复制切片避免共享可变状态
func (c *warmRecordingBrokerConnection) warmSpecsSnapshot() [][]ConsumerSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := make([][]ConsumerSpec, 0, len(c.specCalls))
	for _, specs := range c.specCalls {
		copied := make([]ConsumerSpec, 0, len(specs))
		for _, spec := range specs {
			spec.Retry.Delays = append([]time.Duration(nil), spec.Retry.Delays...)
			copied = append(copied, spec)
		}
		snapshot = append(snapshot, copied)
	}
	return snapshot
}

// warmTopologyCalls 返回回退 DeclareTopology 的调用次数
func (c *warmRecordingBrokerConnection) warmTopologyCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.topologyCalls
}

// warmRecordingDialer 每次建连返回一条独立的记录连接
type warmRecordingDialer struct {
	mu    sync.Mutex
	conns []*warmRecordingBrokerConnection
	calls int
}

func (d *warmRecordingDialer) dial(_ config.RabbitMQConfig) (BrokerConnection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	conn := &warmRecordingBrokerConnection{}
	d.conns = append(d.conns, conn)
	return conn, nil
}

func (d *warmRecordingDialer) warmCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *warmRecordingDialer) warmConnection(index int) *warmRecordingBrokerConnection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[index]
}

// warmExpectedQueues 返回一个规格应当声明的完整队列名集合
func warmExpectedQueues(spec ConsumerSpec) map[string]struct{} {
	names := map[string]struct{}{spec.Queue: {}, spec.DeadLetterQueueName(): {}}
	for index := range spec.Retry.Delays {
		names[spec.RetryQueueName(index)] = struct{}{}
	}
	return names
}

// warmAssertQueueDeclared 断言队列已按持久化非自动删除声明并返回其参数
func warmAssertQueueDeclared(t *testing.T, ch *warmRecordingChannel, name string) amqp.Table {
	t.Helper()
	record, ok := ch.warmQueue(name)
	if !ok {
		t.Fatalf("队列 %s 未声明 got=%v", name, ch.warmQueueNames())
	}
	if !record.durable || record.autoDelete || record.exclusive {
		t.Fatalf("队列 %s 的持久化参数错误 durable=%v autoDelete=%v exclusive=%v", name, record.durable, record.autoDelete, record.exclusive)
	}
	return record.args
}

// warmAssertSpecTopology 断言单个消费规格的主队列、重试队列与死信拓扑齐备
func warmAssertSpecTopology(t *testing.T, ch *warmRecordingChannel, spec ConsumerSpec, wantTTLMillis []int64) {
	t.Helper()
	mainArgs := warmAssertQueueDeclared(t, ch, spec.Queue)
	if got := warmTab(mainArgs, "x-dead-letter-exchange"); got != DeadLetterExchange {
		t.Fatalf("主队列 %s 的死信交换机错误 got=%v want=%s", spec.Queue, got, DeadLetterExchange)
	}
	if got := warmTab(mainArgs, "x-dead-letter-routing-key"); got != spec.DeadLetterQueueName() {
		t.Fatalf("主队列 %s 的死信路由键错误 got=%v want=%s", spec.Queue, got, spec.DeadLetterQueueName())
	}

	for index, want := range wantTTLMillis {
		queue := spec.RetryQueueName(index)
		args := warmAssertQueueDeclared(t, ch, queue)
		if got := warmTab(args, "x-message-ttl"); got != want {
			t.Fatalf("重试队列 %s 的 TTL 错误 got=%v want=%d args=%s", queue, got, want, ch.warmQueueArgsText(queue))
		}
		if got := warmTab(args, "x-dead-letter-exchange"); got != spec.Event.Exchange {
			t.Fatalf("重试队列 %s 的回主交换机错误 got=%v want=%s", queue, got, spec.Event.Exchange)
		}
		if got := warmTab(args, "x-dead-letter-routing-key"); got != spec.Event.RoutingKey {
			t.Fatalf("重试队列 %s 的回主路由键错误 got=%v want=%s", queue, got, spec.Event.RoutingKey)
		}
	}

	deadArgs := warmAssertQueueDeclared(t, ch, spec.DeadLetterQueueName())
	if len(deadArgs) != 0 {
		t.Fatalf("死信队列 %s 不应携带声明参数 got=%s", spec.DeadLetterQueueName(), ch.warmQueueArgsText(spec.DeadLetterQueueName()))
	}
	if !ch.warmBindExists(spec.Event.Exchange, spec.Event.RoutingKey, spec.Queue) {
		t.Fatalf("主队列 %s 缺少到 %s 的 %s 绑定 got=%v", spec.Queue, spec.Event.Exchange, spec.Event.RoutingKey, ch.binds)
	}
	if !ch.warmBindExists(DeadLetterExchange, spec.DeadLetterQueueName(), spec.DeadLetterQueueName()) {
		t.Fatalf("死信队列 %s 缺少死信交换机绑定 got=%v", spec.DeadLetterQueueName(), ch.binds)
	}
}

// 测试目标：验证视频处理规格与卡片预热规格在同一个信道上各自声明完整拓扑
// 预期效果：两个主队列、各自的重试队列与死信队列参数正确且互不串用，交换机与死信绑定齐备
func TestFeedCardWarmTopologyDeclaresBothConsumerSpecs(t *testing.T) {
	video := VideoProcessSpec()
	warm := FeedCardWarmSpec()
	ch := &warmRecordingChannel{}
	if err := DeclareTopologyFor(ch, video, warm); err != nil {
		t.Fatalf("声明两类消费规格拓扑失败: %v", err)
	}

	// 测试目标：核对两类规格的重试队列名与延迟档位一一对应
	// 预期效果：重试队列名按延迟命名，卡片刻的档位为 1s 5s 30s
	wantVideoRetry := []string{"video.process.retry.1s", "video.process.retry.5s", "video.process.retry.30s"}
	if got := []string{video.RetryQueueName(0), video.RetryQueueName(1), video.RetryQueueName(2)}; !warmEqualStrings(got, wantVideoRetry) {
		t.Fatalf("视频处理重试队列名错误 got=%v want=%v", got, wantVideoRetry)
	}
	wantWarmRetry := []string{"feed.card.warm.retry.1s", "feed.card.warm.retry.5s", "feed.card.warm.retry.30s"}
	if got := []string{warm.RetryQueueName(0), warm.RetryQueueName(1), warm.RetryQueueName(2)}; !warmEqualStrings(got, wantWarmRetry) {
		t.Fatalf("卡片预热重试队列名错误 got=%v want=%v", got, wantWarmRetry)
	}
	if warm.Queue != FeedCardWarmQueue || warm.Event.RoutingKey != VideoPublishedRoutingKey {
		t.Fatalf("卡片预热规格契约错误 queue=%s routingKey=%s", warm.Queue, warm.Event.RoutingKey)
	}

	videoQueues := warmExpectedQueues(video)
	warmQueues := warmExpectedQueues(warm)
	warmQueues["feed.card.warm"] = struct{}{}

	if len(ch.exchanges) != 4 {
		t.Fatalf("交换机声明次数错误 got=%v", ch.exchanges)
	}
	if ch.warmExchangeCount(EventsExchange) != 2 || ch.warmExchangeCount(DeadLetterExchange) != 2 {
		t.Fatalf("每个规格应各声明一次事件交换机与死信交换机 got=%v", ch.exchanges)
	}
	for _, record := range ch.exchanges {
		if record.kind != "topic" || !record.durable {
			t.Fatalf("交换机 %s 的声明参数错误 kind=%s durable=%v", record.name, record.kind, record.durable)
		}
	}
	for _, name := range []string{EventsExchange, DeadLetterExchange} {
		if index := ch.warmExchangeIndex(name); index < 0 {
			t.Fatalf("交换机 %s 未声明 got=%v", name, ch.exchanges)
		} else if ch.exchanges[index].kind != "topic" {
			t.Fatalf("交换机 %s 的类型错误 got=%s", name, ch.exchanges[index].kind)
		}
	}

	// 测试目标：统计本次声明涉及的全部队列
	// 预期效果：恰好是两个主队列、两个死信队列与六个重试队列，没有多余或缺失
	declared := ch.warmQueueNames()
	if len(declared) != len(videoQueues)+len(warmQueues) {
		t.Fatalf("队列声明数量错误 got=%v want=%d", declared, len(videoQueues)+len(warmQueues))
	}
	for name := range videoQueues {
		if _, ok := declared[name]; !ok {
			t.Fatalf("视频处理队列 %s 未声明 got=%v", name, declared)
		}
	}
	for name := range warmQueues {
		if _, ok := declared[name]; !ok {
			t.Fatalf("卡片预热队列 %s 未声明 got=%v", name, declared)
		}
	}

	wantTTL := []int64{1000, 5000, 30000}
	warmAssertSpecTopology(t, ch, video, wantTTL)
	warmAssertSpecTopology(t, ch, warm, wantTTL)

	// 测试目标：验证绑定只覆盖两个主队列与两个死信队列
	// 预期效果：总绑定数为四，重试队列不被任何交换机绑定
	if len(ch.binds) != 4 {
		t.Fatalf("绑定数量错误 got=%v", ch.binds)
	}
	videoDead := video.DeadLetterQueueName()
	warmDead := warm.DeadLetterQueueName()

	// 测试目标：核对事件交换机上的绑定队列与路由键
	// 预期效果：卡片预热只绑 video.published，视频处理只绑 video.process
	eventBound := ch.warmBoundQueues(EventsExchange)
	if len(eventBound) != 2 {
		t.Fatalf("事件交换机绑定队列数错误 got=%v", eventBound)
	}
	for _, name := range []string{FeedCardWarmQueue, VideoProcessQueue} {
		if _, ok := eventBound[name]; !ok {
			t.Fatalf("队列 %s 未绑定到事件交换机 got=%v", name, eventBound)
		}
	}
	if !ch.warmBindExists(EventsExchange, VideoPublishedRoutingKey, FeedCardWarmQueue) {
		t.Fatalf("卡片预热队列的主交换机绑定错误 got=%v", ch.binds)
	}
	if !ch.warmBindExists(EventsExchange, VideoProcessRoutingKey, VideoProcessQueue) {
		t.Fatalf("视频处理队列的主交换机绑定错误 got=%v", ch.binds)
	}
	warmQueueOnEvents := 0
	for _, record := range ch.binds {
		if record.exchange != EventsExchange {
			continue
		}
		if record.queue == FeedCardWarmQueue {
			warmQueueOnEvents++
			if record.key != VideoPublishedRoutingKey {
				t.Fatalf("卡片预热队列绑定了错误路由键 got=%s want=%s", record.key, VideoPublishedRoutingKey)
			}
		}
	}
	if warmQueueOnEvents != 1 {
		t.Fatalf("卡片预热队列在事件交换机上的绑定数错误 got=%d", warmQueueOnEvents)
	}

	// 测试目标：核对死信交换机上的绑定队列与路由键
	// 预期效果：两个死信队列按各自队列名作为路由键绑定，不使用主队列路由键
	deadBound := ch.warmBoundQueues(DeadLetterExchange)
	if len(deadBound) != 2 {
		t.Fatalf("死信交换机绑定队列数错误 got=%v", deadBound)
	}
	for _, name := range []string{videoDead, warmDead} {
		if _, ok := deadBound[name]; !ok {
			t.Fatalf("死信队列 %s 未绑定到死信交换机 got=%v", name, deadBound)
		}
	}
	if !ch.warmBindExists(DeadLetterExchange, warmDead, warmDead) {
		t.Fatalf("卡片预热死信队列的死信绑定错误 got=%v", ch.binds)
	}
	if !ch.warmBindExists(DeadLetterExchange, videoDead, videoDead) {
		t.Fatalf("视频处理死信队列的死信绑定错误 got=%v", ch.binds)
	}
	if ch.warmBindExists(DeadLetterExchange, VideoPublishedRoutingKey, warmDead) {
		t.Fatalf("死信队列不应使用业务路由键绑定 got=%v", ch.binds)
	}

	// 测试目标：确认重试队列只被声明而不被绑定
	// 预期效果：主交换机与死信交换机的绑定目标都不包含重试队列
	for _, exchange := range []string{EventsExchange, DeadLetterExchange} {
		bound := ch.warmBoundQueues(exchange)
		for index := range warm.Retry.Delays {
			if _, ok := bound[warm.RetryQueueName(index)]; ok {
				t.Fatalf("重试队列 %s 不应绑定到 %s got=%v", warm.RetryQueueName(index), exchange, ch.binds)
			}
		}
		for index := range video.Retry.Delays {
			if _, ok := bound[video.RetryQueueName(index)]; ok {
				t.Fatalf("重试队列 %s 不应绑定到 %s got=%v", video.RetryQueueName(index), exchange, ch.binds)
			}
		}
	}
}

// warmEqualStrings 比较两个字符串切片是否逐项相等
func warmEqualStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// 测试目标：验证两类消费规格之间重名队列被提前拒绝，避免互相覆盖拓扑
// 预期效果：重复队列名返回 duplicate topology queue 错误且不发出任何声明
func TestFeedCardWarmTopologyRejectsDuplicateQueueNames(t *testing.T) {
	ch := &warmRecordingChannel{}
	err := DeclareTopologyFor(ch, FeedCardWarmSpec(), FeedCardWarmSpec())
	if err == nil {
		t.Fatal("重复队列名的两类规格应返回错误")
	}
	if !strings.Contains(err.Error(), "duplicate topology queue") {
		t.Fatalf("错误文本未指出重复队列 got=%v", err)
	}
	if !strings.Contains(err.Error(), FeedCardWarmQueue) {
		t.Fatalf("错误文本未包含重复的队列名 got=%v", err)
	}
	if len(ch.exchanges) != 0 || len(ch.queues) != 0 || len(ch.binds) != 0 {
		t.Fatalf("重复校验失败时不应发出声明 got=%v %v %v", ch.exchanges, ch.queues, ch.binds)
	}

	// 测试目标：验证同规格重复声明但不共存于同一次调用
	// 预期效果：单规格调用保持幂等语义，可以正常返回
	single := &warmRecordingChannel{}
	if err := DeclareTopologyFor(single, FeedCardWarmSpec()); err != nil {
		t.Fatalf("单规格声明应成功: %v", err)
	}
	if len(single.queues) != 5 {
		t.Fatalf("卡片预热规格应声明五个队列 got=%v", single.warmQueueNames())
	}
}

// 测试目标：验证 runtime 配置两类消费规格后首次连接与重连都恢复完整拓扑
// 预期效果：每条连接各调用一次 DeclareConsumerTopology，且每次收到的规格数量恒为二
func TestFeedCardWarmTopologyRuntimeRestoresConsumerSpecs(t *testing.T) {
	dialer := &warmRecordingDialer{}
	runtime := NewRuntime(config.RabbitMQConfig{Host: "warm.test"}, WithDialer(dialer.dial), WithConsumerSpecs(VideoProcessSpec(), FeedCardWarmSpec()))
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.EnsureConnected(); err != nil {
		t.Fatalf("首次建连失败: %v", err)
	}
	if err := runtime.EnsureConnected(); err != nil {
		t.Fatalf("复用健康连接失败: %v", err)
	}
	if dialer.warmCalls() != 1 {
		t.Fatalf("健康连接应被复用 got=%d", dialer.warmCalls())
	}
	first := dialer.warmConnection(0)
	if first.warmTopologyCalls() != 0 {
		t.Fatalf("配置消费规格后不应回退 DeclareTopology got=%d", first.warmTopologyCalls())
	}
	snapshot := first.warmSpecsSnapshot()
	if len(snapshot) != 1 {
		t.Fatalf("首次连接应声明一次消费拓扑 got=%d", len(snapshot))
	}
	if len(snapshot[0]) != 2 {
		t.Fatalf("首次连接应收到两个消费规格 got=%d", len(snapshot[0]))
	}
	if snapshot[0][0].Queue != VideoProcessQueue || snapshot[0][1].Queue != FeedCardWarmQueue {
		t.Fatalf("首次连接的消费规格顺序错误 got=%s %s", snapshot[0][0].Queue, snapshot[0][1].Queue)
	}

	// 测试目标：关闭底层连接模拟意外断线后再触发一次调用
	// 预期效果：runtime 重新建连并在新连接上再次声明完整的两类规格拓扑
	if err := first.Close(); err != nil {
		t.Fatalf("关闭底层连接失败: %v", err)
	}
	if !first.IsClosed() {
		t.Fatal("关闭后的连接应视为已关闭")
	}
	if err := runtime.EnsureConnected(); err != nil {
		t.Fatalf("断线重连失败: %v", err)
	}
	if dialer.warmCalls() != 2 {
		t.Fatalf("断线后应重新建连 got=%d", dialer.warmCalls())
	}
	second := dialer.warmConnection(1)
	reconnected := second.warmSpecsSnapshot()
	if len(reconnected) != 1 {
		t.Fatalf("重连连接应声明一次消费拓扑 got=%d", len(reconnected))
	}
	if len(reconnected[0]) != 2 {
		t.Fatalf("重连连接应收到两个消费规格 got=%d", len(reconnected[0]))
	}
	if reconnected[0][1].Queue != FeedCardWarmQueue {
		t.Fatalf("重连后卡片预热规格缺失 got=%s", reconnected[0][1].Queue)
	}

	// 测试目标：核对重连声明的重试队列契约来自配置而非默认值
	// 预期效果：卡片预热规格携带三档延迟与四个预取
	if reconnected[0][1].Prefetch != 4 || len(reconnected[0][1].Retry.Delays) != 3 {
		t.Fatalf("重连后卡片预热契约错误 prefetch=%d delays=%v", reconnected[0][1].Prefetch, reconnected[0][1].Retry.Delays)
	}
}

// 测试目标：验证 WithConsumerSpecs 深拷贝调用方的重试延迟切片
// 预期效果：调用方在配置之后改动原始切片不会改变 runtime 实际声明的拓扑
func TestFeedCardWarmTopologyConsumerSpecsDeepCopy(t *testing.T) {
	callerSpecs := []ConsumerSpec{VideoProcessSpec(), FeedCardWarmSpec()}
	callerDelays := callerSpecs[1].Retry.Delays
	if !warmEqualInt64(warmDelaysMillis(callerDelays), []int64{1000, 5000, 30000}) {
		t.Fatalf("前置条件错误 调用方延迟档位 got=%v", callerDelays)
	}

	dialer := &warmRecordingDialer{}
	runtime := NewRuntime(config.RabbitMQConfig{Host: "warm.test"}, WithDialer(dialer.dial), WithConsumerSpecs(callerSpecs...))
	t.Cleanup(func() { _ = runtime.Close() })

	// 测试目标：改写调用方自己的切片内容
	// 预期效果：runtime 内部副本保持原值，不受调用方后续改动影响
	for index := range callerDelays {
		callerDelays[index] = 99 * time.Hour
	}
	callerSpecs[1].Retry.Delays[0] = 7 * time.Second

	if err := runtime.EnsureConnected(); err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	snapshot := dialer.warmConnection(0).warmSpecsSnapshot()
	if len(snapshot) != 1 || len(snapshot[0]) != 2 {
		t.Fatalf("拓扑恢复调用参数错误 got=%v", snapshot)
	}
	gotDelays := snapshot[0][1].Retry.Delays
	if !warmEqualInt64(warmDelaysMillis(gotDelays), []int64{1000, 5000, 30000}) {
		t.Fatalf("runtime 内部副本被调用方改动回填 got=%v", gotDelays)
	}
	retryQueues := []string{snapshot[0][1].RetryQueueName(0), snapshot[0][1].RetryQueueName(1), snapshot[0][1].RetryQueueName(2)}
	wantRetryQueues := []string{"feed.card.warm.retry.1s", "feed.card.warm.retry.5s", "feed.card.warm.retry.30s"}
	if !warmEqualStrings(retryQueues, wantRetryQueues) {
		t.Fatalf("重试队列名随调用方改动漂移 got=%v want=%v", retryQueues, wantRetryQueues)
	}

	// 测试目标：核对声明出的拓扑参数同样来自内部副本
	// 预期效果：两份规格的重试队列 TTL 仍为原档位
	channel := &warmRecordingChannel{}
	if err := DeclareTopologyFor(channel, snapshot[0]...); err != nil {
		t.Fatalf("按 runtime 参数声明拓扑失败: %v", err)
	}
	wantTTL := []int64{1000, 5000, 30000}
	warmAssertSpecTopology(t, channel, snapshot[0][0], wantTTL)
	warmAssertSpecTopology(t, channel, snapshot[0][1], wantTTL)
}

// warmDelaysMillis 把延迟档位转换为毫秒切片
func warmDelaysMillis(delays []time.Duration) []int64 {
	millis := make([]int64, 0, len(delays))
	for _, delay := range delays {
		millis = append(millis, delay.Milliseconds())
	}
	return millis
}

// warmEqualInt64 比较两个整数切片是否逐项相等
func warmEqualInt64(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// 测试目标：验证 mandatory 发布对已绑定路由键放行而对无绑定路由键报错
// 预期效果：真实 broker 上绑定路由键发布成功且不误报，未绑定路由键发布返回 unroutable 而不是静默成功
func TestMandatoryRouteRejectsUnroutableOnRealBroker(t *testing.T) {
	integrationRabbitMQConfig(t)

	admin := integrationAdminConnection(t)
	adminChannel, err := admin.Channel()
	if err != nil {
		t.Fatalf("创建管理信道失败: %v", err)
	}
	t.Cleanup(func() { _ = adminChannel.Close() })

	suffix := time.Now().UnixNano()
	boundQueue := fmt.Sprintf("gofeed.test.mandatory.bound.%d", suffix)
	unroutedKey := fmt.Sprintf("gofeed.test.unrouted.%d", suffix)
	exchange := fmt.Sprintf("gofeed.test.mandatory.events.%d", suffix)
	t.Cleanup(func() {
		cleanup, err := admin.Channel()
		if err != nil {
			t.Errorf("创建交换机回收信道失败: %v", err)
			return
		}
		defer cleanup.Close()
		if err := cleanup.ExchangeDelete(exchange, false, false); err != nil {
			t.Errorf("回收探针交换机失败: %v", err)
		}
	})
	if err := adminChannel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("声明探针交换机失败: %v", err)
	}
	if _, err := adminChannel.QueueDeclare(boundQueue, true, false, false, false, nil); err != nil {
		t.Fatalf("声明探针队列失败: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := admin.Channel()
		if cleanupErr != nil {
			t.Errorf("创建探针队列删除信道失败: %v", cleanupErr)
			return
		}
		defer cleanup.Close()
		if _, cleanupErr = cleanup.QueueDelete(boundQueue, false, false, false); cleanupErr != nil {
			t.Errorf("删除探针队列失败: %v", cleanupErr)
		}
	})
	if err := adminChannel.QueueBind(boundQueue, boundQueue, exchange, false, nil); err != nil {
		t.Fatalf("绑定探针队列失败: %v", err)
	}

	// 测试目标：在独立随机队列与随机路由键上做正反两例
	// 预期效果：不污染 feed.card.warm、video.process 等共享拓扑
	if !strings.Contains(boundQueue, "gofeed.test.mandatory.bound.") || boundQueue == FeedCardWarmQueue || boundQueue == VideoProcessQueue {
		t.Fatalf("探针队列命名未隔离 got=%s", boundQueue)
	}

	channel, err := admin.Channel()
	if err != nil {
		t.Fatalf("创建发布信道失败: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	if err := channel.Confirm(false); err != nil {
		t.Fatalf("开启发布确认失败: %v", err)
	}
	publisherSeam := &amqpChannelPublisher{ch: channel}
	publisherSeam.EnableRoutingChecks()
	publisher := newPublisherWithSeam(publisherSeam)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 测试目标：发布到探针队列自己绑定的随机路由键
	// 预期效果：broker 确认成功且没有 Return 通知，发布返回 nil
	if err := publisher.Publish(ctx, exchange, boundQueue, map[string]any{"probe": "mandatory-bound"}); err != nil {
		t.Fatalf("已绑定路由键发布失败: %v", err)
	}

	// 测试目标：发布到本用例交换机上没有绑定的随机路由键
	// 预期效果：即使 broker 已确认也判为失败，错误文本包含 unroutable 与路由键
	routingErr := publisher.Publish(ctx, exchange, unroutedKey, map[string]any{"probe": "mandatory-unrouted"})
	if routingErr == nil {
		t.Fatalf("未绑定路由键 %s 的发布应失败而不是静默成功", unroutedKey)
	}
	if !strings.Contains(routingErr.Error(), "unroutable") {
		t.Fatalf("不可路由错误文本缺少 unroutable got=%v", routingErr)
	}
	if !strings.Contains(routingErr.Error(), unroutedKey) {
		t.Fatalf("不可路由错误文本未包含路由键 got=%v want=%s", routingErr, unroutedKey)
	}

	// 测试目标：确认探针队列只收到成功路由的消息
	// 预期效果：队列深度为一，未绑定路由键的消息没有落入探针队列
	inspect, inspectErr := admin.Channel()
	if inspectErr != nil {
		t.Fatalf("创建队列检查信道失败: %v", inspectErr)
	}
	defer inspect.Close()
	info, inspectErr := inspect.QueueInspect(boundQueue)
	if inspectErr != nil {
		t.Fatalf("检查探针队列失败: %v", inspectErr)
	}
	if info.Messages != 1 {
		t.Fatalf("探针队列只应收到已绑定路由键的那条消息 got=%d", info.Messages)
	}
}
