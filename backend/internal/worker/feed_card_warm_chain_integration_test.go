package worker

// 本文件用真实 MySQL、RabbitMQ 与 Redis 串起 video.published 事件的卡片预热链路
// 每个用例使用随机队列与事件交换机，只回收自己创建的拓扑与精确缓存键

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	redisdriver "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/config"
	infracachefeed "gofeed/internal/infra/cache/feed"
	infrafeed "gofeed/internal/infra/persistence/feed"
	"gofeed/internal/mq"
	infraredis "gofeed/internal/redis"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
)

const (
	chainWarmAuthorID   = uint(42)
	chainWarmVideoTitle = "链路卡片预热视频"

	chainWarmScriptSet    = "set"
	chainWarmScriptDelete = "delete"
	chainWarmScriptRead   = "read"
)

// 测试目标：给出生产布局的卡片缓存键
// 预期效果：用例据此刻画键布局漂移
func chainWarmCardKey(videoID uint) string {
	return "gofeed:feed:card:v1:" + strconv.FormatUint(uint64(videoID), 10)
}

// 测试目标：列出卡片预热消费规格的全部队列
// 预期效果：清理范围只覆盖本特性自有队列，不触碰共享交换机
func chainWarmQueueFamily(spec mq.ConsumerSpec) []string {
	queues := make([]string, 0, len(spec.Retry.Delays)+2)
	queues = append(queues, spec.Queue, spec.DeadLetterQueueName())
	queues = append(queues, retryQueueNames(spec)...)
	return queues
}

// 测试目标：判断事件标识是否为非空 UUID
// 预期效果：证据中能直接标注标识合法性
func chainWarmValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil
}

// 测试目标：格式化可能为空的时间指针
// 预期效果：证据输出不出现空指针解引用
func chainWarmFormatTime(value *time.Time) string {
	if value == nil {
		return "<nil>"
	}
	return value.Format(time.RFC3339Nano)
}

// 测试目标：从日志文本中取出包含关键字的一行
// 预期效果：消费者结果行可作为脱敏证据输出
func chainWarmLogLine(text string, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// 测试目标：统计真实键在记录中的出现次数
// 预期效果：支持重复写入与从未写入两类断言
func chainWarmCount(keys []string, key string) int {
	count := 0
	for _, item := range keys {
		if item == key {
			count++
		}
	}
	return count
}

// 测试目标：对真实键记录去重并保序
// 预期效果：确认重复投递没有产生额外键
func chainWarmUnique(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	unique := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	return unique
}

// 测试目标：把 backend/.env 中的真实依赖变量注入进程环境
// 预期效果：未显式设置时补齐 Redis 变量，已设置时不覆盖
func chainWarmLoadEnv(t *testing.T) {
	t.Helper()
	values, err := godotenv.Read("../../.env")
	if err != nil {
		return
	}
	for _, key := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_DB", "REDIS_PASSWORD"} {
		if os.Getenv(key) != "" {
			continue
		}
		if value, ok := values[key]; ok && value != "" {
			t.Setenv(key, value)
		}
	}
}

// 测试目标：读取真实 Redis 连接配置
// 预期效果：沿用 worker 集成用例的环境变量约定，未配置时明确跳过
func chainWarmRedisConfig(t *testing.T) config.RedisConfig {
	t.Helper()
	chainWarmLoadEnv(t)
	cfg := config.Config{}
	config.OverrideWithEnv(&cfg)
	if cfg.Redis.Host == "" || cfg.Redis.Port == 0 {
		t.Skip("需要真实 Redis：未配置 REDIS_HOST 与 REDIS_PORT")
	}
	return cfg.Redis
}

// 测试目标：建立真实 Redis 连接
// 预期效果：连接失败时跳过并说明原因
func chainWarmNewRedisClient(t *testing.T, cfg config.RedisConfig) infraredis.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := infraredis.New(ctx, cfg)
	if err != nil {
		t.Skipf("需要真实 Redis：连接 %s:%d 失败 %v", cfg.Host, cfg.Port, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// chainWarmScriptCache 为真实缓存叠加随机命名空间并记录本用例访问过的真实键
type chainWarmScriptCache struct {
	client  infraredis.Client
	prefix  string
	mu      sync.Mutex
	sets    []string
	deletes []string
	reads   []string
}

// 测试目标：把缓存脚本调用收敛到本用例命名空间
// 预期效果：脚本语义不变，真实键带随机前缀且被完整记录
func (c *chainWarmScriptCache) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	scoped := make([]string, 0, len(keys))
	for _, key := range keys {
		scoped = append(scoped, c.prefix+key)
	}
	c.record(script, scoped)
	return c.client.Eval(ctx, script, scoped, args...)
}

// 测试目标：按脚本语义归类记录真实键
// 预期效果：区分写入、删除与读取，便于断言精确键被删除
func (c *chainWarmScriptCache) record(script string, keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case strings.Contains(script, "redis.call('DEL'"):
		c.deletes = append(c.deletes, keys...)
	case strings.Contains(script, "redis.call('SET'"):
		c.sets = append(c.sets, keys...)
	default:
		c.reads = append(c.reads, keys...)
	}
}

// 测试目标：导出指定类别真实键的副本
// 预期效果：并发脚本调用下调用方仍取得一致快照
func (c *chainWarmScriptCache) keysOf(kind string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch kind {
	case chainWarmScriptSet:
		return append([]string(nil), c.sets...)
	case chainWarmScriptDelete:
		return append([]string(nil), c.deletes...)
	default:
		return append([]string(nil), c.reads...)
	}
}

