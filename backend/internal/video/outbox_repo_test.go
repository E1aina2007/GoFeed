package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gofeed/internal/testutil"

	"gorm.io/gorm"
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

// 测试目标：验证 claim 把到期 pending 事件租约为 publishing 并递增 attempt
// 预期效果：事件状态、attempt、last_attempt_at、locked_until 落库，返回视频快照且 HasVideo 为真
func TestClaimPendingOutboxEventsAcquiresLease(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "租约视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-lease-1", OutboxEventStatusPending)

	dispatches, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("claim 失败: %v", err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("应 claim 一条事件 got=%d", len(dispatches))
	}
	got := dispatches[0]
	if got.Event.ID != event.ID || got.Video.ID != row.ID {
		t.Fatalf("claim 结果错误 got=%+v", got)
	}
	if !got.HasVideo {
		t.Fatalf("存在视频快照时 HasVideo 应为真 got=%+v", got)
	}
	if got.Event.Status != OutboxEventStatusPublishing {
		t.Fatalf("状态应为 publishing got=%s", got.Event.Status)
	}
	if got.Event.Attempt != 1 {
		t.Fatalf("attempt 应递增为一 got=%d", got.Event.Attempt)
	}
	if got.Event.NextAttemptAt != nil {
		t.Fatalf("claim 应清空 next_attempt_at got=%v", got.Event.NextAttemptAt)
	}
	if got.Event.LastAttemptAt == nil || got.Event.LockedUntil == nil {
		t.Fatalf("租约字段应写入 got=%+v", got.Event)
	}
}

// 测试目标：验证持有有效租约的事件不会被第二个 relay 重复 claim
// 预期效果：第二次 claim 返回空，事件仍属于首个持有者
func TestClaimPendingOutboxEventsExcludesLiveLease(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "互斥视频")
	seedOutboxEvent(t, db, row.ID, "evt-lease-live", OutboxEventStatusPending)

	first, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("首次 claim 失败 got=%d err=%v", len(first), err)
	}
	second, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("第二次 claim 失败: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("有效租约不应被重复 claim got=%d", len(second))
	}
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

// 测试目标：验证缺少视频快照的事件仍被租用但不返回快照
// 预期效果：软删除视频后 HasVideo 为假且视频标识为空，事件保持 publishing 等待释放
func TestClaimPendingOutboxEventsWithoutVideoSnapshot(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "软删除视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-no-snapshot", OutboxEventStatusPending)
	if err := db.Delete(&Video{}, row.ID).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("缺少快照的事件仍应被 claim got=%d err=%v", len(claimed), err)
	}
	got := claimed[0]
	if got.HasVideo {
		t.Fatalf("缺少视频快照时 HasVideo 应为假 got=%+v", got)
	}
	if got.Video.ID != 0 {
		t.Fatalf("缺少视频快照时不应返回视频 got=%+v", got.Video)
	}
	if got.Event.Status != OutboxEventStatusPublishing || got.Event.LockedUntil == nil {
		t.Fatalf("缺少快照的事件应保持租用状态 got=%+v", got.Event)
	}

	// 测试目标：验证缺少快照的事件可被正常释放
	// 预期效果：释放后回到 pending 并安排下一次尝试
	released, err := repo.ReleaseOutboxRetry(context.Background(), got.Event.ID, got.Event.Attempt, 5*time.Minute, errors.New("video snapshot is missing"))
	if err != nil || !released {
		t.Fatalf("缺少快照的事件应可释放 released=%v err=%v", released, err)
	}
	var stored OutboxEvent
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if stored.Status != OutboxEventStatusPending || stored.NextAttemptAt == nil || stored.LockedUntil != nil {
		t.Fatalf("释放结果错误 got=%+v", stored)
	}
}

