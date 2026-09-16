package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gofeed/internal/mq"
	"gofeed/internal/testutil"
	"gofeed/internal/video"

	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
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

type faultContextKey struct{}

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

// 测试目标：验证 relay 与 consumer 的契约全部取自 mq 规格
// 预期效果：事件规格取自 VideoProcessEventSpec，消费规格整体等于 VideoProcessSpec
func TestWorkerUsesMQSpecAsSingleSource(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	relay := NewRelay(repo, &fakePublisher{})
	consumer := NewConsumer(repo, &fakePublisher{}, t.TempDir())
	spec := mq.VideoProcessSpec()

	if relay.spec != mq.VideoProcessEventSpec() {
		t.Fatalf("relay 事件规格应取自 VideoProcessEventSpec got=%+v", relay.spec)
	}
	if !reflect.DeepEqual(consumer.spec, spec) {
		t.Fatalf("consumer 消费规格应等于 VideoProcessSpec got=%+v want=%+v", consumer.spec, spec)
	}
}

// 测试目标：验证 relay 将 pending 事件派发后标记 dispatched
// 预期效果：消息携带事件与媒体快照，事件状态与派发时间落库
func TestRelayDispatchRoundMarksDispatched(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	publisher := &fakePublisher{}
	relay := NewRelay(repo, publisher)
	seedProcessingVideo(t, repo, db, 1)

	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("应派发一条消息 got=%d", len(publisher.messages))
	}
	msg := publisher.messages[0]
	if msg.SchemaVersion != mq.SchemaVersion || msg.EventID == "" || msg.VideoID == 0 || msg.PlayURL == "" || msg.CoverURL == "" {
		t.Fatalf("消息载荷错误 got=%+v", msg)
	}
	if publisher.exchanges[0] != mq.VideoProcessEventSpec().Exchange || publisher.routingKeys[0] != mq.VideoProcessEventSpec().RoutingKey {
		t.Fatalf("派发目标应取自事件规格 got=%s/%s", publisher.exchanges[0], publisher.routingKeys[0])
	}

	var event video.OutboxEvent
	if err := db.First(&event, "event_id = ?", msg.EventID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil {
		t.Fatalf("事件应被标记已派发 got=%+v", event)
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

// 测试目标：验证媒体完整的处理中视频被消费端发布
// 预期效果：processing → published 且无错误返回
func TestConsumerProcessPublishesValidMedia(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	consumer := NewConsumer(repo, &fakePublisher{}, root)
	row := seedProcessingVideo(t, repo, db, 4)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-ok", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("消费处理失败: %v", err)
	}
	var updated video.Video
	if err := db.First(&updated, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if updated.Status != video.VideoStatusPublished {
		t.Fatalf("视频应已发布 got=%+v", updated)
	}
}

// 测试目标：验证媒体缺陷被判定为确定性拒绝并记录原因
// 预期效果：视频进入 rejected 且原因落在列宽之内
func TestConsumerProcessRejectsMissingMedia(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	consumer := NewConsumer(repo, &fakePublisher{}, t.TempDir())
	row := seedProcessingVideo(t, repo, db, 5)

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-missing", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("确定性失败不应返回错误: %v", err)
	}
	var updated video.Video
	if err := db.First(&updated, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if updated.Status != video.VideoStatusRejected || updated.RejectedReason == "" || len(updated.RejectedReason) > 255 {
		t.Fatalf("视频应被拒绝并记录原因 got=%+v", updated)
	}
}

// 测试目标：验证重复消息与状态已流转的投递按幂等确认
// 预期效果：published 视频重复消费不报错也不改变状态
func TestConsumerProcessDuplicateMessageIsNoop(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	consumer := NewConsumer(repo, &fakePublisher{}, root)
	row := seedProcessingVideo(t, repo, db, 6)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})
	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-dup", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}

	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("首次消费失败: %v", err)
	}
	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("重复消费应幂等成功: %v", err)
	}
}