// 测试目标：建立真实 RabbitMQ 运行时并声明卡片预热队列族
// 预期效果：拓扑参数冲突时立即失败，连接不可用时跳过
func chainWarmRuntime(t *testing.T, spec mq.ConsumerSpec) *mq.Runtime {
	t.Helper()
	cfg := integrationRabbitMQConfig(t)
	runtime := mq.NewRuntime(cfg, mq.WithConsumerSpecs(spec), mq.WithMandatoryPublishing(true))
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.EnsureConnected(); err != nil {
		text := err.Error()
		if strings.Contains(text, "PRECONDITION_FAILED") || strings.Contains(text, "406") {
			t.Fatalf("feed.card.warm 拓扑声明冲突: %v", err)
		}
		t.Skipf("需要真实 RabbitMQ：建连或声明卡片预热拓扑失败 %v", err)
	}
	return runtime
}

// 测试目标：回收当前用例创建的随机拓扑，包括初始化失败时的部分声明
// 预期效果：只删除拥有的队列与事件交换机，不清空固定业务队列或删除共享死信交换机
func chainWarmCleanupTopology(t *testing.T, conn *amqp.Connection, spec mq.ConsumerSpec) {
	t.Helper()
	for _, queue := range chainWarmQueueFamily(spec) {
		channel, err := conn.Channel()
		if err != nil {
			t.Errorf("打开拓扑回收信道失败: %v", err)
			return
		}
		_, err = channel.QueueDelete(queue, false, false, false)
		_ = channel.Close()
		var brokerErr *amqp.Error
		if err != nil && !(errors.As(err, &brokerErr) && brokerErr.Code == 404) {
			t.Errorf("删除用例队列 %s 失败: %v", queue, err)
		}
	}
	channel, err := conn.Channel()
	if err != nil {
		t.Errorf("打开交换机回收信道失败: %v", err)
		return
	}
	defer channel.Close()
	err = channel.ExchangeDelete(spec.Event.Exchange, false, false)
	var brokerErr *amqp.Error
	if err != nil && !(errors.As(err, &brokerErr) && brokerErr.Code == 404) {
		t.Errorf("删除用例交换机失败: %v", err)
	}
}

// chainWarmEnv 汇总一条真实链式用例所需的全部依赖
type chainWarmEnv struct {
	t        *testing.T
	db       *gorm.DB
	repo     *video.Repository
	conn     *amqp.Connection
	runtime  *mq.Runtime
	spec     mq.ConsumerSpec
	client   infraredis.Client
	script   *chainWarmScriptCache
	cache    *infracachefeed.CardCache
	warmer   *applicationfeed.CardWarmer
	consumer *CardWarmConsumer
	logs     *syncLogBuffer
	prefix   string
	eventIDs map[string]struct{}
}

// 测试目标：装配真实 MySQL、RabbitMQ 与 Redis 的卡片预热链路
// 预期效果：依赖缺失时跳过，可用时建立随机拓扑并登记精确键与自有拓扑回收
func chainWarmNewEnv(t *testing.T, options ...video.RepositoryOption) *chainWarmEnv {
	t.Helper()
	if len(options) == 0 {
		// 仓储默认不写发布事件，B 系列用例需要显式打开才能覆盖真实发布链路
		options = []video.RepositoryOption{video.WithPublishedEvents(true)}
	}
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	spec := mq.FeedCardWarmSpec()
	spec.Queue = "gofeed.test.cardwarm." + uuid.NewString()
	spec.Event.Exchange = spec.Queue + ".events"
	t.Cleanup(func() { chainWarmCleanupTopology(t, conn, spec) })
	runtime := chainWarmRuntime(t, spec)
	redisClient := chainWarmNewRedisClient(t, chainWarmRedisConfig(t))
	prefix := "gofeed:test:" + uuid.NewString() + ":"
	script := &chainWarmScriptCache{client: redisClient, prefix: prefix}
	cache, err := infracachefeed.NewCardCache(script, infracachefeed.CardCacheOptions{
		TTL:              time.Minute,
		OperationTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("构建卡片缓存失败: %v", err)
	}
	repo := video.NewRepository(db, options...)
	warmer, err := applicationfeed.NewCardWarmer(infrafeed.NewCardReader(repo), cache)
	if err != nil {
		t.Fatalf("构建卡片预热器失败: %v", err)
	}
	consumer, err := NewCardWarmConsumer(warmer, runtime)
	if err != nil {
		t.Fatalf("构建卡片预热消费者失败: %v", err)
	}
	consumer.spec = spec
	env := &chainWarmEnv{
		t:        t,
		db:       db,
		repo:     repo,
		conn:     conn,
		runtime:  runtime,
		spec:     spec,
		client:   redisClient,
		script:   script,
		cache:    cache,
		warmer:   warmer,
		consumer: consumer,
		logs:     captureWorkerLogs(t),
		prefix:   prefix,
		eventIDs: make(map[string]struct{}),
	}
	env.logf("依赖就绪 database=%s prefix=%s queues=%v", env.databaseName(), prefix, chainWarmQueueFamily(spec))
	t.Cleanup(env.cleanup)
	return env
}

// 测试目标：输出脱敏的链路证据
// 预期效果：只记录标识、键名与结果，不打印口令与连接串
func (e *chainWarmEnv) logf(format string, args ...any) {
	e.t.Helper()
	e.t.Logf("[chain-warm] "+format, args...)
}

// 测试目标：读取当前临时库名
// 预期效果：清理证据能指明临时库由 testutil 自动创建与回收
func (e *chainWarmEnv) databaseName() string {
	var name string
	row := e.db.Raw("SELECT DATABASE()").Row()
	if err := row.Scan(&name); err != nil {
		return "unknown"
	}
	return name
}

// 测试目标：登记本用例自己的事件标识
// 预期效果：外来载荷可被立即识别
func (e *chainWarmEnv) trackEvent(eventID string) {
	e.eventIDs[eventID] = struct{}{}
}

// 测试目标：拼出本用例写入的真实卡片键
// 预期效果：键名与生产布局一致，只多随机命名空间前缀
func (e *chainWarmEnv) exactKey(videoID uint) string {
	return e.prefix + chainWarmCardKey(videoID)
}

// 测试目标：读取真实缓存原文
// 预期效果：命中返回内容，未命中返回 redis.Nil
func (e *chainWarmEnv) rawValue(key string) (string, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.client.Get(ctx, key)
}

// 测试目标：读取单个队列的深度
// 预期效果：用于断言积压与消费完成状态
func (e *chainWarmEnv) queueDepth(queue string) int {
	e.t.Helper()
	depth, err := e.runtime.QueueDepth(queue)
	if err != nil {
		e.t.Fatalf("读取队列 %s 深度失败: %v", queue, err)
	}
	return depth
}

// 测试目标：确认队列族没有遗留消息
// 预期效果：不存在永久积压或未确认投递
func (e *chainWarmEnv) assertQueuesEmpty() {
	e.t.Helper()
	for _, queue := range chainWarmQueueFamily(e.spec) {
		if depth := e.queueDepth(queue); depth != 0 {
			e.t.Fatalf("队列 %s 残留 %d 条消息", queue, depth)
		}
	}
}

// 测试目标：回收本用例在真实依赖上的写入与积压
// 预期效果：精确删除记名键并回读确认为空，记录自有队列深度，随后回收拓扑
func (e *chainWarmEnv) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	keys := e.script.keysOf(chainWarmScriptSet)
	cleared := 0
	for _, key := range keys {
		if _, err := e.client.Del(ctx, key); err != nil {
			e.t.Errorf("清理缓存键 %s 失败: %v", key, err)
		}
	}
	for _, key := range keys {
		if _, err := e.client.Get(ctx, key); errors.Is(err, redisdriver.Nil) {
			cleared++
			continue
		}
		e.t.Errorf("缓存键 %s 未清理干净", key)
	}
	depths := make([]string, 0, len(chainWarmQueueFamily(e.spec)))
	for _, queue := range chainWarmQueueFamily(e.spec) {
		depth, depthErr := e.runtime.QueueDepth(queue)
		if depthErr != nil {
			depths = append(depths, fmt.Sprintf("%s=err(%v)", queue, depthErr))
			continue
		}
		depths = append(depths, fmt.Sprintf("%s=%d", queue, depth))
	}
	e.logf("清理完成 database=%s 精确键%d/%d 回读为空 队列深度=%v", e.databaseName(), cleared, len(keys), depths)
}

