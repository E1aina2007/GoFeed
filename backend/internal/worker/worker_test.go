package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"

	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"
)

// 测试目标：配置 worker 闭环集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：记录发布调用并支持注入故障
// 预期效果：用例可断言载荷、消息头与路由键，也可模拟 broker 不可用
type fakePublisher struct {
	mu          sync.Mutex
	messages    []ProcessMessage
	headers     []amqp.Table
	exchanges   []string
	routingKeys []string
	attempts    []string
	err         error
}

func (f *fakePublisher) Publish(ctx context.Context, exchange, routingKey string, payload any) error {
	return f.PublishWithHeaders(ctx, exchange, routingKey, payload, nil)
}

func (f *fakePublisher) PublishWithHeaders(_ context.Context, exchange, routingKey string, payload any, headers amqp.Table) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, routingKey)
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, payload.(ProcessMessage))
	f.headers = append(f.headers, headers)
	f.exchanges = append(f.exchanges, exchange)
	f.routingKeys = append(f.routingKeys, routingKey)
	return nil
}

// 测试目标：记录原消息是否被确认
// 预期效果：用例可断言重试路径的确认顺序
type ackRecorder struct {
	acked bool
}

func (a *ackRecorder) ack(_ bool) error {
	a.acked = true
	return nil
}

// 测试目标：按表名向语句注入错误的测试夹具
// 预期效果：用例可精确制造数据库基础设施故障
type faultInjection struct {
	mu     sync.Mutex
	target *faultTarget
}

type faultTarget struct {
	table string
	err   error
}

func (f *faultInjection) inject(tx *gorm.DB) {
	f.mu.Lock()
	target := f.target
	f.mu.Unlock()
	if target == nil || tx.Statement == nil || tx.Statement.Context == nil {
		return
	}
	if tx.Statement.Table != target.table {
		return
	}
	tx.AddError(target.err)
}

func (f *faultInjection) arm(table string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = &faultTarget{table: table, err: err}
}

func (f *faultInjection) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = nil
}

// 测试目标：注册按表名短路的故障回调
// 预期效果：未标记目标表时为 no-op，用例结束自动移除回调
func registerFaultInjection(t *testing.T, gdb *gorm.DB) *faultInjection {
	t.Helper()
	faults := &faultInjection{}
	callback := func(tx *gorm.DB) { faults.inject(tx) }
	registrations := []struct {
		name     string
		register func() error
		remove   func() error
	}{
		{
			name: "gofeed:worker_fault_query",
			register: func() error {
				return gdb.Callback().Query().Before("gorm:query").Register("gofeed:worker_fault_query", callback)
			},
			remove: func() error { return gdb.Callback().Query().Remove("gofeed:worker_fault_query") },
		},
		{
			name: "gofeed:worker_fault_create",
			register: func() error {
				return gdb.Callback().Create().Before("gorm:create").Register("gofeed:worker_fault_create", callback)
			},
			remove: func() error { return gdb.Callback().Create().Remove("gofeed:worker_fault_create") },
		},
		{
			name: "gofeed:worker_fault_update",
			register: func() error {
				return gdb.Callback().Update().Before("gorm:update").Register("gofeed:worker_fault_update", callback)
			},
			remove: func() error { return gdb.Callback().Update().Remove("gofeed:worker_fault_update") },
		},
	}
	t.Cleanup(func() {
		faults.disarm()
		for _, registration := range registrations {
			if err := registration.remove(); err != nil {
				t.Errorf("移除故障回调 %s 失败: %v", registration.name, err)
			}
		}
	})
	for _, registration := range registrations {
		if err := registration.register(); err != nil {
			t.Fatalf("注册故障回调失败: %v", err)
		}
	}
	return faults
}

