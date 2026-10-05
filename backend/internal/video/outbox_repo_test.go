package video

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"gofeed/internal/testutil"
)

// 测试目标：写入一条指定状态的事件
// 预期效果：租约用例可基于该事件构造 claim 场景
func seedOutboxEvent(t *testing.T, db *gorm.DB, videoID uint, eventID, status string) OutboxEvent {
	t.Helper()
	event := OutboxEvent{
		EventID:   eventID,
		VideoID:   videoID,
		EventType: VideoProcessEventType,
		Status:    status,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("创建 outbox 事件失败: %v", err)
	}
	return event
}

// 测试目标：写入一条 processing 视频
// 预期效果：满足 outbox 外键与视频快照读取需求
func seedProcessingVideoRow(t *testing.T, repo *Repository, authorID uint, title string) *Video {
	t.Helper()
	publishedAt := baseTime
	row := newVideoFixture(authorID, title, VideoStatusProcessing, publishedAt)
	row.PublishedAt = &publishedAt
	if err := repo.Create(context.Background(), row); err != nil {
		t.Fatalf("创建处理视频失败: %v", err)
	}
	return row
}

// 测试目标：验证批量 claim 把同一批事件各自领取一次并重置租约字段
// 预期效果：每条事件只出现一次，attempt 递增为一且 next_attempt_at 清空
func TestClaimPendingOutboxEventsClaimsBatchOnce(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "批量领取视频")
	const total = 5
	ids := make([]uint, 0, total)
	for index := 0; index < total; index++ {
		event := seedOutboxEvent(t, db, row.ID, fmt.Sprintf("evt-batch-%d", index), OutboxEventStatusPending)
		ids = append(ids, event.ID)
		// 测试目标：让部分事件带已到期的退避时间
		// 预期效果：到期事件与无退避事件都能被同一轮 claim 领取
		if err := db.Model(&OutboxEvent{}).Where("id = ?", event.ID).
			Update("next_attempt_at", gorm.Expr("TIMESTAMPADD(SECOND, -10, NOW(3))")).Error; err != nil {
			t.Fatalf("构造到期退避失败: %v", err)
		}
	}

	claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), total, time.Minute)
	if err != nil {
		t.Fatalf("批量 claim 失败: %v", err)
	}
	if len(claimed) != total {
		t.Fatalf("应领取全部事件 got=%d want=%d", len(claimed), total)
	}
	seen := make(map[uint]bool, total)
	for _, dispatch := range claimed {
		if seen[dispatch.Event.ID] {
			t.Fatalf("事件 %d 在同一次 claim 中重复出现", dispatch.Event.ID)
		}
		seen[dispatch.Event.ID] = true
		if !dispatch.HasVideo {
			t.Fatalf("存在视频快照时 HasVideo 应为真 got=%+v", dispatch)
		}
		if dispatch.Event.Attempt != 1 {
			t.Fatalf("claim 后 attempt 应为一 got=%d", dispatch.Event.Attempt)
		}
		if dispatch.Event.NextAttemptAt != nil {
			t.Fatalf("claim 应清空 next_attempt_at got=%v", dispatch.Event.NextAttemptAt)
		}
	}

	var stored []OutboxEvent
	if err := db.Where("id IN ?", ids).Order("id ASC").Find(&stored).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(stored) != total {
		t.Fatalf("事件数量错误 got=%d want=%d", len(stored), total)
	}
	for _, event := range stored {
		if event.Status != OutboxEventStatusPublishing || event.Attempt != 1 || event.NextAttemptAt != nil || event.LockedUntil == nil {
			t.Fatalf("落库租约字段错误 got=%+v", event)
		}
	}

	// 测试目标：验证同一批事件不会被重复领取
	// 预期效果：租约有效期间第二次 claim 返回空
	again, err := repo.ClaimPendingOutboxEvents(context.Background(), total, time.Minute)
	if err != nil {
		t.Fatalf("重复 claim 失败: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("同一批事件不应被再次领取 got=%d", len(again))
	}
}