// 测试目标：写入待处理视频与已派发的处理事件
// 预期效果：视频可被真实完成接口转为 published，处理事件提供原始事件标识
func (e *chainWarmEnv) seedProcessing() (video.Video, video.OutboxEvent) {
	e.t.Helper()
	ctx := context.Background()
	publishedAt := testTime()
	entity := video.Video{
		AuthorID:          chainWarmAuthorID,
		Title:             chainWarmVideoTitle,
		Description:       "链路卡片预热说明",
		Status:            video.VideoStatusProcessing,
		PlayURL:           "/static/videos/42/20260801/chain.mp4",
		PlayFileName:      "chain.mp4",
		PlayOriginalName:  "链路视频.mp4",
		CoverURL:          "/static/covers/42/20260801/chain.png",
		CoverFileName:     "chain.png",
		CoverOriginalName: "链路封面.png",
		PublishedAt:       &publishedAt,
	}
	if err := e.repo.Create(ctx, &entity); err != nil {
		e.t.Fatalf("写入待处理视频失败: %v", err)
	}
	dispatchedAt := testTime()
	processEvent := video.OutboxEvent{
		EventID:      uuid.NewString(),
		VideoID:      entity.ID,
		EventType:    video.VideoProcessEventType,
		Status:       video.OutboxEventStatusDispatched,
		DispatchedAt: &dispatchedAt,
	}
	if err := e.db.Create(&processEvent).Error; err != nil {
		e.t.Fatalf("写入处理事件失败: %v", err)
	}
	e.trackEvent(processEvent.EventID)
	e.logf("seed video_id=%d process_event_id=%s process_event_type=%s process_uuid_valid=%t",
		entity.ID, processEvent.EventID, processEvent.EventType, chainWarmValidUUID(processEvent.EventID))
	return entity, processEvent
}

// 测试目标：通过真实仓储完成视频处理
// 预期效果：视频转为 published 并读回持久化的 video.published 事件
func (e *chainWarmEnv) completeProcessing(videoID uint) video.OutboxEvent {
	e.t.Helper()
	changed, err := e.repo.CompleteVideoProcessing(context.Background(), videoID)
	if err != nil {
		e.t.Fatalf("完成视频处理失败: %v", err)
	}
	if !changed {
		e.t.Fatalf("视频 %d 未被完成处理，期望 processing 转为 published", videoID)
	}
	event := e.publishedEvent(videoID)
	e.trackEvent(event.EventID)
	return event
}

// 测试目标：读取视频最新持久化的发布事件
// 预期效果：事件标识与类型来自数据库而不是构造值
func (e *chainWarmEnv) publishedEvent(videoID uint) video.OutboxEvent {
	e.t.Helper()
	var event video.OutboxEvent
	err := e.db.Where("video_id = ? AND event_type = ?", videoID, video.VideoPublishedEventType).
		Order("id DESC").First(&event).Error
	if err != nil {
		e.t.Fatalf("读取发布事件失败: %v", err)
	}
	return event
}