// 测试目标：写入一条处理中视频与 outbox 事件
// 预期效果：返回已回填标识的视频行与待派发事件
func seedProcessingVideo(t *testing.T, repo *video.Repository, db *gorm.DB, id int64) video.Video {
	t.Helper()
	ctx := context.Background()
	playURL := "/static/videos/1/20260801/clip.mp4"
	coverURL := "/static/covers/1/20260801/cover.png"
	publishedAt := testTime()
	row := video.Video{
		AuthorID: 1, Title: "处理视频", Status: video.VideoStatusProcessing,
		PlayURL: playURL, PlayFileName: "clip.mp4", PlayOriginalName: "clip.mp4",
		CoverURL: coverURL, CoverFileName: "cover.png", CoverOriginalName: "cover.png",
		PublishedAt: &publishedAt,
	}
	if err := repo.Create(ctx, &row); err != nil {
		t.Fatalf("创建处理视频失败: %v", err)
	}
	event := video.OutboxEvent{
		EventID:   fmt.Sprintf("evt-%d", id),
		VideoID:   row.ID,
		EventType: video.VideoProcessEventType,
		Status:    video.OutboxEventStatusPending,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("创建 outbox 事件失败: %v", err)
	}
	return row
}

// 测试目标：按公开 URL 相对路径写入媒体文件
// 预期效果：被测处理逻辑可校验到完整媒体
func writeMediaFile(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建媒体目录失败: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("写入媒体文件失败: %v", err)
	}
}

// 测试目标：验证过期租约接管已完成视频时收口 outbox 而不再投递
// 预期效果：published 和 rejected 终态的事件均标记 dispatched，消息发布器不产生重复调用
func TestRelayMarksTakenOverTerminalEventDispatched(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	for _, status := range []string{video.VideoStatusPublished, video.VideoStatusRejected} {
		t.Run(status, func(t *testing.T) {
			publisher := &fakePublisher{}
			relay := NewRelay(repo, publisher)
			row := seedProcessingVideo(t, repo, db, int64(len(status)))
			var event video.OutboxEvent
			if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
				t.Fatalf("读取初始 outbox 事件失败: %v", err)
			}
			claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), 1, time.Minute)
			if err != nil || len(claimed) != 1 {
				t.Fatalf("领取初始租约失败 got=%d err=%v", len(claimed), err)
			}
			if err := db.Model(&video.Video{}).Where("id = ?", row.ID).Update("status", status).Error; err != nil {
				t.Fatalf("构造 %s 终态失败: %v", status, err)
			}
			if err := db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).
				Update("locked_until", gorm.Expr("TIMESTAMPADD(SECOND, -1, NOW(3))")).Error; err != nil {
				t.Fatalf("构造过期租约失败: %v", err)
			}

			if err := relay.dispatchRound(context.Background()); err != nil {
				t.Fatalf("接管终态事件失败: %v", err)
			}
			if len(publisher.messages) != 0 {
				t.Fatalf("终态接管不应重复发布消息 got=%d", len(publisher.messages))
			}
			var stored video.OutboxEvent
			if err := db.First(&stored, event.ID).Error; err != nil {
				t.Fatalf("读取接管后的 outbox 事件失败: %v", err)
			}
			if stored.Status != video.OutboxEventStatusDispatched || stored.Attempt != 2 || stored.DispatchedAt == nil {
				t.Fatalf("终态接管应收口为 dispatched got=%+v", stored)
			}
		})
	}
}

// 测试目标：验证发布失败的事件写回 pending 并安排退避，退避到期后可重新派发
// 预期效果：失败后状态为 pending 且 next_attempt_at 在未来，清除退避后派发成功
func TestRelayKeepsPendingWhenPublishFails(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	publisher := &fakePublisher{err: errors.New("broker unavailable")}
	relay := NewRelay(repo, publisher)
	row := seedProcessingVideo(t, repo, db, 2)

	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	var event video.OutboxEvent
	if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusPending {
		t.Fatalf("发布失败的事件应写回 pending got=%+v", event)
	}
	if event.NextAttemptAt == nil || !event.NextAttemptAt.After(time.Now()) {
		t.Fatalf("失败事件应安排未来重试 got=%v", event.NextAttemptAt)
	}
	if event.LastError == "" {
		t.Fatal("失败事件应记录原因")
	}

	// 测试目标：模拟退避到期后同一事件可成功派发
	// 预期效果：清除 next_attempt_at 后恢复派发并标记已派发
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).Update("next_attempt_at", nil).Error; err != nil {
		t.Fatalf("清除退避失败: %v", err)
	}
	publisher.err = nil
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("恢复后派发失败: %v", err)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("恢复后应派发一条消息 got=%d", len(publisher.messages))
	}
}