// 测试目标：验证两个 relay 实例并发 claim 同一批事件时不会重复取到
// 预期效果：同一条事件只被一个实例 claim，总数等于事件数
func TestClaimPendingOutboxEventsIsExclusive(t *testing.T) {
	db := testutil.DB(t)
	first := NewRepository(db)
	second := NewRepository(db.Session(&gorm.Session{NewDB: true}))
	row := seedProcessingVideoRow(t, first, 1, "并发 claim 视频")
	for i := 0; i < 4; i++ {
		seedOutboxEvent(t, db, row.ID, fmt.Sprintf("evt-concurrent-%d", i), OutboxEventStatusPending)
	}

	start := make(chan struct{})
	results := make(chan []OutboxDispatch, 2)
	for _, repository := range []*Repository{first, second} {
		repository := repository
		go func() {
			<-start
			dispatches, err := repository.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
			if err != nil {
				results <- nil
				return
			}
			results <- dispatches
		}()
	}
	close(start)

	seen := make(map[uint]int)
	total := 0
	for range 2 {
		for _, dispatch := range <-results {
			seen[dispatch.Event.ID]++
			total++
		}
	}
	if total != 4 {
		t.Fatalf("并发 claim 总数应等于事件数 got=%d", total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("事件 %d 被重复 claim %d 次", id, count)
		}
	}
}

// 测试目标：验证过期 publishing 事件可被接管，且旧持有者的围栏令牌失效
// 预期效果：接管后 attempt 递增，旧令牌的标记与释放均不生效，新持有者可完成标记
func TestClaimPendingOutboxEventsTakesOverExpiredLease(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "接管视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-lease-expired", OutboxEventStatusPublishing)

	// 测试目标：构造一个已过期的租约模拟持有者崩溃
	// 预期效果：该事件可被后续 claim 接管
	if err := db.Model(&OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
		"locked_until": gorm.Expr("TIMESTAMPADD(SECOND, -60, NOW(3))"),
		"attempt":      1,
	}).Error; err != nil {
		t.Fatalf("构造过期租约失败: %v", err)
	}

	// 测试目标：验证租约过期且尚未接管时旧持有者失效
	// 预期效果：标记与释放都不产生变更
	marked, err := repo.MarkOutboxDispatched(context.Background(), event.ID, 1)
	if err != nil {
		t.Fatalf("过期租约标记失败: %v", err)
	}
	if marked {
		t.Fatal("租约过期后原持有者不应能标记事件")
	}
	released, err := repo.ReleaseOutboxRetry(context.Background(), event.ID, 1, time.Second, errors.New("stale"))
	if err != nil {
		t.Fatalf("过期租约释放失败: %v", err)
	}
	if released {
		t.Fatal("租约过期后原持有者不应能释放事件")
	}

	// 测试目标：验证接管后新持有者拿到递增的围栏令牌
	// 预期效果：旧令牌无法覆盖状态，新令牌可完成标记
	taken, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("接管失败: %v", err)
	}
	if len(taken) != 1 || taken[0].Event.Attempt != 2 {
		t.Fatalf("过期租约应被接管并递增 attempt got=%+v", taken)
	}
	staleMark, err := repo.MarkOutboxDispatched(context.Background(), event.ID, 1)
	if err != nil {
		t.Fatalf("旧令牌标记失败: %v", err)
	}
	if staleMark {
		t.Fatal("旧围栏令牌不应能标记已接管的事件")
	}
	marked, err = repo.MarkOutboxDispatched(context.Background(), taken[0].Event.ID, taken[0].Event.Attempt)
	if err != nil || !marked {
		t.Fatalf("接管者应能标记事件 marked=%v err=%v", marked, err)
	}
}