// 测试目标：统计视频的发布事件数量
// 预期效果：可验证关闭开关后完成处理不新增事件
func (e *chainWarmEnv) publishedEventCount(videoID uint) int64 {
	e.t.Helper()
	var count int64
	err := e.db.Model(&video.OutboxEvent{}).
		Where("video_id = ? AND event_type = ?", videoID, video.VideoPublishedEventType).
		Count(&count).Error
	if err != nil {
		e.t.Fatalf("统计发布事件失败: %v", err)
	}
	return count
}

// 测试目标：读取视频当前持久化状态
// 预期效果：证据包含状态、发布时间与软删除时间
func (e *chainWarmEnv) currentVideo(videoID uint) video.Video {
	e.t.Helper()
	var entity video.Video
	if err := e.db.Unscoped().First(&entity, videoID).Error; err != nil {
		e.t.Fatalf("读取视频失败: %v", err)
	}
	return entity
}

// 测试目标：用真实 relay 派发 video.published 事件
// 预期效果：事件进入 dispatched 且 outbox 没有残留 pending 或 publishing
func (e *chainWarmEnv) dispatchPublished() {
	e.t.Helper()
	route := VideoPublishedRoute()
	route.Event = e.spec.Event
	relay, err := NewRelayWithRoutes(e.repo, e.runtime, route)
	if err != nil {
		e.t.Fatalf("构造发布派发器失败: %v", err)
	}
	if err := relay.dispatchRound(context.Background()); err != nil {
		e.t.Fatalf("派发轮次失败: %v", err)
	}
}

// 测试目标：确认事件按持久化行标记派发
// 预期效果：状态为 dispatched 且派发时间落库
func (e *chainWarmEnv) assertEventDispatched(eventID uint) video.OutboxEvent {
	e.t.Helper()
	var event video.OutboxEvent
	if err := e.db.First(&event, eventID).Error; err != nil {
		e.t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil {
		e.t.Fatalf("事件未标记派发 got=%+v", event)
	}
	return event
}

// 测试目标：确认 outbox 没有残留未完成事件
// 预期效果：待派发与派发中计数都为 0
func (e *chainWarmEnv) assertOutboxSettled() {
	e.t.Helper()
	snapshot, err := e.repo.GetOutboxSnapshot(context.Background())
	if err != nil {
		e.t.Fatalf("读取 outbox 快照失败: %v", err)
	}
	if snapshot.PendingCount != 0 || snapshot.PublishingCount != 0 {
		e.t.Fatalf("outbox 未收敛 pending=%d publishing=%d", snapshot.PendingCount, snapshot.PublishingCount)
	}
	e.logf("outbox 收敛 pending=%d publishing=%d", snapshot.PendingCount, snapshot.PublishingCount)
}

// 测试目标：从真实 feed.card.warm 队列读取一条本用例投递
// 预期效果：载荷标识属于本用例，出现外来载荷立即失败并提示上报
func (e *chainWarmEnv) consumePublished() (amqp.Delivery, PublishedMessage) {
	e.t.Helper()
	delivery := consumeDelivery(e.t, e.conn, e.spec.Queue)
	msg, err := decodePublishedMessage(delivery.Body)
	if err != nil {
		e.t.Fatalf("解码 feed.card.warm 载荷失败: %v body=%s", err, string(delivery.Body))
	}
	if _, ok := e.eventIDs[msg.EventID]; !ok {
		e.t.Fatalf("检测到非本用例载荷 event_id=%s video_id=%d，可能存在并行消费者，停止断言并上报", msg.EventID, msg.VideoID)
	}
	e.logf("broker_payload=%s event_id=%s video_id=%d", string(delivery.Body), msg.EventID, msg.VideoID)
	return delivery, msg
}

// 测试目标：把真实投递交给卡片预热消费者处理但不确认
// 预期效果：返回消费者判定，便于用例自行模拟确认丢失
func (e *chainWarmEnv) handleOnly(delivery amqp.Delivery) mq.HandlerResult {
	e.t.Helper()
	return e.consumer.handleDelivery(context.Background(), delivery)
}

// 测试目标：处理并确认一条真实投递
// 预期效果：仅 ResultAck 确认，其他结果拒绝且不重回队列
func (e *chainWarmEnv) deliver(delivery amqp.Delivery) mq.HandlerResult {
	e.t.Helper()
	result := e.handleOnly(delivery)
	switch result {
	case mq.ResultAck:
		if err := delivery.Ack(false); err != nil {
			e.t.Fatalf("确认投递失败: %v", err)
		}
	default:
		if err := delivery.Nack(false, false); err != nil {
			e.t.Fatalf("拒绝投递失败: %v", err)
		}
	}
	return result
}

// 测试目标：在捕获日志中确认消费者结果行
// 预期效果：返回命中的完整日志行作为脱敏证据
func (e *chainWarmEnv) assertLogLine(videoID uint, want applicationfeed.CardWarmupResult) string {
	e.t.Helper()
	needle := fmt.Sprintf("video_id=%d result=%s", videoID, want)
	line := chainWarmLogLine(e.logs.String(), needle)
	if line == "" {
		e.t.Fatalf("日志未出现 %q logs=%s", needle, e.logs.String())
	}
	e.logf("consumer_log=%s", line)
	return line
}

type chainWarmSettledSource struct {
	*mq.Runtime
	ctx     context.Context
	settled chan struct{}
}

// 测试目标：观察真实消费信道的确认结果
// 预期效果：原消费信道仍负责注册与关闭，仅补充成功确认通知
func (s *chainWarmSettledSource) ConsumerChannel(prefetch int) (mq.ConsumerChannel, error) {
	channel, err := s.Runtime.ConsumerChannel(prefetch)
	if err != nil {
		return nil, err
	}
	return &chainWarmSettledChannel{ConsumerChannel: channel, source: s}, nil
}

type chainWarmSettledChannel struct {
	mq.ConsumerChannel
	source *chainWarmSettledSource
}

// 测试目标：保留真实投递并观察确认调用
// 预期效果：转发协程随请求取消退出，不提前关闭实际确认信道
func (c *chainWarmSettledChannel) Consume(queue string) (<-chan amqp.Delivery, error) {
	deliveries, err := c.ConsumerChannel.Consume(queue)
	if err != nil {
		return nil, err
	}
	forwarded := make(chan amqp.Delivery)
	go func() {
		defer close(forwarded)
		for {
			select {
			case <-c.source.ctx.Done():
				return
			case delivery, ok := <-deliveries:
				if !ok {
					return
				}
				delivery.Acknowledger = chainWarmSettledAck{delivery.Acknowledger, c.source.settled}
				select {
				case forwarded <- delivery:
				case <-c.source.ctx.Done():
					return
				}
			}
		}
	}()
	return forwarded, nil
}

type chainWarmSettledAck struct {
	amqp.Acknowledger
	settled chan struct{}
}

// 测试目标：在真实确认成功之后通知用例
// 预期效果：取消消费循环不会抢在日志之后、确认之前导致重新入队
func (a chainWarmSettledAck) Ack(tag uint64, multiple bool) error {
	err := a.Acknowledger.Ack(tag, multiple)
	if err == nil {
		select {
		case a.settled <- struct{}{}:
		default:
		}
	}
	return err
}

// 测试目标：用真实消费循环完成目标投递
// 预期效果：真实确认成功后再停止循环，超时输出队列深度与日志诊断
func (e *chainWarmEnv) runConsumerUntil(videoID uint, want applicationfeed.CardWarmupResult) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &chainWarmSettledSource{Runtime: e.runtime, ctx: ctx, settled: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.consumer.Run(ctx, source)
	}()
	needle := fmt.Sprintf("video_id=%d result=%s", videoID, want)
	select {
	case <-source.settled:
	case <-time.After(15 * time.Second):
		cancel()
		<-done
		e.t.Fatalf("消费循环未在超时内确认 %q 深度=%d logs=%s",
			needle, e.queueDepth(e.spec.Queue), e.logs.String())
	}
	e.assertLogLine(videoID, want)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		e.t.Fatalf("消费循环未在取消后退出")
	}
}