// 测试目标：验证损坏载荷与未知版本进入死信
// 预期效果：解码失败和不支持版本均返回死信结果
func TestConsumerHandleDeliveryDeadLettersCorruptPayload(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	consumer := NewConsumer(repo, &fakePublisher{}, t.TempDir())

	corrupt := amqp.Delivery{Body: []byte("not-json")}
	if result := consumer.handleDelivery(context.Background(), corrupt); result != mq.ResultDeadLetter {
		t.Fatalf("损坏载荷应进入死信 got=%v", result)
	}
	stale := amqp.Delivery{Body: []byte(`{"schema_version":99}`)}
	if result := consumer.handleDelivery(context.Background(), stale); result != mq.ResultDeadLetter {
		t.Fatalf("未知版本应进入死信 got=%v", result)
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

// 测试目标：验证重试耗尽且处理仍失败时才进入死信
// 预期效果：上限前一次投递仍返回重试结果，达到上限的投递返回死信且不再重发
func TestConsumerDeadLettersWhenFinalAttemptFails(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	root := t.TempDir()
	faults := registerFaultInjection(t, db)
	publisher := &fakePublisher{}
	consumer := NewConsumer(repo, publisher, root)
	row := seedProcessingVideo(t, repo, db, 8)
	writeMediaFile(t, root, "videos/1/20260801/clip.mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
	writeMediaFile(t, root, "covers/1/20260801/cover.png", []byte{0x89, 'P', 'N', 'G'})
	spec := mq.VideoProcessSpec()

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-exhausted", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}

	faults.arm("videos", errors.New("injected database outage"))
	// 测试目标：验证上限前一次投递仍可安排重试
	// 预期效果：第 2 次重试失败返回重试结果，说明死信边界正好在重试上限
	beforeLimit := amqp.Delivery{Body: body, Headers: amqp.Table{"x-retry-attempt": int32(spec.Retry.MaxRetries - 1)}}
	if result := consumer.handleDelivery(context.Background(), beforeLimit); result != mq.ResultRetry {
		t.Fatalf("上限前一次投递应返回重试结果 got=%v", result)
	}
	// 测试目标：验证重试耗尽且本次处理仍失败时进入死信
	// 预期效果：达到上限的投递返回死信结果且不再安排重试
	exhausted := amqp.Delivery{Body: body, Headers: amqp.Table{"x-retry-attempt": int32(spec.Retry.MaxRetries)}}
	if result := consumer.handleDelivery(context.Background(), exhausted); result != mq.ResultDeadLetter {
		t.Fatalf("重试耗尽且处理失败应进入死信 got=%v", result)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("耗尽后不应再次安排重试 got=%d", len(publisher.messages))
	}
	faults.disarm()
}

// 测试目标：验证重试消息投递失败时不确认原消息
// 预期效果：republishRetry 尝试投递到第一档重试队列后返回错误，确认回调未触发
func TestConsumerRetryPublishFailureLeavesMessageUnacked(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	publisher := &fakePublisher{err: errors.New("broker unavailable")}
	consumer := NewConsumer(repo, publisher, t.TempDir())
	spec := mq.VideoProcessSpec()

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-retry-fail", VideoID: 1}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}
	recorder := &ackRecorder{}
	if err := consumer.republishRetry(context.Background(), body, 0, recorder.ack); err == nil {
		t.Fatal("重试投递失败应返回错误")
	}
	if recorder.acked {
		t.Fatal("重试投递失败时不应确认原消息")
	}
	if len(publisher.attempts) != 1 || publisher.attempts[0] != spec.RetryQueueName(0) {
		t.Fatalf("重试投递应尝试第一档重试队列 got=%v", publisher.attempts)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("失败的投递不应记录成功消息 got=%d", len(publisher.messages))
	}
}

// 测试目标：验证 outbox 退避按时长翻倍并封顶五分钟
// 预期效果：各次尝试的退避始终为正且不超过五分钟，超长尝试沿用封顶值
func TestOutboxBackoffIsPositiveAndCapped(t *testing.T) {
	cases := map[int]time.Duration{
		1:           time.Second,
		2:           2 * time.Second,
		9:           256 * time.Second,
		10:          outboxMaxBackoff,
		64:          outboxMaxBackoff,
		math.MaxInt: outboxMaxBackoff,
		0:           time.Second,
		-7:          time.Second,
	}
	for attempt, want := range cases {
		got := outboxBackoff(attempt)
		if got <= 0 {
			t.Fatalf("退避必须为正 attempt=%d got=%v", attempt, got)
		}
		if got > outboxMaxBackoff {
			t.Fatalf("退避不得超过五分钟 attempt=%d got=%v", attempt, got)
		}
		if got != want {
			t.Fatalf("退避计算错误 attempt=%d got=%v want=%v", attempt, got, want)
		}
	}
	if outboxMaxBackoff != 5*time.Minute {
		t.Fatalf("退避封顶值应为五分钟 got=%v", outboxMaxBackoff)
	}
}

// 测试目标：固定测试基准时间
// 预期效果：用例共享同一发布时刻，避免时区与时钟差异
func testTime() time.Time {
	return time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
}

// 测试目标：写入一条已过期的 publishing 事件
// 预期效果：租约接管与派发用例可模拟持有者崩溃后的接管场景
func seedExpiredPublishingEvent(t *testing.T, db *gorm.DB, videoID uint, eventID string) video.OutboxEvent {
	t.Helper()
	event := video.OutboxEvent{
		EventID:   eventID,
		VideoID:   videoID,
		EventType: video.VideoProcessEventType,
		Status:    video.OutboxEventStatusPublishing,
		Attempt:   1,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("创建过期 publishing 事件失败: %v", err)
	}
	if err := db.Model(&video.OutboxEvent{}).Where("id = ?", event.ID).
		Update("locked_until", gorm.Expr("TIMESTAMPADD(SECOND, -60, NOW(3))")).Error; err != nil {
		t.Fatalf("构造过期租约失败: %v", err)
	}
	return event
}

// 测试目标：验证 claim 返回的派发结果正确标记租约接管
// 预期效果：过期 publishing 事件 LeaseTakenOver 为 true，普通 pending 事件为 false
func TestClaimPendingOutboxEventsFlagsLeaseTakeover(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	row := seedProcessingVideo(t, repo, db, 20)
	seedExpiredPublishingEvent(t, db, row.ID, "evt-takeover-20")

	dispatches, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("claim 失败: %v", err)
	}
	if len(dispatches) != 2 {
		t.Fatalf("应领取两条事件 got=%d", len(dispatches))
	}
	flags := make(map[string]bool, len(dispatches))
	for _, dispatch := range dispatches {
		flags[dispatch.Event.EventID] = dispatch.LeaseTakenOver
	}
	if flags["evt-20"] {
		t.Fatal("普通 pending 事件不应标记租约接管")
	}
	if !flags["evt-takeover-20"] {
		t.Fatal("过期 publishing 事件应标记租约接管")
	}
}

// 测试目标：验证 relay 接管租约派发时记录 mq_lease_takeover
// 预期效果：日志携带 event、event_id、video_id 与接管后递增的 attempt
func TestRelayLogsLeaseTakeover(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	row := seedProcessingVideo(t, repo, db, 21)
	seedExpiredPublishingEvent(t, db, row.ID, "evt-takeover-21")
	relay := NewRelay(repo, &fakePublisher{})
	logs := captureWorkerLogs(t)

	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	want := fmt.Sprintf("event=mq_lease_takeover event_id=evt-takeover-21 video_id=%d attempt=2", row.ID)
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("应记录租约接管日志 got=%q want=%q", logs.String(), want)
	}
}

// 测试目标：验证 relay 发布失败时记录 mq_publish_failed 定位字段
// 预期效果：日志携带 component=relay、event_type、event_id、video_id、attempt 与 error
func TestRelayLogsPublishFailure(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	row := seedProcessingVideo(t, repo, db, 22)
	relay := NewRelay(repo, &fakePublisher{err: errors.New("broker unavailable")})
	logs := captureWorkerLogs(t)

	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	want := fmt.Sprintf(
		"event=mq_publish_failed component=relay event_type=video.process event_id=evt-22 video_id=%d attempt=1 error=\"broker unavailable\"",
		row.ID,
	)
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("应记录发布失败日志 got=%q want=%q", logs.String(), want)
	}
}