// 测试目标：验证发布失败时写回 pending、安排退避并按 255 字节安全记录原因
// 预期效果：状态回到 pending，next_attempt_at 在未来，last_error 截断且保持有效 UTF-8
func TestReleaseOutboxRetrySchedulesNextAttempt(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "释放视频")
	seedOutboxEvent(t, db, row.ID, "evt-release", OutboxEventStatusPending)

	claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim 失败 got=%d err=%v", len(claimed), err)
	}
	// 测试目标：构造截断边界落在多字节字符中间的原因
	// 预期效果：截断结果必须丢弃半个字符并保持有效 UTF-8
	longCause := errors.New("a" + strings.Repeat("故障", 400))
	released, err := repo.ReleaseOutboxRetry(context.Background(), claimed[0].Event.ID, claimed[0].Event.Attempt, 30*time.Second, longCause)
	if err != nil || !released {
		t.Fatalf("释放失败 released=%v err=%v", released, err)
	}

	var stored OutboxEvent
	if err := db.First(&stored, claimed[0].Event.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if stored.Status != OutboxEventStatusPending || stored.LockedUntil != nil {
		t.Fatalf("释放结果错误 got=%+v", stored)
	}
	if stored.NextAttemptAt == nil || !stored.NextAttemptAt.After(time.Now()) {
		t.Fatalf("应安排未来的重试时间 got=%v", stored.NextAttemptAt)
	}
	if stored.LastError == "" {
		t.Fatal("失败原因不应为空")
	}
	if size := len([]byte(stored.LastError)); size > 255 || size < 250 {
		t.Fatalf("失败原因应截断到 255 字节以内且用满预算 got_len=%d", size)
	}
	if !utf8.ValidString(stored.LastError) {
		t.Fatalf("失败原因必须保持有效 UTF-8 got=%q", stored.LastError)
	}
	if !strings.HasPrefix(longCause.Error(), stored.LastError) {
		t.Fatalf("失败原因应是原始信息的前缀 got=%q", stored.LastError)
	}
}

// 以下用例验收「视频处理完成时同事务写入 video.published 发布事件」这一能力
// 覆盖正常流转、CAS 零行、拒绝分支、并发竞争、插入失败回滚、提交失败回滚与开关关闭七类场景

// 测试目标：验证并发完成同一视频时只有一个调用赢得状态流转并写入发布事件
// 预期效果：八个并发调用恰好一个返回变更且都不报错 视频最终为 published 发布事件恰好一条
func TestPublishedEventSingleWinnerUnderConcurrentCompletion(t *testing.T) {
	db := testutil.DB(t)
	row := seedProcessingVideoRow(t, NewRepository(db, WithPublishedEvents(true)), 1, "并发完成视频")

	const workers = 8
	type completionResult struct {
		changed bool
		err     error
	}

	start := make(chan struct{})
	results := make(chan completionResult, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo := NewRepository(db, WithPublishedEvents(true))
			<-start
			changed, err := repo.CompleteVideoProcessing(context.Background(), row.ID)
			results <- completionResult{changed: changed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("并发完成处理不应返回错误 got=%v", got.err)
		}
		if got.changed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("应恰好一个调用赢得状态流转 got=%d", winners)
	}
	if got := readPublishedEventVideo(t, db, row.ID); got.Status != VideoStatusPublished {
		t.Fatalf("视频状态应为 published got=%s", got.Status)
	}
	if events := listPublishedEvents(t, db, row.ID); len(events) != 1 {
		t.Fatalf("发布事件应恰好一条 got=%d", len(events))
	}
}

// 测试目标：验证发布事件插入失败时状态流转随事务一起回滚
// 预期效果：返回注入的插入错误 视频仍为 processing published_at 不变 outbox 无任何事件
func TestPublishedEventRolledBackWhenOutboxInsertFails(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))
	row := seedProcessingVideoRow(t, repo, 1, "事件插入失败视频")
	before := readPublishedEventVideo(t, db, row.ID)

	fault := registerPublishedEventCreateFault(t, db)
	injected := errors.New("injected outbox insert failure")
	fault.arm("video_outbox_events", injected)

	changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID)
	fault.disarm()
	if !errors.Is(err, injected) {
		t.Fatalf("应返回注入的插入错误 changed=%v err=%v", changed, err)
	}
	if changed {
		t.Fatal("插入失败回滚后不应报告状态变更")
	}
	if !fault.hit() {
		t.Fatal("注入点未被触发 用例未覆盖真实插入失败路径")
	}

	after := readPublishedEventVideo(t, db, row.ID)
	if after.Status != VideoStatusProcessing {
		t.Fatalf("插入失败应回滚状态 got=%s", after.Status)
	}
	if after.PublishedAt == nil || before.PublishedAt == nil {
		t.Fatal("published_at 不应为空")
	}
	if !before.PublishedAt.Equal(*after.PublishedAt) {
		t.Fatalf("插入失败后 published_at 不应改变 before=%v after=%v", *before.PublishedAt, *after.PublishedAt)
	}
	if total := countAllOutboxEvents(t, db); total != 0 {
		t.Fatalf("插入失败回滚后不应留下事件 got=%d", total)
	}

	// 移除注入后同一视频仍可正常完成 说明失败并非前置状态被破坏
	fault.disarm()
	if changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID); err != nil || !changed {
		t.Fatalf("解除注入后应能正常完成 changed=%v err=%v", changed, err)
	}
	if events := listPublishedEvents(t, db, row.ID); len(events) != 1 {
		t.Fatalf("解除注入后应写入一条发布事件 got=%d", len(events))
	}
}