// 测试目标：按 GORM 软删除语义让视频不可见
// 预期效果：deleted_at 落库，公开读取与卡片读取都不再返回该视频
func (e *chainWarmEnv) softDeleteVideo(videoID uint) {
	e.t.Helper()
	if err := e.db.Where("id = ?", videoID).Delete(&video.Video{}).Error; err != nil {
		e.t.Fatalf("软删除视频失败: %v", err)
	}
	entity := e.currentVideo(videoID)
	if !entity.DeletedAt.Valid {
		e.t.Fatalf("视频 %d 软删除未生效", videoID)
	}
	e.logf("video_id=%d soft_deleted_at=%s", videoID, entity.DeletedAt.Time.Format(time.RFC3339Nano))
}

// 测试目标：直接调用真实卡片预热器
// 预期效果：返回生产结果枚举并记录本次键访问
func (e *chainWarmEnv) warm(videoID uint) applicationfeed.CardWarmupResult {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cardWarmupTimeout)
	defer cancel()
	result, err := e.warmer.WarmCard(ctx, videoID)
	if err != nil {
		e.t.Fatalf("卡片预热失败: %v", err)
	}
	e.logf("warm_direct video_id=%d result=%s", videoID, result)
	return result
}

// 测试目标：直接领取一条待派发事件以观察快照标记
// 预期效果：返回的派发项带有 HasVideo 与租约接管标记
func (e *chainWarmEnv) claimDirect() video.OutboxDispatch {
	e.t.Helper()
	dispatches, err := e.repo.ClaimPendingOutboxEvents(context.Background(), 32, outboxLease)
	if err != nil {
		e.t.Fatalf("领取 outbox 事件失败: %v", err)
	}
	if len(dispatches) != 1 {
		e.t.Fatalf("期望领取 1 条事件 got=%d", len(dispatches))
	}
	return dispatches[0]
}

// 测试目标：把领取到的事件交回 pending
// 预期效果：真实 relay 仍可按正常路径派发同一条事件
func (e *chainWarmEnv) releaseDirect(dispatch video.OutboxDispatch) {
	e.t.Helper()
	released, err := e.repo.ReleaseOutboxRetry(context.Background(), dispatch.Event.ID, dispatch.Event.Attempt, 0,
		errors.New("chain warm test hands back"))
	if err != nil {
		e.t.Fatalf("释放 outbox 事件失败: %v", err)
	}
	if !released {
		e.t.Fatalf("outbox 事件 %d 未被释放", dispatch.Event.ID)
	}
}

// 测试目标：把已派发事件回退为租约过期的 publishing 状态
// 预期效果：真实 relay 会按租约接管语义重新派发同一事件
func (e *chainWarmEnv) expireLease(event video.OutboxEvent) {
	e.t.Helper()
	err := e.db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
		"status":          video.OutboxEventStatusPublishing,
		"locked_until":    gorm.Expr("TIMESTAMPADD(SECOND, -1, NOW(3))"),
		"next_attempt_at": nil,
	}).Error
	if err != nil {
		e.t.Fatalf("回退事件租约失败: %v", err)
	}
	e.logf("lease_rewound event_id=%s event_type=%s", event.EventID, event.EventType)
}