// 测试目标：验证物理删除视频行会级联删除 outbox 事件
// 预期效果：事件随外键级联移除，不会成为无视频的孤立 claim 目标
func TestClaimPendingOutboxEventsCascadesDeletedVideoEvent(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "级联视频")
	seedOutboxEvent(t, db, row.ID, "evt-cascade", OutboxEventStatusPending)

	if err := db.Exec("DELETE FROM videos WHERE id = ?", row.ID).Error; err != nil {
		t.Fatalf("物理删除视频失败: %v", err)
	}
	var remaining int64
	if err := db.Model(&OutboxEvent{}).Where("event_id = ?", "evt-cascade").Count(&remaining).Error; err != nil {
		t.Fatalf("统计事件失败: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("外键级联应删除事件 got=%d", remaining)
	}

	claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("claim 失败: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("级联删除后不应有可领取事件 got=%d", len(claimed))
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

// 测试目标：验证未到期的退避事件不会被 claim
// 预期效果：next_attempt_at 在未来时返回空，到期后可 claim
func TestClaimPendingOutboxEventsRespectsBackoff(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "退避视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-lease-backoff", OutboxEventStatusPending)

	if err := db.Model(&OutboxEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
		"next_attempt_at": gorm.Expr("TIMESTAMPADD(SECOND, 300, NOW(3))"),
	}).Error; err != nil {
		t.Fatalf("构造退避失败: %v", err)
	}
	blocked, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("claim 失败: %v", err)
	}
	if len(blocked) != 0 {
		t.Fatalf("退避未到期不应 claim got=%d", len(blocked))
	}

	if err := db.Model(&OutboxEvent{}).Where("id = ?", event.ID).Update("next_attempt_at", nil).Error; err != nil {
		t.Fatalf("清除退避失败: %v", err)
	}
	ready, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil || len(ready) != 1 {
		t.Fatalf("退避清除后应可 claim got=%d err=%v", len(ready), err)
	}
}

// 测试目标：验证发布成功后标记 dispatched 并清除租约
// 预期效果：状态为 dispatched、dispatched_at 落库、locked_until 清空
func TestMarkOutboxDispatchedClearsLease(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "派发视频")
	seedOutboxEvent(t, db, row.ID, "evt-mark", OutboxEventStatusPending)

	claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim 失败 got=%d err=%v", len(claimed), err)
	}
	marked, err := repo.MarkOutboxDispatched(context.Background(), claimed[0].Event.ID, claimed[0].Event.Attempt)
	if err != nil || !marked {
		t.Fatalf("标记失败 marked=%v err=%v", marked, err)
	}

	var stored OutboxEvent
	if err := db.First(&stored, claimed[0].Event.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if stored.Status != OutboxEventStatusDispatched || stored.DispatchedAt == nil || stored.LockedUntil != nil {
		t.Fatalf("标记结果错误 got=%+v", stored)
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

// 测试目标：验证发布失败释放时的非法退避被拒绝
// 预期效果：负退避返回 ErrInvalidOutboxLease 且不改变事件，正亚秒退避向上取整为一秒
func TestReleaseOutboxRetryRejectsNegativeBackoff(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "负退避视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-release-negative", OutboxEventStatusPending)

	claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim 失败 got=%d err=%v", len(claimed), err)
	}
	for _, backoff := range []time.Duration{-time.Nanosecond, -500 * time.Millisecond, -time.Second} {
		released, err := repo.ReleaseOutboxRetry(context.Background(), claimed[0].Event.ID, claimed[0].Event.Attempt, backoff, errors.New("negative backoff"))
		if !errors.Is(err, ErrInvalidOutboxLease) {
			t.Fatalf("退避 %v 应被拒绝 got=%v", backoff, err)
		}
		if released {
			t.Fatalf("退避 %v 不应释放事件", backoff)
		}
	}
	var stored OutboxEvent
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if stored.Status != OutboxEventStatusPublishing || stored.LockedUntil == nil {
		t.Fatalf("被拒绝的释放不应改变事件 got=%+v", stored)
	}

	// 测试目标：验证正亚秒退避被接受并向上取整
	// 预期效果：next_attempt_at 落在约一秒之后而不是当前时刻
	released, err := repo.ReleaseOutboxRetry(context.Background(), claimed[0].Event.ID, claimed[0].Event.Attempt, 300*time.Millisecond, errors.New("sub-second backoff"))
	if err != nil || !released {
		t.Fatalf("正亚秒退避应被接受 released=%v err=%v", released, err)
	}
	var remainingMicros int64
	if err := db.Raw(
		"SELECT TIMESTAMPDIFF(MICROSECOND, NOW(3), next_attempt_at) FROM video_outbox_events WHERE id = ?",
		event.ID,
	).Scan(&remainingMicros).Error; err != nil {
		t.Fatalf("读取剩余退避失败: %v", err)
	}
	if remainingMicros <= 300000 || remainingMicros > 1300000 {
		t.Fatalf("亚秒退避应向上取整为一秒 got=%d微秒", remainingMicros)
	}
}