// 测试目标：验证事务提交失败时状态流转与发布事件一起回滚
// 预期效果：返回注入的提交错误 视频仍为 processing published_at 不变 outbox 无任何事件
func TestPublishedEventRolledBackWhenCommitFails(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))
	row := seedProcessingVideoRow(t, repo, 1, "提交失败视频")
	before := readPublishedEventVideo(t, db, row.ID)

	faultRepo, fault := newPublishedEventCommitFaultRepository(t, db)
	injected := errors.New("injected commit failure")
	fault.arm(injected)

	changed, err := faultRepo.CompleteVideoProcessing(t.Context(), row.ID)
	if !errors.Is(err, injected) {
		t.Fatalf("应返回注入的提交错误 changed=%v err=%v", changed, err)
	}
	if changed {
		t.Fatal("提交失败回滚后不应报告状态变更")
	}
	if begins, commits := fault.counters(); begins != 1 || commits != 1 {
		t.Fatalf("注错点应恰好开启并提交一次事务 begins=%d commits=%d", begins, commits)
	}
	fault.disarm()

	after := readPublishedEventVideo(t, db, row.ID)
	if after.Status != VideoStatusProcessing {
		t.Fatalf("提交失败应回滚状态 got=%s", after.Status)
	}
	if after.PublishedAt == nil || before.PublishedAt == nil {
		t.Fatal("published_at 不应为空")
	}
	if !before.PublishedAt.Equal(*after.PublishedAt) {
		t.Fatalf("提交失败后 published_at 不应改变 before=%v after=%v", *before.PublishedAt, *after.PublishedAt)
	}
	if total := countAllOutboxEvents(t, db); total != 0 {
		t.Fatalf("提交失败回滚后不应留下事件 got=%d", total)
	}

	// 同一连接池在解除注入后仍可正常提交 说明回滚确实落到底层事务
	faultRepo2, _ := newPublishedEventCommitFaultRepository(t, db)
	if changed, err := faultRepo2.CompleteVideoProcessing(t.Context(), row.ID); err != nil || !changed {
		t.Fatalf("解除注入后应能正常完成 changed=%v err=%v", changed, err)
	}
	if events := listPublishedEvents(t, db, row.ID); len(events) != 1 {
		t.Fatalf("解除注入后应写入一条发布事件 got=%d", len(events))
	}
}

// 测试目标：验证发布事件开关关闭时保持既有只更新状态的行为
// 预期效果：状态转为 published 但 outbox 不产生任何 video.published 事件
func TestPublishedEventDisabledKeepsLegacyCompletion(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	if repo.publishedEvents {
		t.Fatal("默认构造的仓储不应开启发布事件")
	}
	row := seedProcessingVideoRow(t, repo, 1, "开关关闭视频")
	before := readPublishedEventVideo(t, db, row.ID)

	changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID)
	if err != nil {
		t.Fatalf("关闭开关时完成处理不应返回错误 got=%v", err)
	}
	if !changed {
		t.Fatal("关闭开关时仍应报告状态变更")
	}

	after := readPublishedEventVideo(t, db, row.ID)
	if after.Status != VideoStatusPublished {
		t.Fatalf("视频状态应为 published got=%s", after.Status)
	}
	if after.PublishedAt == nil || before.PublishedAt == nil {
		t.Fatal("published_at 不应为空")
	}
	if !before.PublishedAt.Equal(*after.PublishedAt) {
		t.Fatalf("关闭开关时不应改写 published_at before=%v after=%v", *before.PublishedAt, *after.PublishedAt)
	}
	if total := countAllOutboxEvents(t, db); total != 0 {
		t.Fatalf("关闭开关时不应产生任何事件 got=%d", total)
	}
}