// 测试目标：验证完成处理后的发布事件经真实 broker 与消费者预热出精确卡片键
// 预期效果：feed.card.warm 收到持久化事件标识的载荷、消费者记录 warmed 且 Redis 出现精确键
func TestCardWarmChainPublishesVideoPublishedToBroker(t *testing.T) {
	env := chainWarmNewEnv(t)
	entity, processEvent := env.seedProcessing()
	publishedEvent := env.completeProcessing(entity.ID)
	row := env.currentVideo(entity.ID)
	env.logf("state video_id=%d status=%s published_at=%s process_event_id=%s published_event_id=%s published_event_type=%s uuid_valid=%t/%t event_id_changed=%t",
		entity.ID, row.Status, chainWarmFormatTime(row.PublishedAt), processEvent.EventID, publishedEvent.EventID,
		publishedEvent.EventType, chainWarmValidUUID(processEvent.EventID), chainWarmValidUUID(publishedEvent.EventID),
		processEvent.EventID != publishedEvent.EventID)
	if row.Status != video.VideoStatusPublished || row.PublishedAt == nil {
		t.Fatalf("完成处理后视频应为已发布 got status=%s published_at=%s", row.Status, chainWarmFormatTime(row.PublishedAt))
	}
	if processEvent.EventID == publishedEvent.EventID {
		t.Fatalf("发布事件应使用新的持久化标识 process=%s published=%s", processEvent.EventID, publishedEvent.EventID)
	}

	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)
	env.assertOutboxSettled()

	env.runConsumerUntil(entity.ID, applicationfeed.CardWarmed)

	key := env.exactKey(entity.ID)
	value, err := env.rawValue(key)
	if err != nil {
		t.Fatalf("读取卡片键 %s 失败: %v", key, err)
	}
	env.logf("redis_key=%s value=%s", key, value)
	if chainWarmCount(env.script.keysOf(chainWarmScriptSet), key) != 1 {
		t.Fatalf("真实写入键应精确命中一次 key=%s sets=%v", key, env.script.keysOf(chainWarmScriptSet))
	}
	env.assertQueuesEmpty()
}

// 测试目标：验证卡片缓存键布局与内容契约
// 预期效果：真实键为 gofeed:feed:card:v1:<video_id>，原文经生产解码器还原为同一视频
func TestCardWarmChainCachesExactCardKeyLayout(t *testing.T) {
	env := chainWarmNewEnv(t)
	entity, processEvent := env.seedProcessing()
	publishedEvent := env.completeProcessing(entity.ID)
	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)

	delivery, msg := env.consumePublished()
	if msg.EventID != publishedEvent.EventID || msg.VideoID != entity.ID {
		t.Fatalf("载荷与持久化事件不一致 got=%+v persisted=%+v", msg, publishedEvent)
	}
	if msg.SchemaVersion != mq.VideoPublishedSchemaVersion {
		t.Fatalf("载荷版本错误 got=%d", msg.SchemaVersion)
	}
	if result := env.deliver(delivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认投递 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)

	key := env.prefix + chainWarmCardKey(entity.ID)
	value, err := env.rawValue(key)
	if err != nil {
		t.Fatalf("读取真实缓存键 %s 失败: %v", key, err)
	}
	env.logf("redis_key=%s value=%s", key, value)
	var payload struct {
		Version int `json:"version"`
		Card    struct {
			VideoID     uint      `json:"VideoID"`
			AuthorID    *uint     `json:"AuthorID"`
			Title       string    `json:"Title"`
			PublishedAt time.Time `json:"PublishedAt"`
		} `json:"card"`
	}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		t.Fatalf("解码缓存原文失败: %v value=%s", err, value)
	}
	if payload.Version != 1 || payload.Card.VideoID != entity.ID {
		t.Fatalf("缓存版本或视频标识错误 got=%+v", payload)
	}
	if payload.Card.AuthorID == nil || *payload.Card.AuthorID != chainWarmAuthorID {
		t.Fatalf("缓存作者标识错误 got=%+v", payload.Card.AuthorID)
	}
	if payload.Card.Title != chainWarmVideoTitle {
		t.Fatalf("缓存标题错误 got=%q", payload.Card.Title)
	}
	row := env.currentVideo(entity.ID)
	if row.PublishedAt == nil || !payload.Card.PublishedAt.Equal(*row.PublishedAt) {
		t.Fatalf("缓存发布时间错误 got=%s want=%s", payload.Card.PublishedAt, chainWarmFormatTime(row.PublishedAt))
	}

	cached, err := env.cache.GetCards(context.Background(), []uint{entity.ID})
	if err != nil {
		t.Fatalf("生产解码器读取缓存失败: %v", err)
	}
	card, ok := cached.Cards[entity.ID]
	if !ok || cached.InvalidCount != 0 {
		t.Fatalf("生产解码器未取到卡片 got=%+v invalid=%d", cached.Cards, cached.InvalidCount)
	}
	if err := applicationfeed.ValidateCachedCard(card); err != nil {
		t.Fatalf("卡片不满足公开契约: %v", err)
	}
	if card.VideoID != entity.ID || card.Title != chainWarmVideoTitle || card.AuthorID != chainWarmAuthorID {
		t.Fatalf("卡片内容错误 got=%+v", card)
	}
	env.logf("decoded_card video_id=%d title=%s author_id=%d published_at=%s process_event_id=%s",
		card.VideoID, card.Title, card.AuthorID, card.PublishedAt.Format(time.RFC3339Nano), processEvent.EventID)
	env.assertQueuesEmpty()
	env.assertOutboxSettled()
}