// 测试目标：验证视频行软删除后的事件按固定不一致退避释放
// 预期效果：不产生消息，事件回到 pending 并按固定退避推迟到五分钟之后
func TestRelayReleasesOrphanEventWithFixedBackoff(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	publisher := &fakePublisher{}
	relay := NewRelay(repo, publisher)
	row := seedProcessingVideo(t, repo, db, 3)
	if err := db.Delete(&video.Video{}, row.ID).Error; err != nil {
		t.Fatalf("软删除视频行失败: %v", err)
	}

	startedAt := time.Now()
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("缺少视频快照的事件不应派发消息 got=%d", len(publisher.messages))
	}
	var event video.OutboxEvent
	if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusPending {
		t.Fatalf("不一致事件应回到 pending got=%+v", event)
	}
	if event.NextAttemptAt == nil {
		t.Fatal("不一致事件应安排固定退避")
	}
	if event.NextAttemptAt.Before(startedAt.Add(outboxInconsistentBackoff-time.Minute)) ||
		event.NextAttemptAt.After(time.Now().Add(outboxInconsistentBackoff+time.Minute)) {
		t.Fatalf("不一致事件应按固定退避释放 got=%v want≈%v", event.NextAttemptAt, outboxInconsistentBackoff)
	}
	if event.LockedUntil != nil {
		t.Fatalf("释放后不应保留租约 got=%v", event.LockedUntil)
	}
	if event.LastError == "" {
		t.Fatal("不一致事件应记录原因")
	}
}

// 测试目标：验证基础设施故障时最多安排三次延迟重试，达到上限的投递仍执行处理
// 预期效果：前三次失败依次投递到 1s、5s、30s 队列并递增计数头，基础设施恢复后第 3 次重试成功确认
func TestConsumerSchedulesThreeRetriesThenProcessesFinalAttempt(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	faults := registerFaultInjection(t, db)
	publisher := &fakePublisher{}
	consumer := NewConsumer(repo, publisher, root)
	row := seedProcessingVideo(t, repo, db, 7)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})
	spec := mq.VideoProcessSpec()

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-retry", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}

	faults.arm("videos", errors.New("injected database outage"))
	// 测试目标：验证前三次失败各自安排一次延迟重试
	// 预期效果：计数头从一递增到三，投递目标依次为三档重试队列
	for attempt := 0; attempt < spec.Retry.MaxRetries; attempt++ {
		delivery := amqp.Delivery{Body: body, Headers: amqp.Table{"x-retry-attempt": int32(attempt)}}
		if result := consumer.handleDelivery(context.Background(), delivery); result != mq.ResultRetry {
			t.Fatalf("第 %d 次投递应返回重试结果 got=%v", attempt, result)
		}
		recorder := &ackRecorder{}
		if err := consumer.republishRetry(context.Background(), body, attempt, recorder.ack); err != nil {
			t.Fatalf("第 %d 次投递重试队列失败: %v", attempt, err)
		}
		if !recorder.acked {
			t.Fatalf("第 %d 次投递重试成功后应确认原消息", attempt)
		}
		if got, want := publisher.routingKeys[attempt], spec.RetryQueueName(attempt); got != want {
			t.Fatalf("第 %d 次投递目标队列错误 got=%s want=%s", attempt, got, want)
		}
		header, ok := publisher.headers[attempt]["x-retry-attempt"].(int)
		if !ok || header != attempt+1 {
			t.Fatalf("第 %d 次投递计数头错误 got=%v want=%d", attempt, publisher.headers[attempt]["x-retry-attempt"], attempt+1)
		}
	}

	// 测试目标：验证达到重试上限的那次投递仍然执行处理
	// 预期效果：基础设施恢复后处理成功并确认，不再安排第四次重试
	faults.disarm()
	final := amqp.Delivery{Body: body, Headers: amqp.Table{"x-retry-attempt": int32(spec.Retry.MaxRetries)}}
	if result := consumer.handleDelivery(context.Background(), final); result != mq.ResultAck {
		t.Fatalf("达到重试上限的投递在基础设施恢复后应确认 got=%v", result)
	}
	if len(publisher.messages) != spec.Retry.MaxRetries {
		t.Fatalf("基础设施恢复后不应再安排重试 got=%d", len(publisher.messages))
	}
	var updated video.Video
	if err := db.First(&updated, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if updated.Status != video.VideoStatusPublished {
		t.Fatalf("视频应已发布 got=%+v", updated)
	}
}

