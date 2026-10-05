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