// 测试目标：验证重复投递沿用持久化事件标识
// 预期效果：首次投递、代理重投与租约接管重发的载荷 event_id 都等于 outbox 行取值
func TestCardWarmChainRedeliveryKeepsPersistedEventID(t *testing.T) {
	env := chainWarmNewEnv(t)
	entity, _ := env.seedProcessing()
	publishedEvent := env.completeProcessing(entity.ID)
	if !chainWarmValidUUID(publishedEvent.EventID) {
		t.Fatalf("持久化事件标识应为有效 UUID got=%q", publishedEvent.EventID)
	}
	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)

	firstDelivery, firstMsg := env.consumePublished()
	if firstMsg.EventID != publishedEvent.EventID {
		t.Fatalf("首次载荷事件标识与持久化行不一致 payload=%s persisted=%s", firstMsg.EventID, publishedEvent.EventID)
	}
	env.logf("redelivery=first event_id=%s redelivered=%t", firstMsg.EventID, firstDelivery.Redelivered)
	if result := env.handleOnly(firstDelivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认投递 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)
	key := env.exactKey(entity.ID)
	firstValue, err := env.rawValue(key)
	if err != nil {
		t.Fatalf("读取卡片键 %s 失败: %v", key, err)
	}

	if err := firstDelivery.Nack(false, true); err != nil {
		t.Fatalf("重投消息失败: %v", err)
	}
	secondDelivery, secondMsg := env.consumePublished()
	if !secondDelivery.Redelivered {
		t.Fatalf("代理重投的投递应带重投标记 got=%+v", secondDelivery)
	}
	if secondMsg.EventID != publishedEvent.EventID {
		t.Fatalf("代理重投事件标识被改写 payload=%s persisted=%s", secondMsg.EventID, publishedEvent.EventID)
	}
	if !bytes.Equal(firstDelivery.Body, secondDelivery.Body) {
		t.Fatalf("代理重投载荷应逐字节一致 first=%s second=%s", firstDelivery.Body, secondDelivery.Body)
	}
	env.logf("redelivery=broker event_id=%s redelivered=%t body_identical=true", secondMsg.EventID, secondDelivery.Redelivered)
	if result := env.deliver(secondDelivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认重投 got=%v", result)
	}

	env.expireLease(publishedEvent)
	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)
	thirdDelivery, thirdMsg := env.consumePublished()
	if thirdMsg.EventID != publishedEvent.EventID {
		t.Fatalf("租约接管重发事件标识被改写 payload=%s persisted=%s", thirdMsg.EventID, publishedEvent.EventID)
	}
	if !bytes.Equal(firstDelivery.Body, thirdDelivery.Body) {
		t.Fatalf("租约接管重发载荷应逐字节一致 first=%s third=%s", firstDelivery.Body, thirdDelivery.Body)
	}
	env.logf("redelivery=lease_takeover event_id=%s persisted_event_id=%s", thirdMsg.EventID, publishedEvent.EventID)
	if result := env.deliver(thirdDelivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认重发 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)

	valueAfter, err := env.rawValue(key)
	if err != nil {
		t.Fatalf("重复投递后读取卡片键 %s 失败: %v", key, err)
	}
	if valueAfter != firstValue {
		t.Fatalf("重复投递后卡片内容应保持不变 before=%s after=%s", firstValue, valueAfter)
	}
	if keys := chainWarmUnique(env.script.keysOf(chainWarmScriptSet)); len(keys) != 1 || keys[0] != key {
		t.Fatalf("重复投递只应命中同一个精确键 got=%v", keys)
	}
	env.assertQueuesEmpty()
	env.assertOutboxSettled()
}

// 测试目标：验证软删除视频的发布事件仍被派发且缓存卡片被删除
// 预期效果：outbox 收敛为 dispatched、消费者记录 skipped_not_public 且精确键被 DEL
func TestCardWarmChainDispatchesSoftDeletedVideo(t *testing.T) {
	env := chainWarmNewEnv(t)
	entity, _ := env.seedProcessing()
	publishedEvent := env.completeProcessing(entity.ID)
	if result := env.warm(entity.ID); result != applicationfeed.CardWarmed {
		t.Fatalf("预热应写入卡片 got=%s", result)
	}
	key := env.exactKey(entity.ID)
	if _, err := env.rawValue(key); err != nil {
		t.Fatalf("预热后应存在卡片键 %s: %v", key, err)
	}

	env.softDeleteVideo(entity.ID)

	dispatch := env.claimDirect()
	if dispatch.HasVideo {
		t.Fatalf("软删除视频不应带视频快照 got=%+v", dispatch)
	}
	if dispatch.Event.EventID != publishedEvent.EventID || dispatch.Event.EventType != video.VideoPublishedEventType {
		t.Fatalf("领取到的事件与持久化行不一致 got=%+v persisted=%+v", dispatch.Event, publishedEvent)
	}
	env.logf("claim_direct event_id=%s event_type=%s has_video=%t lease_taken_over=%t",
		dispatch.Event.EventID, dispatch.Event.EventType, dispatch.HasVideo, dispatch.LeaseTakenOver)
	env.releaseDirect(dispatch)

	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)
	env.assertOutboxSettled()

	delivery, msg := env.consumePublished()
	if msg.EventID != publishedEvent.EventID {
		t.Fatalf("软删除后事件标识被改写 payload=%s persisted=%s", msg.EventID, publishedEvent.EventID)
	}
	if result := env.deliver(delivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认投递 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardSkippedNotPublic)

	if chainWarmCount(env.script.keysOf(chainWarmScriptDelete), key) != 1 {
		t.Fatalf("应精确删除卡片键一次 key=%s deletes=%v", key, env.script.keysOf(chainWarmScriptDelete))
	}
	if _, err := env.rawValue(key); !errors.Is(err, redisdriver.Nil) {
		t.Fatalf("软删除后卡片键应被删除 err=%v", err)
	}
	env.logf("cache_deleted key=%s readback=redis.Nil", key)
	env.assertQueuesEmpty()
}