// 测试目标：固定测试基准时间
// 预期效果：用例共享同一发布时刻，避免时区与时钟差异
func testTime() time.Time {
	return time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
}

// 测试目标：记录快照读取次数并返回预设快照或错误
// 预期效果：观测器用例可断言采集次数与错误传播
type fakeOutboxReader struct {
	mu       sync.Mutex
	snapshot video.OutboxSnapshot
	err      error
	calls    int
}

func (f *fakeOutboxReader) GetOutboxSnapshot(context.Context) (video.OutboxSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.snapshot, f.err
}

func (f *fakeOutboxReader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// 测试目标：记录队列深度查询并返回预设深度或错误
// 预期效果：观测器用例可断言查询的队列名与深度来源
type fakeQueueDepthReader struct {
	mu     sync.Mutex
	depth  int
	err    error
	queues []string
}

func (f *fakeQueueDepthReader) QueueDepth(queue string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues = append(f.queues, queue)
	return f.depth, f.err
}

func (f *fakeQueueDepthReader) queriedQueues() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queues...)
}

// syncLogBuffer 串行化日志捕获的写入与读取
// 测试目标：让后台消费循环写日志与用例断言读日志并发安全
// 预期效果：-race 下不出现 strings.Builder 的数据竞争
type syncLogBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 测试目标：捕获标准日志输出并在用例结束后恢复
// 预期效果：观测日志断言不污染其他用例输出，后台协程并发写日志也安全
func captureWorkerLogs(t *testing.T) *syncLogBuffer {
	t.Helper()
	output := &syncLogBuffer{}
	log.SetOutput(output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	return output
}

// 测试目标：验证 Run 按 interval 周期重复采集
// 预期效果：短周期下采集次数随时间增长，取消后停止增长并退出
func TestMQObserverRunCollectsAtInterval(t *testing.T) {
	reader := &fakeOutboxReader{}
	observer := NewMQObserver(reader, &fakeQueueDepthReader{depth: 1})
	observer.interval = 15 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		observer.Run(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for reader.callCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("应按周期重复采集 got=%d", reader.callCount())
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Run 应退出")
	}
	before := reader.callCount()
	time.Sleep(50 * time.Millisecond)
	if after := reader.callCount(); after != before {
		t.Fatalf("取消后不应继续采集 got=%d want=%d", after, before)
	}
}

// 测试目标：为路由测试准备已发布快照及测试专用事件
// 预期效果：新类型仅存在于隔离测试库，不改变业务事件写入逻辑
func seedRouteEvent(t *testing.T, db *gorm.DB, eventID, eventType string) video.OutboxEvent {
	t.Helper()
	row := seedProcessingVideo(t, video.NewRepository(db), db, 200)
	if err := db.Model(&video.Video{}).Where("id = ?", row.ID).Update("status", video.VideoStatusPublished).Error; err != nil {
		t.Fatal(err)
	}
	var event video.OutboxEvent
	if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&event).Updates(map[string]any{"event_id": eventID, "event_type": eventType}).Error; err != nil {
		t.Fatal(err)
	}
	event.EventID, event.EventType = eventID, eventType
	return event
}

type routeSnapshotMessage struct {
	EventID string `json:"event_id"`
	VideoID uint   `json:"video_id"`
	Title   string `json:"title"`
}

// 测试目标：定义状态与载荷不同于视频处理的测试路由
// 预期效果：已发布快照由自己的规则构造载荷，不套用 processing 或接管终态规则
func snapshotTestRoute(event mq.EventSpec) RelayRoute {
	return RelayRoute{Event: event, Prepare: func(dispatch video.OutboxDispatch) (RelayPreparation, error) {
		if !dispatch.HasVideo || dispatch.Video.Status != video.VideoStatusPublished {
			return RelayPreparation{}, errors.New("snapshot is not published")
		}
		return RelayPreparation{Payload: routeSnapshotMessage{EventID: dispatch.Event.EventID, VideoID: dispatch.Video.ID, Title: dispatch.Video.Title}}, nil
	}}
}

