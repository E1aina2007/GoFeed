package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gofeed/internal/testutil"

	"gorm.io/gorm"
)

// 测试目标：固定仓储测试的基准时间
// 预期效果：统一使用本地时区，避免读写往返产生时区断言差异
var baseTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)

func timePtr(value time.Time) *time.Time {
	return &value
}

// 测试目标：配置视频仓储集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：构造字段齐全的视频测试数据
// 预期效果：调用方可指定作者、标题、状态和发布时间
func newVideoFixture(authorID uint, title, status string, publishedAt time.Time) *Video {
	return &Video{
		AuthorID:          authorID,
		Title:             title,
		Description:       "集成测试描述",
		PlayURL:           "/static/videos/1/a.mp4",
		PlayFileName:      "a.mp4",
		PlayOriginalName:  "原始视频.mp4",
		CoverURL:          "/static/covers/1/a.webp",
		CoverFileName:     "a.webp",
		CoverOriginalName: "封面.webp",
		Status:            status,
		PublishedAt:       timePtr(publishedAt),
	}
}

// 测试目标：通过仓储写入一条视频测试数据
// 预期效果：返回已回填视频标识的实体
func seedVideo(t *testing.T, repo *Repository, authorID uint, title, status string, publishedAt time.Time) *Video {
	t.Helper()
	v := newVideoFixture(authorID, title, status, publishedAt)
	if err := repo.Create(context.Background(), v); err != nil {
		t.Fatalf("写入视频 %q 失败: %v", title, err)
	}
	return v
}

// 测试目标：设置视频软删除时间以构造宽限期边界
// 预期效果：测试可准确覆盖到期、边界和未到期三种记录
func setVideoDeletedAt(t *testing.T, db *gorm.DB, id uint, at time.Time) {
	t.Helper()
	if err := db.Exec("UPDATE videos SET deleted_at = ? WHERE id = ?", at, id).Error; err != nil {
		t.Fatalf("设置视频 deleted_at 失败: %v", err)
	}
}