// readPublishedEventVideo 读取视频当前落库状态
// 测试目标：为用例提供真实数据库中的状态快照
// 预期效果：返回该视频行的状态与发布时间
func readPublishedEventVideo(t *testing.T, db *gorm.DB, videoID uint) Video {
	t.Helper()
	var row Video
	if err := db.First(&row, "id = ?", videoID).Error; err != nil {
		t.Fatalf("读取视频 %d 失败: %v", videoID, err)
	}
	return row
}

// listPublishedEvents 列出指定视频的发布事件
// 测试目标：只统计 event_type 为 video.published 的事件
// 预期效果：按创建顺序返回该视频的发布事件
func listPublishedEvents(t *testing.T, db *gorm.DB, videoID uint) []OutboxEvent {
	t.Helper()
	var events []OutboxEvent
	if err := db.Where("video_id = ? AND event_type = ?", videoID, VideoPublishedEventType).
		Order("id ASC").Find(&events).Error; err != nil {
		t.Fatalf("读取视频 %d 的发布事件失败: %v", videoID, err)
	}
	return events
}

// countAllOutboxEvents 统计 outbox 全表事件数
// 测试目标：判断是否有多余事件残留
// 预期效果：返回 video_outbox_events 的当前行数
func countAllOutboxEvents(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var total int64
	if err := db.Model(&OutboxEvent{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 outbox 事件失败: %v", err)
	}
	return total
}

// publishedEventCreateFault 是写入发布事件时的插入故障注入器
// 测试目标：让针对目标表的 Create 语句在回调阶段直接失败
// 预期效果：武装后命中目标表的插入返回注入错误 其余语句不受影响
type publishedEventCreateFault struct {
	mu    sync.Mutex
	table string
	err   error
	hits  int
}

// inject 是注册到 Create 回调链上的处理函数
// 测试目标：在 INSERT 执行前按表名短路语句
// 预期效果：命中目标表时把注入错误写入语句错误并累计命中次数
func (f *publishedEventCreateFault) inject(tx *gorm.DB) {
	f.mu.Lock()
	table, err := f.table, f.err
	if err != nil && tx.Statement != nil && tx.Statement.Table == table {
		f.hits++
	}
	f.mu.Unlock()
	if err == nil || tx.Statement == nil || tx.Statement.Table != table {
		return
	}
	tx.AddError(err)
}

// arm 武装故障
// 测试目标：让下一次命中目标表的插入失败
// 预期效果：目标表匹配且错误非空
func (f *publishedEventCreateFault) arm(table string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.table = table
	f.err = err
}

// disarm 解除故障
// 测试目标：恢复目标表的正常写入
// 预期效果：后续插入不再注入错误
func (f *publishedEventCreateFault) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = nil
}

// hit 报告注入点是否被触发
// 测试目标：确认用例确实走到了目标语句
// 预期效果：命中过至少一次返回 true
func (f *publishedEventCreateFault) hit() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits > 0
}

const publishedEventCreateFaultCallback = "gofeed:published_event_create_fault"

// registerPublishedEventCreateFault 在 Create 流程上注册插入故障注入
// 测试目标：为用例提供可武装的插入失败能力
// 预期效果：回调在 gorm:create 之前执行 测试结束后自动移除
func registerPublishedEventCreateFault(t *testing.T, db *gorm.DB) *publishedEventCreateFault {
	t.Helper()
	fault := &publishedEventCreateFault{}
	if err := db.Callback().Create().Before("gorm:create").
		Register(publishedEventCreateFaultCallback, fault.inject); err != nil {
		t.Fatalf("注册插入故障回调失败: %v", err)
	}
	t.Cleanup(func() {
		fault.disarm()
		if err := db.Callback().Create().Remove(publishedEventCreateFaultCallback); err != nil {
			t.Errorf("移除插入故障回调失败: %v", err)
		}
	})
	return fault
}