// 测试目标：验证不可见视频不会通过重复预热重新写回缓存
// 预期效果：再次预热与重放载荷都返回 skipped_not_public，精确键被 DEL 且回读为空
func TestCardWarmChainInvisibleVideoDoesNotReappear(t *testing.T) {
	env := chainWarmNewEnv(t)
	entity, _ := env.seedProcessing()
	publishedEvent := env.completeProcessing(entity.ID)
	env.dispatchPublished()
	env.assertEventDispatched(publishedEvent.ID)

	delivery, msg := env.consumePublished()
	if msg.EventID != publishedEvent.EventID {
		t.Fatalf("载荷事件标识与持久化行不一致 payload=%s persisted=%s", msg.EventID, publishedEvent.EventID)
	}
	payload := append([]byte(nil), delivery.Body...)
	if result := env.deliver(delivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认投递 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)

	key := env.exactKey(entity.ID)
	if _, err := env.rawValue(key); err != nil {
		t.Fatalf("预热后应存在卡片键 %s: %v", key, err)
	}
	setCount := chainWarmCount(env.script.keysOf(chainWarmScriptSet), key)

	env.softDeleteVideo(entity.ID)
	if result := env.warm(entity.ID); result != applicationfeed.CardSkippedNotPublic {
		t.Fatalf("不可见视频预热应跳过 got=%s", result)
	}
	if _, err := env.rawValue(key); !errors.Is(err, redisdriver.Nil) {
		t.Fatalf("不可见视频的卡片键应被删除 err=%v", err)
	}
	if chainWarmCount(env.script.keysOf(chainWarmScriptSet), key) != setCount {
		t.Fatalf("不可见视频不应新增缓存写入 before=%d after=%d", setCount, chainWarmCount(env.script.keysOf(chainWarmScriptSet), key))
	}
	if chainWarmCount(env.script.keysOf(chainWarmScriptDelete), key) != 1 {
		t.Fatalf("应精确删除卡片键一次 key=%s deletes=%v", key, env.script.keysOf(chainWarmScriptDelete))
	}
	cached, err := env.cache.GetCards(context.Background(), []uint{entity.ID})
	if err != nil {
		t.Fatalf("生产解码器读取缓存失败: %v", err)
	}
	if _, ok := cached.Cards[entity.ID]; ok {
		t.Fatalf("不可见视频不应出现在缓存读取结果 cards=%+v", cached.Cards)
	}
	env.logf("cache_deleted key=%s readback=redis.Nil writes=%d", key, setCount)

	if result := env.consumer.handleDelivery(context.Background(), amqp.Delivery{Body: payload}); result != mq.ResultAck {
		t.Fatalf("重放持久化载荷应被确认 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardSkippedNotPublic)
	if _, err := env.rawValue(key); !errors.Is(err, redisdriver.Nil) {
		t.Fatalf("重放载荷后卡片键仍应缺失 err=%v", err)
	}
	env.assertQueuesEmpty()
	env.assertOutboxSettled()
}

// 测试目标：验证关闭发布事件开关时仍能派发既有 video.published 事件
// 预期效果：完成处理不新增事件，既有事件被真实 relay 与消费者按原标识处理并预热卡片
func TestCardWarmChainConsumesExistingEventWithoutRepoFlag(t *testing.T) {
	env := chainWarmNewEnv(t, video.WithPublishedEvents(false))
	entity, _ := env.seedProcessing()
	preExisting := video.OutboxEvent{
		EventID:   uuid.NewString(),
		VideoID:   entity.ID,
		EventType: video.VideoPublishedEventType,
		Status:    video.OutboxEventStatusPending,
	}
	if err := env.db.Create(&preExisting).Error; err != nil {
		t.Fatalf("写入既有发布事件失败: %v", err)
	}
	env.trackEvent(preExisting.EventID)

	publishedEvent := env.completeProcessing(entity.ID)
	if count := env.publishedEventCount(entity.ID); count != 1 {
		t.Fatalf("关闭开关时完成处理不应新增发布事件 count=%d", count)
	}
	if publishedEvent.EventID != preExisting.EventID {
		t.Fatalf("既有事件被改写 persisted=%s want=%s", publishedEvent.EventID, preExisting.EventID)
	}
	row := env.currentVideo(entity.ID)
	env.logf("flag_off video_id=%d status=%s published_at=%s existing_event_id=%s uuid_valid=%t published_event_count=%d",
		entity.ID, row.Status, chainWarmFormatTime(row.PublishedAt), preExisting.EventID,
		chainWarmValidUUID(preExisting.EventID), env.publishedEventCount(entity.ID))
	if row.Status != video.VideoStatusPublished || row.PublishedAt == nil {
		t.Fatalf("完成处理后视频应为已发布 got status=%s published_at=%s", row.Status, chainWarmFormatTime(row.PublishedAt))
	}

	env.dispatchPublished()
	env.assertEventDispatched(preExisting.ID)
	env.assertOutboxSettled()

	delivery, msg := env.consumePublished()
	if msg.EventID != preExisting.EventID {
		t.Fatalf("既有事件标识被改写 payload=%s persisted=%s", msg.EventID, preExisting.EventID)
	}
	if result := env.deliver(delivery); result != mq.ResultAck {
		t.Fatalf("消费者应确认投递 got=%v", result)
	}
	env.assertLogLine(entity.ID, applicationfeed.CardWarmed)

	key := env.exactKey(entity.ID)
	value, err := env.rawValue(key)
	if err != nil {
		t.Fatalf("读取卡片键 %s 失败: %v", key, err)
	}
	env.logf("redis_key=%s value=%s", key, value)
	if chainWarmCount(env.script.keysOf(chainWarmScriptSet), key) != 1 {
		t.Fatalf("真实写入键应精确命中一次 key=%s sets=%v", key, env.script.keysOf(chainWarmScriptSet))
	}
	env.assertQueuesEmpty()
}
