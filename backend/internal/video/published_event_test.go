package video

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"gofeed/internal/testutil"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 以下用例验收「视频处理完成时同事务写入 video.published 发布事件」这一能力
// 覆盖正常流转、CAS 零行、拒绝分支、并发竞争、插入失败回滚、提交失败回滚与开关关闭七类场景

// 测试目标：验证首次完成处理时在状态流转成功的前提下写入一条新的发布事件
// 预期效果：状态转为 published published_at 保持原值 新增一条 pending 的 video.published 事件且标识是不同于原处理事件的新 UUID
func TestPublishedEventCreatedOnFirstCompletion(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))
	row := seedProcessingVideoRow(t, repo, 1, "首次完成视频")
	if row == nil || row.ID == 0 {
		t.Fatal("前置视频未创建成功")
	}

	before := readPublishedEventVideo(t, db, row.ID)
	if before.Status != VideoStatusProcessing || before.PublishedAt == nil {
		t.Fatalf("前置状态错误 got=%+v", before)
	}

	processEvent := seedOutboxEvent(t, db, row.ID, uuid.NewString(), OutboxEventStatusPending)

	changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID)
	if err != nil {
		t.Fatalf("首次完成处理不应返回错误 got=%v", err)
	}
	if !changed {
		t.Fatal("首次完成处理应报告状态变更")
	}

	after := readPublishedEventVideo(t, db, row.ID)
	if after.Status != VideoStatusPublished {
		t.Fatalf("视频状态应为 published got=%s", after.Status)
	}
	if after.PublishedAt == nil {
		t.Fatal("published_at 不应为空")
	}
	if !before.PublishedAt.Equal(*after.PublishedAt) {
		t.Fatalf("完成处理不应改写 published_at before=%v after=%v", *before.PublishedAt, *after.PublishedAt)
	}

	events := listPublishedEvents(t, db, row.ID)
	if len(events) != 1 {
		t.Fatalf("应恰好新增一条发布事件 got=%d", len(events))
	}
	got := events[0]
	if got.VideoID != row.ID {
		t.Fatalf("发布事件应指向该视频 want=%d got=%d", row.ID, got.VideoID)
	}
	if got.EventType != VideoPublishedEventType {
		t.Fatalf("发布事件类型错误 want=%s got=%s", VideoPublishedEventType, got.EventType)
	}
	if got.Status != OutboxEventStatusPending {
		t.Fatalf("发布事件应为待投递 want=%s got=%s", OutboxEventStatusPending, got.Status)
	}

	newID, parseErr := uuid.Parse(got.EventID)
	if parseErr != nil {
		t.Fatalf("发布事件标识应为合法 UUID got=%q err=%v", got.EventID, parseErr)
	}
	oldID, parseErr := uuid.Parse(processEvent.EventID)
	if parseErr != nil {
		t.Fatalf("原处理事件标识应为合法 UUID got=%q err=%v", processEvent.EventID, parseErr)
	}
	if newID == oldID {
		t.Fatalf("发布事件应使用新标识不得复用原处理事件标识 got=%q", got.EventID)
	}
}

// 测试目标：验证同一视频重复完成处理时不会重复写入发布事件
// 预期效果：第二次返回未变更且不报错 发布事件仍为一条 标识与首次一致 outbox 全表仅一条事件
func TestPublishedEventNotDuplicatedOnRepeatCompletion(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))
	row := seedProcessingVideoRow(t, repo, 1, "重复完成视频")

	if changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID); err != nil || !changed {
		t.Fatalf("首次完成处理应发生变更 changed=%v err=%v", changed, err)
	}
	first := listPublishedEvents(t, db, row.ID)
	if len(first) != 1 {
		t.Fatalf("首次应写入一条发布事件 got=%d", len(first))
	}

	changed, err := repo.CompleteVideoProcessing(t.Context(), row.ID)
	if err != nil {
		t.Fatalf("重复完成处理不应返回错误 got=%v", err)
	}
	if changed {
		t.Fatal("已发布视频不应再次报告状态变更")
	}

	second := listPublishedEvents(t, db, row.ID)
	if len(second) != 1 {
		t.Fatalf("重复完成处理不应新增发布事件 got=%d", len(second))
	}
	if second[0].EventID != first[0].EventID {
		t.Fatalf("重复完成处理不应改写事件标识 want=%s got=%s", first[0].EventID, second[0].EventID)
	}
	if total := countAllOutboxEvents(t, db); total != 1 {
		t.Fatalf("outbox 事件总数应保持一条 got=%d", total)
	}
}

// 测试目标：验证状态条件更新匹配零行时不为该视频写入发布事件
// 预期效果：视频不存在 状态为草稿 状态为已发布三种场景都返回未变更且不报错 状态不被改写 outbox 保持为空
func TestPublishedEventSkippedWhenCASMatchesNoRow(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))

	draft := seedVideo(t, repo, 1, "草稿视频", VideoStatusDraft, baseTime)
	published := seedVideo(t, repo, 1, "已发布视频", VideoStatusPublished, baseTime)

	cases := []struct {
		name       string
		videoID    uint
		wantStatus string
	}{
		{name: "视频不存在", videoID: published.ID + 100000, wantStatus: ""},
		{name: "状态为草稿", videoID: draft.ID, wantStatus: VideoStatusDraft},
		{name: "状态为已发布", videoID: published.ID, wantStatus: VideoStatusPublished},
	}

	for _, tc := range cases {
		changed, err := repo.CompleteVideoProcessing(t.Context(), tc.videoID)
		if err != nil {
			t.Fatalf("%s 不应返回错误 got=%v", tc.name, err)
		}
		if changed {
			t.Fatalf("%s 匹配零行不应报告状态变更", tc.name)
		}
		if tc.wantStatus == "" {
			continue
		}
		got := readPublishedEventVideo(t, db, tc.videoID)
		if got.Status != tc.wantStatus {
			t.Fatalf("%s 状态不应被改写 want=%s got=%s", tc.name, tc.wantStatus, got.Status)
		}
	}

	if total := countAllOutboxEvents(t, db); total != 0 {
		t.Fatalf("匹配零行不应产生任何事件 got=%d", total)
	}
}

// 测试目标：验证拒绝分支不会写入发布事件
// 预期效果：视频转为 rejected 后再次完成处理也不发生变更 outbox 始终为空
func TestPublishedEventNotCreatedOnRejection(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db, WithPublishedEvents(true))
	row := seedProcessingVideoRow(t, repo, 1, "被拒视频")

	rejected, err := repo.RejectVideoProcessing(t.Context(), row.ID, "媒体校验失败")
	if err != nil {
		t.Fatalf("拒绝处理不应返回错误 got=%v", err)
	}
	if !rejected {
		t.Fatal("拒绝处理应报告状态变更")
	}
	if got := readPublishedEventVideo(t, db, row.ID); got.Status != VideoStatusRejected {
		t.Fatalf("视频状态应为 rejected got=%s", got.Status)
	}

	if changed, completeErr := repo.CompleteVideoProcessing(t.Context(), row.ID); completeErr != nil || changed {
		t.Fatalf("已拒绝视频不应被完成 changed=%v err=%v", changed, completeErr)
	}
	if total := countAllOutboxEvents(t, db); total != 0 {
		t.Fatalf("拒绝分支不应产生任何事件 got=%d", total)
	}
}

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