// 测试目标：验证非法租约时长被拒绝且不产生副作用
// 预期效果：零、负亚秒与负整秒租约返回 ErrInvalidOutboxLease，事件保持原状态
func TestClaimPendingOutboxEventsRejectsInvalidLease(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "非法租约视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-lease-invalid", OutboxEventStatusPending)

	for _, lease := range []time.Duration{0, -time.Nanosecond, -500 * time.Millisecond, -time.Second} {
		if _, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, lease); !errors.Is(err, ErrInvalidOutboxLease) {
			t.Fatalf("租约 %v 应被拒绝 got=%v", lease, err)
		}
	}
	var stored OutboxEvent
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if stored.Status != OutboxEventStatusPending || stored.Attempt != 0 {
		t.Fatalf("非法租约不应产生副作用 got=%+v", stored)
	}
}

// 测试目标：验证正亚秒租约按秒向上取整
// 预期效果：亚秒租约被接受且至少保护一秒，租约内事件不可被再次领取
func TestClaimPendingOutboxEventsCeilsSubSecondLease(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "亚秒租约视频")
	event := seedOutboxEvent(t, db, row.ID, "evt-lease-subsecond", OutboxEventStatusPending)

	claimed, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, 500*time.Millisecond)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("正亚秒租约应被接受 got=%d err=%v", len(claimed), err)
	}
	var remainingMicros int64
	if err := db.Raw(
		"SELECT TIMESTAMPDIFF(MICROSECOND, NOW(3), locked_until) FROM video_outbox_events WHERE id = ?",
		event.ID,
	).Scan(&remainingMicros).Error; err != nil {
		t.Fatalf("读取剩余租约失败: %v", err)
	}
	if remainingMicros <= 500000 || remainingMicros > 1500000 {
		t.Fatalf("亚秒租约应向上取整为一秒 got=%d微秒", remainingMicros)
	}

	// 测试目标：验证取整后的租约仍然生效
	// 预期效果：租约内第二次 claim 返回空
	again, err := repo.ClaimPendingOutboxEvents(context.Background(), 10, time.Millisecond)
	if err != nil {
		t.Fatalf("租约内重复 claim 失败: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("租约内事件不应被再次领取 got=%d", len(again))
	}
}

// 测试目标：验证拒绝原因按 255 字节安全截断并保持有效 UTF-8
// 预期效果：超长多字节原因被截断到列宽以内且不留下半个字符
func TestRejectVideoProcessingTruncatesReasonSafely(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "拒绝截断视频")
	// 测试目标：构造截断边界落在多字节字符中间的原因
	// 预期效果：截断结果必须丢弃半个字符
	reason := "a" + strings.Repeat("故障", 400)
	rejected, err := repo.RejectVideoProcessing(context.Background(), row.ID, reason)
	if err != nil || !rejected {
		t.Fatalf("拒绝视频失败 rejected=%v err=%v", rejected, err)
	}

	var stored Video
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if stored.Status != VideoStatusRejected {
		t.Fatalf("视频应为 rejected got=%s", stored.Status)
	}
	if size := len([]byte(stored.RejectedReason)); size > 255 || size < 250 {
		t.Fatalf("拒绝原因应截断到 255 字节以内且用满预算 got_len=%d", size)
	}
	if !utf8.ValidString(stored.RejectedReason) {
		t.Fatalf("拒绝原因必须保持有效 UTF-8 got=%q", stored.RejectedReason)
	}
	if !strings.HasPrefix(reason, stored.RejectedReason) {
		t.Fatalf("拒绝原因应是原始信息的前缀 got=%q", stored.RejectedReason)
	}
}