// 测试目标：验证视频仓储创建并按标识读取视频
// 预期效果：创建操作回填标识和时间，读取结果与写入字段一致
func TestRepositoryCreateAndGetByID(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	v := newVideoFixture(1, "往返", VideoStatusPublished, baseTime)
	if err := repo.Create(ctx, v); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v.ID == 0 {
		t.Fatal("Create 未回填 ID")
	}
	if v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() {
		t.Fatalf("Create 未回填时间戳 created=%v updated=%v", v.CreatedAt, v.UpdatedAt)
	}

	got, err := repo.GetByID(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != v.ID || got.AuthorID != v.AuthorID || got.Title != v.Title || got.Description != v.Description {
		t.Fatalf("基础字段读回不一致 got=%+v want=%+v", got, v)
	}
	if got.PlayFileName != v.PlayFileName || got.PlayOriginalName != v.PlayOriginalName ||
		got.CoverFileName != v.CoverFileName || got.CoverOriginalName != v.CoverOriginalName {
		t.Fatalf("媒体文件名读回不一致 got=%+v", got)
	}
	if got.Status != v.Status {
		t.Fatalf("状态读回不一致 got=%s want=%s", got.Status, v.Status)
	}
	if got.PublishedAt == nil || v.PublishedAt == nil || !got.PublishedAt.Equal(*v.PublishedAt) {
		t.Fatalf("PublishedAt 读回不一致 got=%v want=%v", got.PublishedAt, v.PublishedAt)
	}
}

// 测试目标：验证公开读取仅返回已发布视频
// 预期效果：草稿无法公开读取，通用读取仍可读取草稿
func TestRepositoryGetPublishedByIDFiltersStatus(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	published := seedVideo(t, repo, 1, "已发布", VideoStatusPublished, baseTime)
	draft := seedVideo(t, repo, 1, "草稿", VideoStatusDraft, baseTime.Add(time.Minute))

	if _, err := repo.GetPublishedByID(ctx, draft.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("草稿不应通过公开读取到, err=%v", err)
	}
	got, err := repo.GetPublishedByID(ctx, published.ID)
	if err != nil {
		t.Fatalf("已发布视频应可读: %v", err)
	}
	if got.ID != published.ID {
		t.Fatalf("got id=%d want=%d", got.ID, published.ID)
	}

	// 测试目标：验证管理侧读取不限制视频状态
	// 预期效果：草稿可通过通用读取接口返回
	raw, err := repo.GetByID(ctx, draft.ID)
	if err != nil {
		t.Fatalf("GetByID 读草稿失败: %v", err)
	}
	if raw.Status != VideoStatusDraft {
		t.Fatalf("草稿状态读回错误 got=%s", raw.Status)
	}
}

// 测试目标：验证所有公开查询都要求完整的发布状态和媒体快照
// 预期效果：状态异常、软删除、空发布时间或任一媒体字段为空的记录均不可见
func TestRepositoryPublicQueriesRequireCompleteVideo(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	valid := seedVideo(t, repo, 1, "valid", VideoStatusPublished, baseTime)
	otherAuthor := seedVideo(t, repo, 2, "other-author", VideoStatusPublished, baseTime.Add(time.Minute))
	invalidIDs := make(map[uint]bool)

	for _, status := range []string{VideoStatusDraft, VideoStatusPurging, VideoStatusProcessing, VideoStatusRejected} {
		item := newVideoFixture(1, "status-"+status, status, baseTime.Add(2*time.Minute))
		if err := repo.Create(ctx, item); err != nil {
			t.Fatalf("创建 %s 测试记录失败: %v", status, err)
		}
		invalidIDs[item.ID] = true
	}

	withoutPublishedAt := newVideoFixture(1, "missing-published-at", VideoStatusPublished, baseTime)
	withoutPublishedAt.PublishedAt = nil
	if err := repo.Create(ctx, withoutPublishedAt); err != nil {
		t.Fatalf("创建空发布时间记录失败: %v", err)
	}
	invalidIDs[withoutPublishedAt.ID] = true

	for _, missing := range []struct {
		name  string
		clear func(*Video)
	}{
		{name: "play_url", clear: func(item *Video) { item.PlayURL = "" }},
		{name: "play_file_name", clear: func(item *Video) { item.PlayFileName = "" }},
		{name: "play_original_name", clear: func(item *Video) { item.PlayOriginalName = "" }},
		{name: "cover_url", clear: func(item *Video) { item.CoverURL = "" }},
		{name: "cover_file_name", clear: func(item *Video) { item.CoverFileName = "" }},
		{name: "cover_original_name", clear: func(item *Video) { item.CoverOriginalName = "" }},
	} {
		item := newVideoFixture(1, "missing-"+missing.name, VideoStatusPublished, baseTime)
		missing.clear(item)
		if err := repo.Create(ctx, item); err != nil {
			t.Fatalf("创建缺少 %s 的记录失败: %v", missing.name, err)
		}
		invalidIDs[item.ID] = true
	}

	deleted := seedVideo(t, repo, 1, "deleted", VideoStatusPublished, baseTime)
	if err := db.Delete(&Video{}, deleted.ID).Error; err != nil {
		t.Fatalf("软删除测试记录失败: %v", err)
	}
	invalidIDs[deleted.ID] = true

	for id := range invalidIDs {
		if _, err := repo.GetPublishedByID(ctx, id); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Errorf("不完整视频 id=%d 不应通过详情查询, err=%v", id, err)
		}
	}

	all, err := repo.GetPublishedVideoList(ctx, 0, nil, 100)
	if err != nil {
		t.Fatalf("查询公开列表失败: %v", err)
	}
	if len(all) != 2 || all[0].ID != otherAuthor.ID || all[1].ID != valid.ID {
		t.Fatalf("公开列表应只包含两条完整视频, got=%+v", all)
	}

	mine, err := repo.GetAuthorVideoList(ctx, 1, nil, 100)
	if err != nil {
		t.Fatalf("查询作者公开列表失败: %v", err)
	}
	if len(mine) != 1 || mine[0].ID != valid.ID {
		t.Fatalf("作者列表应排除全部不完整记录, got=%+v", mine)
	}

	count, err := repo.GetPublishedVideoCountByAuthor(ctx, 1)
	if err != nil {
		t.Fatalf("统计作者公开视频失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("作者统计应只计入完整公开视频, got=%d", count)
	}
	otherCount, err := repo.GetPublishedVideoCountByAuthor(ctx, 2)
	if err != nil || otherCount != 1 {
		t.Fatalf("其他作者统计错误 count=%d err=%v", otherCount, err)
	}
}

// 测试目标：验证零值视频标识的读取边界
// 预期效果：通用读取和公开读取均返回记录不存在错误
func TestRepositoryGetByIDZero(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, 0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("id=0 应返回 not found, err=%v", err)
	}
	if _, err := repo.GetPublishedByID(ctx, 0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("公开读 id=0 应返回 not found, err=%v", err)
	}
}