// 测试目标：记录不同路由载荷并模拟暂态发布故障
// 预期效果：可断言失败与恢复使用同一目标和事件载荷
type routeRecordingPublisher struct {
	payloads []any
	targets  []mq.EventSpec
	err      error
}

func (p *routeRecordingPublisher) Publish(_ context.Context, exchange, routingKey string, payload any) error {
	p.payloads = append(p.payloads, payload)
	p.targets = append(p.targets, mq.EventSpec{Exchange: exchange, RoutingKey: routingKey})
	return p.err
}

func (p *routeRecordingPublisher) PublishWithHeaders(ctx context.Context, exchange, routingKey string, payload any, _ amqp.Table) error {
	return p.Publish(ctx, exchange, routingKey, payload)
}

// 测试目标：自定义路由发布失败后在退避结束重试同一事件
// 预期效果：失败保留 pending，未到期不重试，恢复后使用同一目标及载荷并标记 dispatched
func TestRelayCustomRouteRetriesSameEvent(t *testing.T) {
	db := testutil.DB(t)
	spec := mq.EventSpec{EventType: "test.snapshot", Exchange: "test.events", RoutingKey: "snapshot.ready"}
	event := seedRouteEvent(t, db, "snapshot-retry", spec.EventType)
	publisher := &routeRecordingPublisher{err: errors.New("injected publish failure")}
	relay, err := NewRelayWithRoutes(video.NewRepository(db), publisher, VideoProcessRoute(), snapshotTestRoute(spec))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stored video.OutboxEvent
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != video.OutboxEventStatusPending || stored.Attempt != 1 || stored.LockedUntil != nil || stored.DispatchedAt != nil || stored.LastError != publisher.err.Error() || stored.NextAttemptAt == nil || stored.NextAttemptAt.Before(started) || stored.NextAttemptAt.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("发布失败应保留事件并使用首次指数退避 got=%+v", stored)
	}
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).
		Update("next_attempt_at", gorm.Expr("TIMESTAMPADD(MINUTE, 1, NOW(3))")).Error; err != nil {
		t.Fatal(err)
	}
	if err := relay.dispatchRound(context.Background()); err != nil || len(publisher.payloads) != 1 {
		t.Fatalf("未到退避时间不应重试 payloads=%+v err=%v", publisher.payloads, err)
	}
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).Update("next_attempt_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	publisher.err = nil
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPayload := routeSnapshotMessage{EventID: event.EventID, VideoID: event.VideoID, Title: "处理视频"}
	wantTarget := mq.EventSpec{Exchange: spec.Exchange, RoutingKey: spec.RoutingKey}
	if len(publisher.payloads) != 2 || !reflect.DeepEqual(publisher.payloads[0], wantPayload) || !reflect.DeepEqual(publisher.payloads[1], wantPayload) || publisher.targets[0] != wantTarget || publisher.targets[1] != wantTarget {
		t.Fatalf("恢复后必须使用同一事件及目标 payloads=%+v targets=%+v", publisher.payloads, publisher.targets)
	}
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != video.OutboxEventStatusDispatched || stored.Attempt != 2 || stored.DispatchedAt == nil || stored.LockedUntil != nil {
		t.Fatalf("恢复后应收口为已派发 got=%+v", stored)
	}
}