// 测试目标：验证重复拒绝已流转视频不产生变更
// 预期效果：视频离开 processing 后拒绝返回未变更
func TestRejectVideoProcessingIgnoresSettledVideo(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	row := seedProcessingVideoRow(t, repo, 1, "已流转视频")
	if _, err := repo.RejectVideoProcessing(context.Background(), row.ID, "首次拒绝"); err != nil {
		t.Fatalf("首次拒绝失败: %v", err)
	}

	rejected, err := repo.RejectVideoProcessing(context.Background(), row.ID, "重复拒绝")
	if err != nil {
		t.Fatalf("重复拒绝失败: %v", err)
	}
	if rejected {
		t.Fatal("已流转视频不应再次变更状态")
	}
	var stored Video
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatalf("读取视频失败: %v", err)
	}
	if stored.RejectedReason != "首次拒绝" {
		t.Fatalf("拒绝原因不应被覆盖 got=%q", stored.RejectedReason)
	}
}

// 测试目标：验证公共截断函数按字节上限保持 UTF-8 完整
// 预期效果：结果始终是原值前缀且有效，边界落在字符中间时丢弃该字符
func TestTruncateUTF8BytesKeepsValidEncoding(t *testing.T) {
	cases := []struct {
		name  string
		value string
		limit int
	}{
		{name: "上限为零", value: "abc", limit: 0},
		{name: "上限为负", value: "abc", limit: -1},
		{name: "恰好命中边界", value: strings.Repeat("故", 3), limit: 9},
		{name: "边界落在字符中间", value: "故障", limit: 4},
		{name: "短于上限", value: "故障", limit: 255},
		{name: "空字符串", value: "", limit: 255},
	}
	for _, tc := range cases {
		got := truncateUTF8Bytes(tc.value, tc.limit)
		if len(got) > tc.limit && tc.limit > 0 {
			t.Fatalf("%s 超出字节上限 got=%d limit=%d", tc.name, len(got), tc.limit)
		}
		if tc.limit <= 0 && got != "" {
			t.Fatalf("%s 应返回空字符串 got=%q", tc.name, got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("%s 结果必须保持有效 UTF-8 got=%q", tc.name, got)
		}
		if !strings.HasPrefix(tc.value, got) {
			t.Fatalf("%s 结果应是原值前缀 got=%q", tc.name, got)
		}
	}
	if got := truncateUTF8Bytes("故障", 4); got != "故" {
		t.Fatalf("边界落在字符中间应丢弃该字符 got=%q", got)
	}
}

// 测试目标：验证公共取整函数把时长向上取整为秒
// 预期效果：非正时长返回零，正亚秒返回一，整秒与超整秒按秒数向上取整
func TestDurationSecondsCeil(t *testing.T) {
	cases := map[time.Duration]int64{
		-time.Second:                     0,
		-time.Nanosecond:                 0,
		0:                                0,
		time.Nanosecond:                  1,
		time.Millisecond:                 1,
		500 * time.Millisecond:           1,
		time.Second:                      1,
		time.Second + time.Nanosecond:    2,
		90 * time.Second:                 90,
		5*time.Minute + time.Millisecond: 301,
	}
	for duration, want := range cases {
		if got := durationSecondsCeil(duration); got != want {
			t.Fatalf("取整错误 duration=%v got=%d want=%d", duration, got, want)
		}
	}
}