// 测试目标：验证 consumer 重试发布失败记录 component=consumer 与目标重试队列
// 预期效果：日志携带递增 attempt、重试队列名与 error，原消息不被确认
func TestConsumerLogsRetryPublishFailure(t *testing.T) {
	repo := video.NewRepository(testutil.DB(t))
	publisher := &fakePublisher{err: errors.New("broker unavailable")}
	consumer := NewConsumer(repo, publisher, t.TempDir())
	spec := mq.VideoProcessSpec()

	msg := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-retry-log", VideoID: 9}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}
	recorder := &ackRecorder{}
	logs := captureWorkerLogs(t)

	if err := consumer.republishRetry(context.Background(), body, 0, recorder.ack); err == nil {
		t.Fatal("重试投递失败应返回错误")
	}
	want := fmt.Sprintf(
		"event=mq_publish_failed component=consumer event_type=video.process event_id=evt-retry-log video_id=9 attempt=1 queue=%s error=\"broker unavailable\"",
		spec.RetryQueueName(0),
	)
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("应记录消费端发布失败日志 got=%q want=%q", logs.String(), want)
	}
	if recorder.acked {
		t.Fatal("重试投递失败时不应确认原消息")
	}
}

// 测试目标：验证非法载荷、未知版本与重试耗尽的死信日志各自携带对应 reason
// 预期效果：三种死信决策分别记录 invalid_payload、unsupported_schema_version 与 retry_exhausted
func TestConsumerLogsDeadLetterReasons(t *testing.T) {
	db := testutil.DB(t)
	consumer := NewConsumer(video.NewRepository(db), &fakePublisher{}, t.TempDir())
	spec := mq.VideoProcessSpec()
	logs := captureWorkerLogs(t)

	// 测试目标：验证非法 JSON 的死信日志
	// 预期效果：reason=invalid_payload 且 attempt 为零
	if result := consumer.handleDelivery(context.Background(), amqp.Delivery{Body: []byte("not-json")}); result != mq.ResultDeadLetter {
		t.Fatalf("非法载荷应进入死信 got=%v", result)
	}
	if !strings.Contains(logs.String(), "event=mq_dead_letter reason=invalid_payload attempt=0") {
		t.Fatalf("非法载荷应记录 invalid_payload got=%q", logs.String())
	}

	// 测试目标：验证未知 schema 版本的死信日志
	// 预期效果：reason=unsupported_schema_version 携带 event_id、video_id 与 schema_version
	stale := ProcessMessage{SchemaVersion: 99, EventID: "evt-stale-log", VideoID: 7}
	staleBody, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}
	if result := consumer.handleDelivery(context.Background(), amqp.Delivery{Body: staleBody}); result != mq.ResultDeadLetter {
		t.Fatalf("未知版本应进入死信 got=%v", result)
	}
	if !strings.Contains(logs.String(), "reason=unsupported_schema_version event_id=evt-stale-log video_id=7 schema_version=99 attempt=0") {
		t.Fatalf("未知版本应记录 unsupported_schema_version got=%q", logs.String())
	}

	// 测试目标：验证重试耗尽的死信日志
	// 预期效果：reason=retry_exhausted 携带 event_id、video_id、attempt 与底层错误
	faults := registerFaultInjection(t, db)
	faults.arm("videos", errors.New("injected database outage"))
	exhausted := ProcessMessage{SchemaVersion: mq.SchemaVersion, EventID: "evt-exhausted-log", VideoID: 8}
	exhaustedBody, err := json.Marshal(exhausted)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}
	delivery := amqp.Delivery{Body: exhaustedBody, Headers: amqp.Table{retryHeader: int32(spec.Retry.MaxRetries)}}
	if result := consumer.handleDelivery(context.Background(), delivery); result != mq.ResultDeadLetter {
		t.Fatalf("重试耗尽应进入死信 got=%v", result)
	}
	want := fmt.Sprintf("reason=retry_exhausted event_id=evt-exhausted-log video_id=8 attempt=%d error=\"injected database outage\"", spec.Retry.MaxRetries)
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("重试耗尽应记录 retry_exhausted got=%q want=%q", logs.String(), want)
	}
	faults.disarm()
}