// 测试目标：验证草稿媒体归属、完整性和发布状态转换均由仓储原子约束
// 预期效果：跨作者或重复绑定被拒绝，不完整草稿不能发布，完整草稿写入实际发布时间
func TestRepositoryDraftMediaAndPublish(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	draft := &Video{AuthorID: 1, Title: "草稿", Status: VideoStatusDraft}
	if err := repo.Create(ctx, draft); err != nil {
		t.Fatalf("创建草稿失败: %v", err)
	}

	videoFile := SavedFile{PublicURL: "/static/videos/1/20260810/a.mp4", FileName: "a.mp4"}
	coverFile := SavedFile{PublicURL: "/static/covers/1/20260810/c.png", FileName: "c.png"}
	if err := repo.UpdateDraftMedia(ctx, draft.ID, 2, MediaVideo, videoFile, "视频.mp4"); !errors.Is(err, ErrNotAuthor) {
		t.Fatalf("跨作者绑定未被拒绝 error=%v", err)
	}
	if err := repo.UpdateDraftMedia(ctx, draft.ID, 1, MediaVideo, videoFile, "视频.mp4"); err != nil {
		t.Fatalf("绑定视频失败: %v", err)
	}
	if err := repo.UpdateDraftMedia(ctx, draft.ID, 1, MediaVideo, videoFile, "视频.mp4"); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("重复绑定未被拒绝 error=%v", err)
	}
	if _, err := repo.UpdateDraftPublication(ctx, draft.ID, 1); !errors.Is(err, ErrDraftIncomplete) {
		t.Fatalf("不完整草稿未被拒绝 error=%v", err)
	}
	if err := repo.UpdateDraftMedia(ctx, draft.ID, 1, MediaCover, coverFile, "封面.png"); err != nil {
		t.Fatalf("绑定封面失败: %v", err)
	}

	published, err := repo.UpdateDraftPublication(ctx, draft.ID, 1)
	if err != nil {
		t.Fatalf("发布草稿失败: %v", err)
	}
	if published.Status != VideoStatusProcessing || published.PublishedAt == nil || published.PlayURL != videoFile.PublicURL || published.CoverURL != coverFile.PublicURL {
		t.Fatalf("发布结果错误 got=%+v", published)
	}
	// 发布事务应原子写入待派发处理事件
	var events []OutboxEvent
	if err := db.Where("video_id = ?", draft.ID).Find(&events).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	if len(events) != 1 || events[0].EventType != VideoProcessEventType || events[0].Status != OutboxEventStatusPending ||
		events[0].EventID == "" || events[0].Attempt != 0 || events[0].DispatchedAt != nil {
		t.Fatalf("outbox 事件写入错误 got=%+v", events)
	}
	if err := repo.UpdateDraftMedia(ctx, draft.ID, 1, MediaCover, coverFile, "封面.png"); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("发布后写入媒体未被拒绝 error=%v", err)
	}
	// processing 状态下重复发布应被条件更新拒绝
	if _, err := repo.UpdateDraftPublication(ctx, draft.ID, 1); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("重复发布未被拒绝 error=%v", err)
	}
}

// 测试目标：验证仓储原子将草稿转入可由 sweeper 接管的 purging 状态
// 预期效果：重试保持幂等，清扫前不删除媒体，其他作者和已发布视频不能触发转换
func TestRepositoryDiscardDraft(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	draft := newVideoFixture(1, "待丢弃草稿", VideoStatusDraft, baseTime)
	draft.PublishedAt = nil
	if err := repo.Create(ctx, draft); err != nil {
		t.Fatalf("创建草稿失败 error=%v", err)
	}

	discarded, err := repo.UpdateDraftDiscard(ctx, draft.ID, 1)
	if err != nil {
		t.Fatalf("丢弃草稿失败 error=%v", err)
	}
	if discarded.Status != VideoStatusPurging || discarded.PurgeToken != nil || discarded.PurgeLeaseUntil != nil || discarded.PlayPurgedAt != nil || discarded.CoverPurgedAt != nil {
		t.Fatalf("丢弃后的清扫状态错误 got=%#v", discarded)
	}
	if err := repo.UpdateDraftMedia(ctx, draft.ID, 1, MediaVideo, SavedFile{PublicURL: "/static/videos/1/20260810/retry.mp4", FileName: "retry.mp4"}, "retry.mp4"); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("清扫中的草稿仍可写入媒体 error=%v", err)
	}
	ids, err := repo.GetRecoverableDraftPurgeList(ctx, 10)
	if err != nil || len(ids) != 1 || ids[0] != draft.ID {
		t.Fatalf("丢弃草稿未成为清扫候选 ids=%v error=%v", ids, err)
	}
	if repeated, err := repo.UpdateDraftDiscard(ctx, draft.ID, 1); err != nil || repeated.Status != VideoStatusPurging {
		t.Fatalf("重复丢弃应保持成功 draft=%#v error=%v", repeated, err)
	}
	if _, err := repo.UpdateDraftDiscard(ctx, draft.ID, 2); !errors.Is(err, ErrNotAuthor) {
		t.Fatalf("跨作者丢弃未被拒绝 error=%v", err)
	}

	published := seedVideo(t, repo, 1, "已发布", VideoStatusPublished, baseTime)
	if _, err := repo.UpdateDraftDiscard(ctx, published.ID, 1); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("已发布视频不应进入草稿清扫 error=%v", err)
	}

	rejectedAt := time.Now().Add(-time.Minute)
	rejected := newVideoFixture(1, "被拒绝视频", VideoStatusRejected, baseTime)
	rejected.RejectedReason = "媒体缺失"
	rejected.RejectedAt = &rejectedAt
	if err := repo.Create(ctx, rejected); err != nil {
		t.Fatalf("创建 rejected 视频失败: %v", err)
	}
	rejectedPurgeToken := "cccccccccccccccccccccccccccccccc"
	rejectedLease := time.Now().Add(time.Hour)
	rejected.PlayPurgedAt = &rejectedLease
	rejected.CoverPurgedAt = &rejectedLease
	rejected.PurgeToken = &rejectedPurgeToken
	rejected.PurgeLeaseUntil = &rejectedLease
	if err := db.Model(&Video{}).Where("id = ?", rejected.ID).Updates(map[string]any{
		"purge_token":       rejectedPurgeToken,
		"purge_lease_until": rejectedLease,
		"play_purged_at":    rejectedLease,
		"cover_purged_at":   rejectedLease,
	}).Error; err != nil {
		t.Fatalf("准备 rejected 清扫字段失败: %v", err)
	}
	discardedRejected, err := repo.UpdateDraftDiscard(ctx, rejected.ID, 1)
	if err != nil {
		t.Fatalf("主动丢弃 rejected 视频失败: %v", err)
	}
	if discardedRejected.Status != VideoStatusPurging || discardedRejected.PurgeToken != nil || discardedRejected.PurgeLeaseUntil != nil || discardedRejected.PlayPurgedAt != nil || discardedRejected.CoverPurgedAt != nil {
		t.Fatalf("rejected 丢弃后的清扫状态错误 got=%#v", discardedRejected)
	}
	if repeated, err := repo.UpdateDraftDiscard(ctx, rejected.ID, 1); err != nil || repeated.Status != VideoStatusPurging {
		t.Fatalf("重复丢弃 rejected 视频应保持成功 video=%#v error=%v", repeated, err)
	}
}