// 测试目标：验证未知类型与路由校验失败释放租约且不阻断同批合法事件
// 预期效果：失败事件保持 pending 并固定退避，后续 video.process 正常派发且重复轮次不重发
func TestRelayRouteFailuresDoNotBlockBatch(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	unknown := seedRouteEvent(t, db, "unknown-event", "test.unknown")
	invalid := seedProcessingVideo(t, repo, db, 201)
	if err := db.Model(&video.Video{}).Where("id = ?", invalid.ID).Update("published_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	valid := seedProcessingVideo(t, repo, db, 202)
	publisher := &fakePublisher{}
	relay := NewRelay(repo, publisher)
	started := time.Now()
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, videoID := range []uint{unknown.VideoID, invalid.ID} {
		var stored video.OutboxEvent
		if err := db.First(&stored, "video_id = ?", videoID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != video.OutboxEventStatusPending || stored.Attempt != 1 || stored.LockedUntil != nil || stored.DispatchedAt != nil || stored.LastError == "" || stored.NextAttemptAt == nil || stored.NextAttemptAt.Before(started.Add(outboxInconsistentBackoff-time.Second)) {
			t.Fatalf("失败路由应释放租约并固定退避 got=%+v", stored)
		}
	}
	if len(publisher.messages) != 1 || publisher.messages[0].VideoID != valid.ID {
		t.Fatalf("同批后续合法事件应正常派发 got=%+v", publisher.messages)
	}
	if err := relay.dispatchRound(context.Background()); err != nil || len(publisher.attempts) != 1 {
		t.Fatalf("重复轮次不应重发已派发或退避中的事件 attempts=%v err=%v", publisher.attempts, err)
	}
}

// 测试目标：在真实 MySQL 与 RabbitMQ 隔离拓扑上按事件类型派发不同载荷
// 预期效果：同批事件进入各自目标，已发布测试事件接管时仍投递自己的载荷，确认后才标记 dispatched
func TestRelayMultipleRoutesIntegration(t *testing.T) {
	db := testutil.DB(t)
	conn := newIntegrationConnection(t)
	broker := newIntegrationRuntime(t)
	processSpec := declareWorkerProcessTopology(t, conn)
	snapshotSpec := declareWorkerProcessTopology(t, conn)
	snapshotSpec.Event.EventType = "test.snapshot"
	processRow := seedProcessingVideo(t, video.NewRepository(db), db, 203)
	snapshotEvent := seedRouteEvent(t, db, "snapshot-event", snapshotSpec.Event.EventType)
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", snapshotEvent.ID).Updates(map[string]any{
		"status": video.OutboxEventStatusPublishing, "attempt": 1,
		"locked_until": gorm.Expr("TIMESTAMPADD(SECOND, -1, NOW(3))"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	processRoute := VideoProcessRoute()
	processRoute.Event = processSpec.Event
	relay, err := NewRelayWithRoutes(video.NewRepository(db), broker, processRoute, snapshotTestRoute(snapshotSpec.Event))
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatal(err)
	}
	processDelivery := consumeDelivery(t, conn, processSpec.Queue)
	var processMessage ProcessMessage
	if err := json.Unmarshal(processDelivery.Body, &processMessage); err != nil {
		t.Fatal(err)
	}
	if processMessage.VideoID != processRow.ID || processMessage.EventID != "evt-203" || processMessage.SchemaVersion != mq.SchemaVersion || processMessage.PlayURL != processRow.PlayURL || processMessage.CoverURL != processRow.CoverURL || processDelivery.Exchange != processSpec.Event.Exchange || processDelivery.RoutingKey != processSpec.Event.RoutingKey {
		t.Fatalf("视频处理目标或载荷错误 delivery=%+v payload=%+v", processDelivery, processMessage)
	}
	snapshotDelivery := consumeDelivery(t, conn, snapshotSpec.Queue)
	var snapshotMessage routeSnapshotMessage
	if err := json.Unmarshal(snapshotDelivery.Body, &snapshotMessage); err != nil {
		t.Fatal(err)
	}
	if snapshotMessage != (routeSnapshotMessage{EventID: snapshotEvent.EventID, VideoID: snapshotEvent.VideoID, Title: "处理视频"}) || snapshotDelivery.Exchange != snapshotSpec.Event.Exchange || snapshotDelivery.RoutingKey != snapshotSpec.Event.RoutingKey {
		t.Fatalf("测试路由目标或载荷错误 delivery=%+v payload=%+v", snapshotDelivery, snapshotMessage)
	}
	for _, delivery := range []amqp.Delivery{processDelivery, snapshotDelivery} {
		if err := delivery.Ack(false); err != nil {
			t.Fatal(err)
		}
	}
	var stored []video.OutboxEvent
	if err := db.Order("id ASC").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Attempt != 1 || stored[1].Attempt != 2 {
		t.Fatalf("派发次数错误 got=%+v", stored)
	}
	for _, event := range stored {
		if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil || event.LockedUntil != nil {
			t.Fatalf("broker 确认后应标记已派发并释放租约 got=%+v", event)
		}
	}
}