// publishedEventCommitFault 是事务提交故障注入器
// 测试目标：在连接池层拦截事务开启并在提交时注入错误
// 预期效果：武装后真实事务被回滚且返回注入错误 未武装时提交照常
type publishedEventCommitFault struct {
	mu       sync.Mutex
	pool     *sql.DB
	injected error
	begins   int
	commits  int
}

// PrepareContext 委托底层连接池
// 测试目标：保持非事务语句可用
// 预期效果：直接使用底层预处理语句
func (f *publishedEventCommitFault) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return f.pool.PrepareContext(ctx, query)
}

// ExecContext 委托底层连接池
// 测试目标：保持非事务语句可用
// 预期效果：直接执行底层语句
func (f *publishedEventCommitFault) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return f.pool.ExecContext(ctx, query, args...)
}

// QueryContext 委托底层连接池
// 测试目标：保持非事务查询可用
// 预期效果：返回底层查询结果
func (f *publishedEventCommitFault) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return f.pool.QueryContext(ctx, query, args...)
}

// QueryRowContext 委托底层连接池
// 测试目标：保持非事务单行查询可用
// 预期效果：返回底层单行结果
func (f *publishedEventCommitFault) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return f.pool.QueryRowContext(ctx, query, args...)
}

// BeginTx 开启真实事务并包装提交
// 测试目标：让事务提交点可注入错误
// 预期效果：返回包装后的连接池并累计开启次数
func (f *publishedEventCommitFault) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	real, err := f.pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.begins++
	f.mu.Unlock()
	return &publishedEventCommitFaultTx{Tx: real, fault: f}, nil
}

// arm 武装提交故障
// 测试目标：让下一次事务提交失败
// 预期效果：提交时先回滚再返回注入错误
func (f *publishedEventCommitFault) arm(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injected = err
}

// disarm 解除提交故障
// 测试目标：恢复事务正常提交
// 预期效果：后续提交直接落地
func (f *publishedEventCommitFault) disarm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injected = nil
}

// counters 报告事务开启与提交次数
// 测试目标：确认注错点确实被走到
// 预期效果：返回开启次数与提交次数
func (f *publishedEventCommitFault) counters() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.begins, f.commits
}

// publishedEventCommitFaultTx 包装真实事务以便注入提交错误
// 测试目标：在事务提交点注入错误并回滚
// 预期效果：武装时回滚真实事务并返回注入错误 未武装时正常提交
type publishedEventCommitFaultTx struct {
	*sql.Tx
	fault *publishedEventCommitFault
}

// Commit 按武装状态提交或注入失败
// 测试目标：模拟提交阶段失败
// 预期效果：武装时先回滚真实事务再返回注入错误 否则提交真实事务
func (tx *publishedEventCommitFaultTx) Commit() error {
	tx.fault.mu.Lock()
	injected := tx.fault.injected
	tx.fault.commits++
	tx.fault.mu.Unlock()
	if injected != nil {
		_ = tx.Tx.Rollback()
		return injected
	}
	return tx.Tx.Commit()
}

// Rollback 回滚真实事务
// 测试目标：保留事务回滚能力
// 预期效果：返回底层回滚结果
func (tx *publishedEventCommitFaultTx) Rollback() error {
	return tx.Tx.Rollback()
}

// newPublishedEventCommitFaultRepository 构造提交点可注入错误的仓储
// 测试目标：让 CompleteVideoProcessing 走真实事务并在提交处失败
// 预期效果：返回使用真实连接池的仓储与对应故障注入器
func newPublishedEventCommitFaultRepository(t *testing.T, db *gorm.DB) (*Repository, *publishedEventCommitFault) {
	t.Helper()
	pool, err := db.DB()
	if err != nil {
		t.Fatalf("读取底层连接池失败: %v", err)
	}
	fault := &publishedEventCommitFault{pool: pool}
	session := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	session.Statement.ConnPool = fault
	return NewRepository(session, WithPublishedEvents(true)), fault
}