// 测试目标：验证 rejected 的自动清扫以 rejected_at 为保留期基准并支持租约认领
// 预期效果：到期和边界记录可认领，未到期或缺少 rejected_at 的记录不会提前清扫
func TestRepositoryRejectedPurgeCandidatesAndClaim(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	now := time.Now()
	cutoff := now.Add(-time.Hour)

	expired := newVideoFixture(1, "expired rejected", VideoStatusRejected, baseTime)
	boundary := newVideoFixture(1, "boundary rejected", VideoStatusRejected, baseTime)
	active := newVideoFixture(1, "active rejected", VideoStatusRejected, baseTime)
	legacy := newVideoFixture(1, "legacy rejected", VideoStatusRejected, cutoff.Add(-time.Hour))
	draft := newVideoFixture(1, "expired draft", VideoStatusDraft, baseTime)
	for _, item := range []*Video{expired, boundary, active, legacy, draft} {
		item.PublishedAt = nil
		if item.Status == VideoStatusRejected {
			item.RejectedReason = "媒体校验失败"
		}
		if err := repo.Create(ctx, item); err != nil {
			t.Fatalf("创建清扫候选 %q 失败: %v", item.Title, err)
		}
	}
	if err := db.Exec("UPDATE videos SET rejected_at = ?, created_at = ? WHERE id = ?", cutoff.Add(-time.Minute), now.Add(time.Hour), expired.ID).Error; err != nil {
		t.Fatalf("设置过期 rejected 时间失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET rejected_at = ? WHERE id = ?", cutoff, boundary.ID).Error; err != nil {
		t.Fatalf("设置边界 rejected 时间失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET rejected_at = ? WHERE id = ?", cutoff.Add(time.Minute), active.ID).Error; err != nil {
		t.Fatalf("设置活跃 rejected 时间失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET rejected_at = NULL, created_at = ? WHERE id = ?", cutoff.Add(-time.Hour), legacy.ID).Error; err != nil {
		t.Fatalf("设置旧 rejected 时间失败: %v", err)
	}
	var boundaryCutoff time.Time
	if err := db.Raw("SELECT rejected_at FROM videos WHERE id = ?", boundary.ID).Scan(&boundaryCutoff).Error; err != nil {
		t.Fatalf("读取数据库截断后的 rejected 边界失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET created_at = ? WHERE id = ?", cutoff.Add(-time.Minute), draft.ID).Error; err != nil {
		t.Fatalf("设置过期草稿创建时间失败: %v", err)
	}

	ids, err := repo.GetExpiredDraftPurgeList(ctx, boundaryCutoff, 20)
	if err != nil {
		t.Fatalf("查询 rejected 清扫候选失败: %v", err)
	}
	got := make(map[uint]bool, len(ids))
	for _, id := range ids {
		got[id] = true
	}
	for _, id := range []uint{expired.ID, boundary.ID, draft.ID} {
		if !got[id] {
			t.Errorf("应返回到期候选 id=%d ids=%v", id, ids)
		}
	}
	for _, id := range []uint{active.ID, legacy.ID} {
		if got[id] {
			t.Errorf("不应返回未到期或缺少 rejected_at 的候选 id=%d ids=%v", id, ids)
		}
	}

	claim, ok, err := repo.UpdateDraftPurgeClaim(ctx, expired.ID, boundaryCutoff, "dddddddddddddddddddddddddddddddd", time.Minute)
	if err != nil || !ok || claim == nil || claim.Token != "dddddddddddddddddddddddddddddddd" {
		t.Fatalf("到期 rejected 认领失败 claim=%+v ok=%t err=%v", claim, ok, err)
	}
	if _, ok, err := repo.UpdateDraftPurgeClaim(ctx, active.ID, boundaryCutoff, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", time.Minute); err != nil || ok {
		t.Fatalf("未到期 rejected 不应认领 ok=%t err=%v", ok, err)
	}
	if _, ok, err := repo.UpdateDraftPurgeClaim(ctx, legacy.ID, boundaryCutoff, "ffffffffffffffffffffffffffffffff", time.Minute); err != nil || ok {
		t.Fatalf("缺少 rejected_at 的旧记录不应认领 ok=%t err=%v", ok, err)
	}
}

// 测试目标：验证草稿清扫租约独占、过期接管和逐媒体删除检查点
// 预期效果：旧 token 不能提交进度或硬删除，两个非空媒体确认后才可物理删除
func TestRepositoryDraftPurgeLeaseAndCheckpoints(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	cutoff := time.Now().Add(-time.Hour)
	expired := newVideoFixture(1, "expired", VideoStatusDraft, baseTime)
	expired.PublishedAt = nil
	active := newVideoFixture(1, "active", VideoStatusDraft, baseTime)
	active.PublishedAt = nil
	published := newVideoFixture(1, "published", VideoStatusPublished, baseTime)
	for _, item := range []*Video{expired, active, published} {
		if err := repo.Create(ctx, item); err != nil {
			t.Fatalf("创建测试视频失败: %v", err)
		}
	}
	if err := db.Exec("UPDATE videos SET created_at = ? WHERE id = ?", cutoff.Add(-time.Minute), expired.ID).Error; err != nil {
		t.Fatalf("设置过期草稿创建时间失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET created_at = ? WHERE id = ?", time.Now().Add(time.Hour), active.ID).Error; err != nil {
		t.Fatalf("设置活跃草稿创建时间失败: %v", err)
	}

	ids, err := repo.GetExpiredDraftPurgeList(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("查询过期草稿失败: %v", err)
	}
	if len(ids) != 1 || ids[0] != expired.ID {
		t.Fatalf("过期草稿筛选错误 got=%+v", ids)
	}

	tokenA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claim, ok, err := repo.UpdateDraftPurgeClaim(ctx, expired.ID, cutoff, tokenA, time.Hour)
	if err != nil || !ok || claim == nil || claim.Token != tokenA || claim.PlayURL == "" || claim.CoverURL == "" {
		t.Fatalf("认领过期草稿错误 claim=%+v ok=%t err=%v", claim, ok, err)
	}
	if _, err := repo.UpdateDraftPublication(ctx, expired.ID, 1); !errors.Is(err, ErrDraftNotWritable) {
		t.Fatalf("清扫中的草稿仍可发布 error=%v", err)
	}
	if _, ok, err := repo.UpdateDraftPurgeClaim(ctx, expired.ID, cutoff, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", time.Hour); err != nil || ok {
		t.Fatalf("未过期租约不应被接管 ok=%t err=%v", ok, err)
	}
	if err := db.Model(&Video{}).Where("id = ?", expired.ID).Update("purge_lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("设置过期租约失败: %v", err)
	}
	ids, err = repo.GetRecoverableDraftPurgeList(ctx, 10)
	if err != nil || len(ids) != 1 || ids[0] != expired.ID {
		t.Fatalf("过期租约应重新成为候选 ids=%v err=%v", ids, err)
	}

	tokenB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	claim, ok, err = repo.UpdateDraftPurgeClaim(ctx, expired.ID, cutoff, tokenB, time.Hour)
	if err != nil || !ok || claim == nil || claim.Token != tokenB {
		t.Fatalf("过期租约接管失败 claim=%+v ok=%t err=%v", claim, ok, err)
	}
	if renewed, err := repo.UpdateDraftPurgeLease(ctx, expired.ID, tokenA, time.Hour); err != nil || renewed {
		t.Fatalf("旧 token 不应续租 renewed=%t err=%v", renewed, err)
	}
	if marked, err := repo.UpdateDraftMediaPurge(ctx, expired.ID, tokenA, MediaVideo, time.Hour); err != nil || marked {
		t.Fatalf("旧 token 不应写入进度 marked=%t err=%v", marked, err)
	}
	if marked, err := repo.UpdateDraftMediaPurge(ctx, expired.ID, tokenB, MediaVideo, time.Hour); err != nil || !marked {
		t.Fatalf("视频删除检查点写入失败 marked=%t err=%v", marked, err)
	}
	if deleted, err := repo.RemovePurgedDraft(ctx, expired.ID, tokenB); err != nil || deleted {
		t.Fatalf("封面未确认前不应硬删除 deleted=%t err=%v", deleted, err)
	}
	if marked, err := repo.UpdateDraftMediaPurge(ctx, expired.ID, tokenB, MediaCover, time.Hour); err != nil || !marked {
		t.Fatalf("封面删除检查点写入失败 marked=%t err=%v", marked, err)
	}
	if deleted, err := repo.RemovePurgedDraft(ctx, expired.ID, tokenA); err != nil || deleted {
		t.Fatalf("旧 token 不应硬删除 deleted=%t err=%v", deleted, err)
	}
	deleted, err := repo.RemovePurgedDraft(ctx, expired.ID, tokenB)
	if err != nil || !deleted {
		t.Fatalf("硬删除已认领草稿失败 deleted=%t err=%v", deleted, err)
	}
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM videos WHERE id = ?", expired.ID).Scan(&count).Error; err != nil {
		t.Fatalf("统计已删除草稿失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("过期草稿应已硬删除 count=%d", count)
	}
}

// 测试目标：验证两个仓储实例并发认领同一过期草稿时租约互斥
// 预期效果：恰好一个 token 成功取得草稿，另一个实例不能获得清扫所有权
func TestRepositoryClaimDraftPurgeIsExclusive(t *testing.T) {
	db := testutil.DB(t)
	first := NewRepository(db)
	second := NewRepository(db.Session(&gorm.Session{NewDB: true}))
	ctx := context.Background()
	cutoff := time.Now().Add(-time.Hour)
	draft := newVideoFixture(1, "concurrent claim", VideoStatusDraft, baseTime)
	draft.PublishedAt = nil
	if err := first.Create(ctx, draft); err != nil {
		t.Fatalf("创建并发认领草稿失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET created_at = ? WHERE id = ?", cutoff.Add(-time.Minute), draft.ID).Error; err != nil {
		t.Fatalf("设置过期草稿创建时间失败: %v", err)
	}

	type result struct {
		token string
		ok    bool
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, item := range []struct {
		repository *Repository
		token      string
	}{
		{repository: first, token: "11111111111111111111111111111111"},
		{repository: second, token: "22222222222222222222222222222222"},
	} {
		item := item
		go func() {
			<-start
			_, ok, err := item.repository.UpdateDraftPurgeClaim(ctx, draft.ID, cutoff, item.token, time.Minute)
			results <- result{token: item.token, ok: ok, err: err}
		}()
	}
	close(start)

	var winner string
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("并发认领失败 token=%s err=%v", result.token, result.err)
		}
		if result.ok {
			if winner != "" {
				t.Fatalf("不应有两个租约 owner first=%s second=%s", winner, result.token)
			}
			winner = result.token
		}
	}
	if winner == "" {
		t.Fatal("并发认领应有一个 token 成功")
	}
	var claimed Video
	if err := db.First(&claimed, draft.ID).Error; err != nil {
		t.Fatalf("读取并发认领结果失败: %v", err)
	}
	if claimed.Status != VideoStatusPurging || claimed.PurgeToken == nil || *claimed.PurgeToken != winner {
		t.Fatalf("并发认领持久化结果错误 status=%s token=%v winner=%s", claimed.Status, claimed.PurgeToken, winner)
	}
}

// 测试目标：验证公开视频列表的状态过滤、作者过滤和排序
// 预期效果：仅返回已发布视频并按发布时间倒序排列
func TestRepositoryListPublishedFilterAndOrder(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	// 测试目标：准备不同作者和状态的视频测试数据
	// 预期效果：可同时验证全局列表和作者列表的过滤与排序
	seedVideo(t, repo, 1, "a1-new", VideoStatusPublished, baseTime.Add(2*time.Minute))
	seedVideo(t, repo, 1, "a1-old", VideoStatusPublished, baseTime)
	seedVideo(t, repo, 1, "a1-draft", VideoStatusDraft, baseTime.Add(3*time.Minute))
	seedVideo(t, repo, 2, "a2", VideoStatusPublished, baseTime.Add(time.Minute))

	all, err := repo.GetPublishedVideoList(ctx, 0, nil, 10)
	if err != nil {
		t.Fatalf("GetPublishedVideoList 全局列表: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("全局列表应只有 3 条 published, got=%d", len(all))
	}
	want := []string{"a1-new", "a2", "a1-old"}
	for i, title := range want {
		if all[i].Title != title {
			t.Fatalf("全局列表顺序错误 index=%d got=%s want=%s", i, all[i].Title, title)
		}
	}

	mine, err := repo.GetPublishedVideoList(ctx, 1, nil, 10)
	if err != nil {
		t.Fatalf("GetPublishedVideoList 作者列表: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("author 1 应只有 2 条 published, got=%d", len(mine))
	}
	if mine[0].Title != "a1-new" || mine[1].Title != "a1-old" {
		t.Fatalf("作者列表顺序错误 got=%v", mine)
	}
}

// 测试目标：验证公开视频列表的游标分页完整性
// 预期效果：所有视频按倒序仅返回一次且不会遗漏或重复
func TestRepositoryListPublishedCursorPagination(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	// 测试目标：记录写入视频的标识与标题对应关系
	// 预期效果：后续可按翻页结果验证完整排序
	titles := map[uint]string{}
	for i := 0; i < 5; i++ {
		v := seedVideo(t, repo, 1, fmt.Sprintf("v%d", i), VideoStatusPublished, baseTime.Add(time.Duration(i)*time.Minute))
		titles[v.ID] = v.Title
	}

	// 测试目标：记录已读取视频和分页游标结果
	// 预期效果：可检测重复项并验证翻页完整性
	seen := map[uint]bool{}
	var cursor *Cursor
	var got []uint
	for page := 0; page < 10; page++ {
		items, err := repo.GetPublishedVideoList(ctx, 0, cursor, 2)
		if err != nil {
			t.Fatalf("第 %d 页查询失败: %v", page, err)
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("游标翻页重复返回 id=%d", item.ID)
			}
			seen[item.ID] = true
			got = append(got, item.ID)
		}
		last := items[len(items)-1]
		cursor = &Cursor{PublishedAt: *last.PublishedAt, ID: last.ID}
	}

	if len(got) != 5 {
		t.Fatalf("应完整翻出 5 条, got=%v", got)
	}
	want := []string{"v4", "v3", "v2", "v1", "v0"}
	for i, id := range got {
		if titles[id] != want[i] {
			t.Fatalf("翻页顺序错误 index=%d id=%d title=%s want=%s", i, id, titles[id], want[i])
		}
	}
}

// 测试目标：验证发布时间相同时游标分页的次级排序
// 预期效果：视频按标识倒序返回且翻页边界不重复不遗漏
func TestRepositoryListPublishedCursorTieBreak(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	// 测试目标：准备发布时间相同的视频和其标识集合
	// 预期效果：验证同一时间点以视频标识作为次级排序条件
	same := baseTime
	var ids []uint
	for i := 0; i < 3; i++ {
		v := seedVideo(t, repo, 1, fmt.Sprintf("same-%d", i), VideoStatusPublished, same)
		ids = append(ids, v.ID)
	}

	first, err := repo.GetPublishedVideoList(ctx, 0, nil, 2)
	if err != nil {
		t.Fatalf("第一页查询失败: %v", err)
	}
	if len(first) != 2 || first[0].ID != ids[2] || first[1].ID != ids[1] {
		t.Fatalf("同一 published_at 应按 id 倒序, got ids=%v", []uint{first[0].ID, first[1].ID})
	}

	last := first[1]
	second, err := repo.GetPublishedVideoList(ctx, 0, &Cursor{PublishedAt: *last.PublishedAt, ID: last.ID}, 2)
	if err != nil {
		t.Fatalf("第二页查询失败: %v", err)
	}
	if len(second) != 1 || second[0].ID != ids[0] {
		t.Fatalf("第二页应只剩 id=%d, got=%+v", ids[0], second)
	}

	empty, err := repo.GetPublishedVideoList(ctx, 0, &Cursor{PublishedAt: same, ID: ids[0]}, 2)
	if err != nil {
		t.Fatalf("末页查询失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("游标指向最后一条后应为空, got=%+v", empty)
	}
}

// 测试目标：验证个人视频列表只包含该作者已发布的视频
// 预期效果：草稿和处理中视频不会混入 VideoItem 列表，其他作者和零值作者不返回数据
func TestRepositoryListByAuthorIncludesPublishedOnly(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	seedVideo(t, repo, 1, "pub", VideoStatusPublished, baseTime)
	seedVideo(t, repo, 1, "draft", VideoStatusDraft, baseTime.Add(time.Minute))
	seedVideo(t, repo, 1, "proc", VideoStatusProcessing, baseTime.Add(2*time.Minute))
	seedVideo(t, repo, 2, "other", VideoStatusPublished, baseTime.Add(3*time.Minute))

	mine, err := repo.GetAuthorVideoList(ctx, 1, nil, 10)
	if err != nil {
		t.Fatalf("GetAuthorVideoList: %v", err)
	}
	if len(mine) != 1 || mine[0].Status != VideoStatusPublished || mine[0].Title != "pub" {
		t.Fatalf("个人列表应只返回已发布视频, got=%+v", mine)
	}

	if empty, err := repo.GetAuthorVideoList(ctx, 999, nil, 10); err != nil || len(empty) != 0 {
		t.Fatalf("不存在的作者应为空, got=%v err=%v", empty, err)
	}
	if empty, err := repo.GetAuthorVideoList(ctx, 0, nil, 10); err != nil || len(empty) != 0 {
		t.Fatalf("authorID=0 应为空, got=%v err=%v", empty, err)
	}
}

// 测试目标：验证仓储只软删除作者的已发布视频
// 预期效果：草稿不会进入已发布视频清扫路径，已发布行仍保持软删除语义
func TestRepositorySoftDeletePublished(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	v := seedVideo(t, repo, 1, "删除", VideoStatusPublished, baseTime)

	if err := repo.DeletePublishedVideo(ctx, v.ID, v.AuthorID); err != nil {
		t.Fatalf("DeletePublishedVideo: %v", err)
	}
	if _, err := repo.GetByID(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删后 GetByID 应 not found, err=%v", err)
	}
	if _, err := repo.GetPublishedByID(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("软删后公开读应 not found, err=%v", err)
	}

	// 测试目标：检查软删除后的物理记录
	// 预期效果：物理行保留且删除时间已写入
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM videos WHERE id = ?", v.ID).Scan(&count).Error; err != nil {
		t.Fatalf("原生统计失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("物理行应保留, count=%d", count)
	}
	var deletedAt *time.Time
	if err := db.Raw("SELECT deleted_at FROM videos WHERE id = ?", v.ID).Scan(&deletedAt).Error; err != nil {
		t.Fatalf("原生读 deleted_at 失败: %v", err)
	}
	if deletedAt == nil {
		t.Fatal("deleted_at 应为非空")
	}

	items, err := repo.GetPublishedVideoList(ctx, 1, nil, 10)
	if err != nil {
		t.Fatalf("删除后列表查询失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("删除后列表应不含该视频, got=%+v", items)
	}

	if err := repo.DeletePublishedVideo(ctx, v.ID, v.AuthorID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("重复删除应 not found, err=%v", err)
	}

	draft := seedVideo(t, repo, 1, "草稿", VideoStatusDraft, baseTime)
	if err := repo.DeletePublishedVideo(ctx, draft.ID, draft.AuthorID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("草稿不应走已发布软删除 err=%v", err)
	}
	if _, err := repo.GetByID(ctx, draft.ID); err != nil {
		t.Fatalf("草稿不应被软删除 err=%v", err)
	}
}

// 测试目标：验证到期软删除视频可被查询并硬删除
// 预期效果：早于或等于截止时间的软删记录被删除，宽限期内及活跃记录保持不变
func TestRepositoryPurgeExpiredDeleted(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	cutoff := time.Date(2026, 8, 18, 12, 0, 0, 0, time.Local)

	expired := seedVideo(t, repo, 1, "expired", VideoStatusPublished, baseTime)
	boundary := seedVideo(t, repo, 1, "boundary", VideoStatusPublished, baseTime)
	grace := seedVideo(t, repo, 1, "grace", VideoStatusPublished, baseTime)
	active := seedVideo(t, repo, 1, "active", VideoStatusPublished, baseTime)
	draftDeleted := seedVideo(t, repo, 1, "draft-deleted", VideoStatusDraft, baseTime)
	purgingDeleted := seedVideo(t, repo, 1, "purging-deleted", VideoStatusPurging, baseTime)
	setVideoDeletedAt(t, db, expired.ID, cutoff.Add(-time.Minute))
	setVideoDeletedAt(t, db, boundary.ID, cutoff)
	setVideoDeletedAt(t, db, grace.ID, cutoff.Add(time.Minute))
	setVideoDeletedAt(t, db, draftDeleted.ID, cutoff.Add(-time.Minute))
	setVideoDeletedAt(t, db, purgingDeleted.ID, cutoff.Add(-time.Minute))

	items, err := repo.GetExpiredDeletedVideoList(ctx, cutoff)
	if err != nil {
		t.Fatalf("GetExpiredDeletedVideoList: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应只返回到期和边界视频, got=%+v", items)
	}
	for _, item := range []Video{*draftDeleted, *purgingDeleted} {
		if deleted, err := repo.RemoveExpiredVideo(ctx, item.ID, cutoff); err != nil || deleted {
			t.Fatalf("非 published 记录不应由已发布清扫删除 id=%d deleted=%t err=%v", item.ID, deleted, err)
		}
	}

	for _, item := range items {
		deleted, err := repo.RemoveExpiredVideo(ctx, item.ID, cutoff)
		if err != nil {
			t.Fatalf("RemoveExpiredVideo id=%d: %v", item.ID, err)
		}
		if !deleted {
			t.Fatalf("到期视频应被硬删除 id=%d", item.ID)
		}
	}
	if deleted, err := repo.RemoveExpiredVideo(ctx, expired.ID, cutoff); err != nil || deleted {
		t.Fatalf("重复硬删除应为空 deleted=%t err=%v", deleted, err)
	}

	for _, id := range []uint{expired.ID, boundary.ID} {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM videos WHERE id = ?", id).Scan(&count).Error; err != nil {
			t.Fatalf("统计硬删除视频失败: %v", err)
		}
		if count != 0 {
			t.Fatalf("视频 id=%d 应被硬删除", id)
		}
	}
	for _, id := range []uint{grace.ID, active.ID} {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM videos WHERE id = ?", id).Scan(&count).Error; err != nil {
			t.Fatalf("统计保留视频失败: %v", err)
		}
		if count != 1 {
			t.Fatalf("视频 id=%d 应在数据库中保留", id)
		}
	}
}

// 测试目标：验证公开视频列表处理非正分页数量
// 预期效果：数量为零时查询成功并返回空列表
func TestRepositoryListPublishedNonPositiveLimit(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx := context.Background()

	seedVideo(t, repo, 1, "v", VideoStatusPublished, baseTime)

	items, err := repo.GetPublishedVideoList(ctx, 0, nil, 0)
	if err != nil {
		t.Fatalf("limit=0 查询失败: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("limit=0 应为空, got=%+v", items)
	}
}

// 测试目标：验证用户主页的视频数量只统计当前公开可见的视频
// 预期效果：其他作者、非发布状态和软删除视频均不计入
func TestRepositoryCountPublishedByAuthor(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	seedVideo(t, repo, 1, "published-1", VideoStatusPublished, baseTime)
	seedVideo(t, repo, 1, "published-2", VideoStatusPublished, baseTime.Add(time.Minute))
	seedVideo(t, repo, 1, "draft", VideoStatusDraft, baseTime)
	seedVideo(t, repo, 1, "processing", VideoStatusProcessing, baseTime)
	seedVideo(t, repo, 1, "rejected", VideoStatusRejected, baseTime)
	deleted := seedVideo(t, repo, 1, "deleted", VideoStatusPublished, baseTime)
	seedVideo(t, repo, 2, "other-author", VideoStatusPublished, baseTime)
	if err := repo.DeletePublishedVideo(ctx, deleted.ID, deleted.AuthorID); err != nil {
		t.Fatalf("DeletePublishedVideo: %v", err)
	}

	count, err := repo.GetPublishedVideoCountByAuthor(ctx, 1)
	if err != nil {
		t.Fatalf("GetPublishedVideoCountByAuthor: %v", err)
	}
	if count != 2 {
		t.Fatalf("作者 1 应仅统计 2 条公开视频, got=%d", count)
	}

	otherCount, err := repo.GetPublishedVideoCountByAuthor(ctx, 2)
	if err != nil {
		t.Fatalf("GetPublishedVideoCountByAuthor other: %v", err)
	}
	if otherCount != 1 {
		t.Fatalf("作者 2 的视频不应混入作者 1, got=%d", otherCount)
	}

	zeroCount, err := repo.GetPublishedVideoCountByAuthor(ctx, 0)
	if err != nil || zeroCount != 0 {
		t.Fatalf("authorID=0 应返回零值, count=%d err=%v", zeroCount, err)
	}
}
