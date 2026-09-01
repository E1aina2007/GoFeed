package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// fakePublisher 记录发布调用并支持注入故障
type fakePublisher struct {
	mu       sync.Mutex
	messages []ProcessMessage
	headers  []amqp.Table
	err      error
}

func (f *fakePublisher) Publish(_ context.Context, _, _ string, payload any) error {
	return f.PublishWithHeaders(context.Background(), "", "", payload, nil)
}

func (f *fakePublisher) PublishWithHeaders(_ context.Context, _, _ string, payload any, headers amqp.Table) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, payload.(ProcessMessage))
	f.headers = append(f.headers, headers)
	return nil
}

// faultInjection 按表名向语句注入错误的测试夹具
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

// registerFaultInjection 注册按表名短路的故障回调，无标记时为 no-op
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

// seedProcessingVideo 写入一条处理中视频与 outbox 事件，返回视频行
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

// writeMediaFile 按公开 URL 相对路径写入媒体文件
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
	if msg.SchemaVersion != 1 || msg.EventID == "" || msg.VideoID == 0 || msg.PlayURL == "" || msg.CoverURL == "" {
		t.Fatalf("消息载荷错误 got=%+v", msg)
	}

	var event video.OutboxEvent
	if err := db.First(&event, "event_id = ?", msg.EventID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusDispatched || event.DispatchedAt == nil {
		t.Fatalf("事件应被标记已派发 got=%+v", event)
	}
}

// 测试目标：验证发布失败的事件保持 pending 由下一轮重试
// 预期效果：派发失败不标记事件，视频状态不变
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
		t.Fatalf("发布失败的事件应保持 pending got=%+v", event)
	}

	// 恢复后同一事件可成功派发
	publisher.err = nil
	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("恢复后派发失败: %v", err)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("恢复后应派发一条消息 got=%d", len(publisher.messages))
	}
}

// 测试目标：验证 relay 跳过视频行已删除的孤立事件
// 预期效果：不一致事件不产生消息且保持 pending，供人工处置
func TestRelaySkipsOrphanEvents(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	publisher := &fakePublisher{}
	relay := NewRelay(repo, publisher)
	row := seedProcessingVideo(t, repo, db, 3)
	if err := db.Delete(&video.Video{}, row.ID).Error; err != nil {
		t.Fatalf("删除视频行失败: %v", err)
	}

	if err := relay.dispatchRound(context.Background()); err != nil {
		t.Fatalf("派发轮次失败: %v", err)
	}
	if len(publisher.messages) != 0 {
		t.Fatalf("孤立事件不应派发消息 got=%d", len(publisher.messages))
	}
	var event video.OutboxEvent
	if err := db.First(&event, "video_id = ?", row.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if event.Status != video.OutboxEventStatusPending {
		t.Fatalf("孤立事件应保持 pending got=%+v", event)
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

	msg := ProcessMessage{SchemaVersion: 1, EventID: "evt-ok", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
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

	msg := ProcessMessage{SchemaVersion: 1, EventID: "evt-missing", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
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
	msg := ProcessMessage{SchemaVersion: 1, EventID: "evt-dup", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}

	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("首次消费失败: %v", err)
	}
	if err := consumer.process(context.Background(), msg); err != nil {
		t.Fatalf("重复消费应幂等成功: %v", err)
	}
}

// 测试目标：验证损坏载荷与未知版本进入死信
// 预期效果：解码失败和不支持版本均返回死信动作
func TestConsumerHandleDeliveryDeadLettersCorruptPayload(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	consumer := NewConsumer(repo, &fakePublisher{}, t.TempDir())

	corrupt := amqp.Delivery{Body: []byte("not-json")}
	if result := consumer.handleDelivery(context.Background(), corrupt); result != consumeDeadLetter {
		t.Fatalf("损坏载荷应进入死信 got=%v", result)
	}
	stale := amqp.Delivery{Body: []byte(`{"schema_version":99}`)}
	if result := consumer.handleDelivery(context.Background(), stale); result != consumeDeadLetter {
		t.Fatalf("未知版本应进入死信 got=%v", result)
	}
}

// 测试目标：验证基础设施故障按重试计数退避重发并在耗尽后进入死信
// 预期效果：首次故障退避后重发携带递增头并确认，重试耗尽返回死信动作
func TestConsumerHandleDeliveryRetriesOnDatabaseFailure(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	faults := registerFaultInjection(t, db)
	publisher := &fakePublisher{}
	consumer := NewConsumer(repo, publisher, t.TempDir())
	row := seedProcessingVideo(t, repo, db, 7)

	msg := ProcessMessage{SchemaVersion: 1, EventID: "evt-retry", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}

	faults.arm("videos", errors.New("injected database outage"))
	if result := consumer.handleDelivery(context.Background(), amqp.Delivery{Body: body}); result != consumeAck {
		t.Fatalf("重发路径应确认原消息 got=%v", result)
	}
	if len(publisher.messages) != 1 || publisher.headers[0]["x-retry-attempt"].(int) != 1 {
		t.Fatalf("应重发一次并携带计数头 got=%+v headers=%+v", publisher.messages, publisher.headers)
	}

	exhausted := amqp.Delivery{Body: body, Headers: amqp.Table{"x-retry-attempt": int32(3)}}
	if result := consumer.handleDelivery(context.Background(), exhausted); result != consumeDeadLetter {
		t.Fatalf("重试耗尽应进入死信 got=%v", result)
	}
	faults.disarm()
}

// 测试目标：验证退避期间上下文取消时不确认消息
// 预期效果：返回重投动作，消息由 broker 在信道关闭后重新投递
func TestConsumerHandleDeliveryRequeuesOnContextCancel(t *testing.T) {
	db := testutil.DB(t)
	repo := video.NewRepository(db)
	faults := registerFaultInjection(t, db)
	consumer := NewConsumer(repo, &fakePublisher{}, t.TempDir())
	row := seedProcessingVideo(t, repo, db, 8)

	msg := ProcessMessage{SchemaVersion: 1, EventID: "evt-cancel", VideoID: row.ID, PlayURL: row.PlayURL, CoverURL: row.CoverURL}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("编码消息失败: %v", err)
	}

	faults.arm("videos", errors.New("injected database outage"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := consumer.handleDelivery(ctx, amqp.Delivery{Body: body}); result != consumeRequeue {
		t.Fatalf("上下文取消应返回重投动作 got=%v", result)
	}
	faults.disarm()
}

// 测试目标：验证重发退避按重试次数指数增长并封顶
// 预期效果：退避时长为 1s、2s、4s 且不超过上限
func TestRetryBackoff(t *testing.T) {
	for attempt, want := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 2: 4 * time.Second, 10: maxRetryBackoff} {
		if got := retryBackoff(attempt); got != want {
			t.Fatalf("退避 %d 错误 got=%v want=%v", attempt, got, want)
		}
	}
}

// testTime 固定测试基准时间
func testTime() time.Time {
	return time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
}
