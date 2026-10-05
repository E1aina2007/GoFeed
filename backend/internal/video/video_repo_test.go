package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	authn "gofeed/internal/auth"
	dbpkg "gofeed/internal/db"
	jwtmw "gofeed/internal/middleware/jwt"
	"gofeed/internal/testutil"
	"gofeed/internal/user"
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

// 测试目标：验证批量读取公开视频的可见性过滤
// 预期效果：非发布状态、软删除、发布时间为空、媒体字段不完整和不存在的标识都不返回
func TestRepositoryGetPublishedByIDsFiltersInvisible(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	public := seedVideo(t, repo, 1, "public", VideoStatusPublished, baseTime)
	otherAuthor := seedVideo(t, repo, 7, "other-author", VideoStatusPublished, baseTime.Add(time.Minute))

	ids := []uint{public.ID, otherAuthor.ID}
	draft := seedVideo(t, repo, 1, "draft", VideoStatusDraft, baseTime)
	processing := seedVideo(t, repo, 1, "processing", VideoStatusProcessing, baseTime)
	rejected := seedVideo(t, repo, 1, "rejected", VideoStatusRejected, baseTime)
	purging := seedVideo(t, repo, 1, "purging", VideoStatusPurging, baseTime)
	deleted := seedVideo(t, repo, 1, "deleted", VideoStatusPublished, baseTime)
	invisible := []*Video{draft, processing, rejected, purging, deleted}

	nullPublishedAt := newVideoFixture(1, "null-published-at", VideoStatusPublished, baseTime)
	nullPublishedAt.PublishedAt = nil
	if err := repo.Create(ctx, nullPublishedAt); err != nil {
		t.Fatalf("写入发布时间为空的视频失败: %v", err)
	}
	invisible = append(invisible, nullPublishedAt)

	blankFields := []struct {
		name   string
		mutate func(*Video)
	}{
		{name: "缺少播放地址", mutate: func(row *Video) { row.PlayURL = "" }},
		{name: "缺少播放文件名", mutate: func(row *Video) { row.PlayFileName = "" }},
		{name: "缺少播放原始名", mutate: func(row *Video) { row.PlayOriginalName = "" }},
		{name: "缺少封面地址", mutate: func(row *Video) { row.CoverURL = "" }},
		{name: "缺少封面文件名", mutate: func(row *Video) { row.CoverFileName = "" }},
		{name: "缺少封面原始名", mutate: func(row *Video) { row.CoverOriginalName = "" }},
	}
	for _, item := range blankFields {
		row := newVideoFixture(1, item.name, VideoStatusPublished, baseTime)
		item.mutate(row)
		if err := repo.Create(ctx, row); err != nil {
			t.Fatalf("写入%s的视频失败: %v", item.name, err)
		}
		invisible = append(invisible, row)
	}
	if err := repo.DeletePublishedVideo(ctx, deleted.ID, deleted.AuthorID); err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	for _, row := range invisible {
		ids = append(ids, row.ID)
	}
	ids = append(ids, 999999)

	got, err := repo.GetPublishedByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("批量读取公开视频失败: %v", err)
	}
	byID := make(map[uint]Video, len(got))
	for _, row := range got {
		byID[row.ID] = row
	}
	if len(byID) != 2 {
		t.Fatalf("结果应只包含两条公开视频 got=%d", len(byID))
	}
	for _, row := range invisible {
		if _, ok := byID[row.ID]; ok {
			t.Fatalf("不可见视频不应出现在结果中 id=%d title=%s", row.ID, row.Title)
		}
	}
	if _, ok := byID[999999]; ok {
		t.Fatalf("不存在的标识不应出现在结果中")
	}
	card, ok := byID[public.ID]
	if !ok {
		t.Fatalf("公开视频缺失 got=%+v", got)
	}
	if card.Title != public.Title || card.PlayURL != public.PlayURL || card.CoverURL != public.CoverURL {
		t.Fatalf("公开视频字段映射错误 got=%+v", card)
	}
	if card.PlayFileName != public.PlayFileName || card.PlayOriginalName != public.PlayOriginalName {
		t.Fatalf("公开视频播放媒体字段映射错误 got=%+v", card)
	}
	if card.CoverFileName != public.CoverFileName || card.CoverOriginalName != public.CoverOriginalName {
		t.Fatalf("公开视频封面媒体字段映射错误 got=%+v", card)
	}
	if card.PublishedAt == nil || !card.PublishedAt.Equal(*public.PublishedAt) {
		t.Fatalf("公开视频发布时间错误 got=%v want=%v", card.PublishedAt, *public.PublishedAt)
	}
	if otherAuthorRow, ok := byID[otherAuthor.ID]; !ok || otherAuthorRow.AuthorID != 7 {
		t.Fatalf("批量读取不应按作者过滤 other=%+v", otherAuthorRow)
	}
}

// videoTestVideoHeader 是校验通过的最小 mp4 文件头
var videoTestVideoHeader = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

// videoTestImageHeader 是校验通过的最小 png 文件头
var videoTestImageHeader = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// videoJSONRequest 发送可选 JSON 请求体与可选访问令牌的 HTTP 请求
func videoJSONRequest(t *testing.T, engine *gin.Engine, method, path, token string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if payload == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	request := httptest.NewRequest(method, path, reader)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// videoMultipartRequest 以多部分表单上传单个媒体文件
func videoMultipartRequest(t *testing.T, engine *gin.Engine, path, token, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建表单文件失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, path, &form)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// videoResponseBody 解析响应体为通用映射以便断言字段契约
func videoResponseBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v body=%s", err, recorder.Body.String())
	}
	return body
}

// videoErrorMessage 断言状态码并读取错误响应的公开文案
func videoErrorMessage(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) string {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	message, ok := videoResponseBody(t, recorder)["error"].(string)
	if !ok {
		t.Fatalf("错误响应缺少 error 字段 body=%s", recorder.Body.String())
	}
	return message
}

// videoListIDs 读取列表响应中的视频标识顺序
func videoListIDs(t *testing.T, recorder *httptest.ResponseRecorder) []uint {
	t.Helper()
	value, exists := videoResponseBody(t, recorder)["items"]
	if !exists {
		t.Fatalf("列表响应缺少 items body=%s", recorder.Body.String())
	}
	if value == nil {
		return []uint{}
	}
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("列表响应 items 格式错误 body=%s", recorder.Body.String())
	}
	ids := make([]uint, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("列表项格式错误 got=%v", entry)
		}
		value, ok := item["id"].(float64)
		if !ok {
			t.Fatalf("列表项缺少标识 got=%v", item)
		}
		ids = append(ids, uint(value))
	}
	return ids
}

// assertVideoIDs 断言列表响应中的视频标识与顺序
func assertVideoIDs(t *testing.T, got, want []uint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("列表长度错误 got=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("列表顺序错误 got=%v want=%v", got, want)
		}
	}
}

// sqlEngagementReader 使用真实 SQL 聚合互动计数以替代 social 包避免测试包循环依赖
type sqlEngagementReader struct {
	db *gorm.DB
}

func (r sqlEngagementReader) GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]EngagementCounts, error) {
	counts := make(map[uint]EngagementCounts, len(videoIDs))
	if len(videoIDs) == 0 {
		return counts, nil
	}
	for _, id := range videoIDs {
		counts[id] = EngagementCounts{}
	}

	type aggregate struct {
		VideoID uint
		Total   int64
	}
	var likes []aggregate
	if err := r.db.WithContext(ctx).Table("video_likes").
		Select("video_id, COUNT(*) AS total").Where("video_id IN ?", videoIDs).
		Group("video_id").Scan(&likes).Error; err != nil {
		return nil, err
	}
	for _, item := range likes {
		entry := counts[item.VideoID]
		entry.LikesCount = item.Total
		counts[item.VideoID] = entry
	}

	var comments []aggregate
	if err := r.db.WithContext(ctx).Table("video_comments").
		Select("video_id, COUNT(*) AS total").
		Where("video_id IN ? AND deleted_at IS NULL", videoIDs).
		Group("video_id").Scan(&comments).Error; err != nil {
		return nil, err
	}
	for _, item := range comments {
		entry := counts[item.VideoID]
		entry.CommentsCount = item.Total
		counts[item.VideoID] = entry
	}
	return counts, nil
}

// newVideoHTTPEngine 装配视频模块的公开读取与认证写入端点
func newVideoHTTPEngine(t *testing.T, middlewares ...gin.HandlerFunc) (*gin.Engine, *gorm.DB, *Repository, *authn.SessionService) {
	t.Helper()
	gdb := testutil.DB(t)
	repo := NewRepository(gdb)
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	service := NewService(repo, NewUserAuthorReader(user.NewRepository(gdb)), sqlEngagementReader{db: gdb})
	controller := NewController(service, NewLocalStorage(t.TempDir()))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middlewares...)
	engine.GET("/api/video", controller.GetVideoList)
	engine.GET("/api/video/:id", controller.GetVideo)

	authorized := engine.Group("/api/video/auth", jwtmw.Auth(sessions))
	authorized.POST("/drafts", controller.CreateDraft)
	authorized.GET("/drafts/:id", controller.GetDraft)
	authorized.POST("/drafts/:id/play", controller.UpdateDraftVideo)
	authorized.POST("/drafts/:id/cover", controller.UpdateDraftCover)
	authorized.POST("/drafts/:id/publish", controller.UpdateDraftPublication)
	authorized.DELETE("/drafts/:id", controller.DiscardDraft)
	authorized.GET("/mine", controller.GetMyVideoList)
	authorized.GET("/:id/status", controller.GetVideoStatus)
	authorized.DELETE("/:id", controller.DeleteVideo)
	return engine, gdb, repo, sessions
}

// newVideoAuthor 创建作者账号并签发访问令牌
func newVideoAuthor(t *testing.T, gdb *gorm.DB, username string) (*user.User, string) {
	t.Helper()
	account := &user.User{Username: username, Password: "test-password-hash"}
	if err := gdb.Create(account).Error; err != nil {
		t.Fatalf("创建用户 %q 失败: %v", username, err)
	}
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	pair, err := sessions.Create(context.Background(), account.ID, account.Username)
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	return account, pair.AccessToken
}

// videoInteractions 写入真实点赞与评论行以验证互动计数
func videoInteractions(t *testing.T, gdb *gorm.DB, videoID uint, likerIDs []uint, commenters []uint) {
	t.Helper()
	now := time.Now()
	for _, likerID := range likerIDs {
		if err := gdb.Exec("INSERT INTO video_likes (video_id, user_id, created_at) VALUES (?, ?, ?)",
			videoID, likerID, now).Error; err != nil {
			t.Fatalf("写入点赞失败: %v", err)
		}
	}
	for index, authorID := range commenters {
		if err := gdb.Exec("INSERT INTO video_comments (video_id, author_id, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			videoID, authorID, fmt.Sprintf("评论 %d", index+1), now, now).Error; err != nil {
			t.Fatalf("写入评论失败: %v", err)
		}
	}
}

// 测试目标：验证草稿上传与发布的完整 HTTP 状态契约
// 预期效果：建草稿 201、媒体不完整发布 409、上传媒体 201、发布 202 且状态转为 processing
func TestVideoDraftPublishHTTPContract(t *testing.T) {
	engine, gdb, _, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "draft-author")

	created := videoJSONRequest(t, engine, http.MethodPost, "/api/video/auth/drafts", token,
		map[string]any{"title": "我的第一个作品", "description": "草稿描述"})
	if created.Code != http.StatusCreated {
		t.Fatalf("建草稿状态错误 got=%d body=%s", created.Code, created.Body.String())
	}
	draft, ok := videoResponseBody(t, created)["draft"].(map[string]any)
	if !ok {
		t.Fatalf("建草稿响应缺少 draft body=%s", created.Body.String())
	}
	if draft["status"] != VideoStatusDraft || draft["has_video"] != false || draft["has_cover"] != false {
		t.Fatalf("草稿初始字段错误 got=%v", draft)
	}
	draftID := uint(draft["id"].(float64))

	incomplete := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, nil)
	if message := videoErrorMessage(t, incomplete, http.StatusConflict); message != ErrDraftIncomplete.Error() {
		t.Fatalf("媒体不完整发布文案错误 got=%q", message)
	}

	play := videoMultipartRequest(t, engine, fmt.Sprintf("/api/video/auth/drafts/%d/play", draftID), token,
		"clip.mp4", videoTestVideoHeader)
	if play.Code != http.StatusCreated {
		t.Fatalf("上传视频状态错误 got=%d body=%s", play.Code, play.Body.String())
	}
	playBody := videoResponseBody(t, play)
	if uint(playBody["draft_id"].(float64)) != draftID {
		t.Fatalf("上传视频 draft_id 错误 got=%v", playBody)
	}
	if !strings.HasPrefix(playBody["play_url"].(string), fmt.Sprintf("/static/videos/%d/", author.ID)) ||
		!strings.HasSuffix(playBody["play_url"].(string), playBody["play_file_name"].(string)) {
		t.Fatalf("上传视频地址归属错误 got=%v", playBody)
	}
	if playBody["play_original_name"] != "clip.mp4" {
		t.Fatalf("上传视频原始文件名错误 got=%v", playBody)
	}

	cover := videoMultipartRequest(t, engine, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draftID), token,
		"cover.png", videoTestImageHeader)
	if cover.Code != http.StatusCreated {
		t.Fatalf("上传封面状态错误 got=%d body=%s", cover.Code, cover.Body.String())
	}
	coverBody := videoResponseBody(t, cover)
	if !strings.HasPrefix(coverBody["cover_url"].(string), fmt.Sprintf("/static/covers/%d/", author.ID)) ||
		!strings.HasSuffix(coverBody["cover_url"].(string), coverBody["cover_file_name"].(string)) {
		t.Fatalf("上传封面地址归属错误 got=%v", coverBody)
	}

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/auth/drafts/%d", draftID), token, nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("草稿详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	detailDraft := videoResponseBody(t, detail)["draft"].(map[string]any)
	if detailDraft["has_video"] != true || detailDraft["has_cover"] != true {
		t.Fatalf("草稿媒体标记错误 got=%v", detailDraft)
	}

	withBody := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, map[string]any{"title": "覆盖"})
	if message := videoErrorMessage(t, withBody, http.StatusBadRequest); message != "publish draft does not accept a request body" {
		t.Fatalf("发布请求体文案错误 got=%q", message)
	}

	published := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, nil)
	if published.Code != http.StatusAccepted {
		t.Fatalf("发布状态错误 got=%d body=%s", published.Code, published.Body.String())
	}
	publishedDraft := videoResponseBody(t, published)["draft"].(map[string]any)
	if publishedDraft["status"] != VideoStatusProcessing {
		t.Fatalf("发布后状态错误 got=%v", publishedDraft)
	}

	repeated := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, nil)
	if message := videoErrorMessage(t, repeated, http.StatusConflict); message != ErrDraftNotWritable.Error() {
		t.Fatalf("重复发布文案错误 got=%q", message)
	}

	processing := videoJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video/auth/%d/status", draftID), token, nil)
	if processing.Code != http.StatusOK {
		t.Fatalf("处理状态查询错误 got=%d body=%s", processing.Code, processing.Body.String())
	}
	if videoResponseBody(t, processing)["status"] != VideoStatusProcessing {
		t.Fatalf("处理状态字段错误 got=%s", processing.Body.String())
	}

	anonymous := videoJSONRequest(t, engine, http.MethodPost, "/api/video/auth/drafts", "",
		map[string]any{"title": "未登录"})
	if message := videoErrorMessage(t, anonymous, http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录建草稿文案错误 got=%q", message)
	}
}

// 测试目标：验证公开列表与详情的可见性、排序与互动计数语义
// 预期效果：仅完整已发布作品可见，同发布时间按标识倒序，计数随真实互动增长
func TestVideoPublicVisibilityHTTPContract(t *testing.T) {
	engine, gdb, repo, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "list-author")
	other, otherToken := newVideoAuthor(t, gdb, "list-other")

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
	first := seedVideo(t, repo, author.ID, "第一条", VideoStatusPublished, base)
	second := seedVideo(t, repo, author.ID, "第二条", VideoStatusPublished, base)
	newest := seedVideo(t, repo, author.ID, "最新一条", VideoStatusPublished, base.Add(time.Hour))
	draft := seedVideo(t, repo, author.ID, "未发布草稿", VideoStatusDraft, base)
	foreign := seedVideo(t, repo, other.ID, "他人作品", VideoStatusPublished, base.Add(2*time.Hour))
	removed := seedVideo(t, repo, author.ID, "已删除作品", VideoStatusPublished, base.Add(3*time.Hour))
	setVideoDeletedAt(t, gdb, removed.ID, time.Now())
	incomplete := newVideoFixture(author.ID, "媒体不完整作品", VideoStatusPublished, base.Add(4*time.Hour))
	incomplete.CoverFileName = ""
	if err := repo.Create(context.Background(), incomplete); err != nil {
		t.Fatalf("写入不完整作品失败: %v", err)
	}

	public := videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)
	if public.Code != http.StatusOK {
		t.Fatalf("公开列表状态错误 got=%d body=%s", public.Code, public.Body.String())
	}
	assertVideoIDs(t, videoListIDs(t, public), []uint{foreign.ID, newest.ID, second.ID, first.ID})

	byAuthor := videoJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video?author_id=%d", author.ID), "", nil)
	assertVideoIDs(t, videoListIDs(t, byAuthor), []uint{newest.ID, second.ID, first.ID})

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video?author_id=abc", "", nil),
		http.StatusBadRequest); message != ErrInvalidAuthorID.Error() {
		t.Fatalf("非法作者参数文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video?limit=99", "", nil),
		http.StatusBadRequest); message != ErrInvalidLimit.Error() {
		t.Fatalf("非法分页文案错误 got=%q", message)
	}

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", first.ID), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("公开详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	item := videoResponseBody(t, detail)["video"].(map[string]any)
	detailAuthor := item["author"].(map[string]any)
	if detailAuthor["username"] != "list-author" || detailAuthor["id"].(float64) != float64(author.ID) {
		t.Fatalf("公开详情作者错误 got=%v", detailAuthor)
	}
	if item["likes_count"].(float64) != 0 || item["comments_count"].(float64) != 0 {
		t.Fatalf("初始互动计数错误 got=%v", item)
	}

	videoInteractions(t, gdb, first.ID, []uint{other.ID, author.ID}, []uint{other.ID})
	liked := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", first.ID), "", nil)
	likedItem := videoResponseBody(t, liked)["video"].(map[string]any)
	if likedItem["likes_count"].(float64) != 2 || likedItem["comments_count"].(float64) != 1 {
		t.Fatalf("互动计数错误 got=%v", likedItem)
	}

	for name, id := range map[string]uint{
		"草稿":    draft.ID,
		"已删除":   removed.ID,
		"媒体不完整": incomplete.ID,
	} {
		message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", id), "", nil),
			http.StatusNotFound)
		if message != "video not found" {
			t.Fatalf("%s作品详情文案错误 got=%q", name, message)
		}
	}

	mine := videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", token, nil)
	if mine.Code != http.StatusOK {
		t.Fatalf("我的视频状态错误 got=%d body=%s", mine.Code, mine.Body.String())
	}
	assertVideoIDs(t, videoListIDs(t, mine), []uint{newest.ID, second.ID, first.ID})

	foreignMine := videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", otherToken, nil)
	assertVideoIDs(t, videoListIDs(t, foreignMine), []uint{foreign.ID})
}

// 测试目标：验证视频删除的权限与可见性契约
// 预期效果：非作者删除 403、作者删除 204 后详情 404 且公开列表不再包含
func TestVideoDeleteHTTPContract(t *testing.T) {
	engine, gdb, repo, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "delete-author")
	_, otherToken := newVideoAuthor(t, gdb, "delete-other")
	video := seedVideo(t, repo, author.ID, "待删除作品", VideoStatusPublished, time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local))
	path := fmt.Sprintf("/api/video/auth/%d", video.ID)

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, otherToken, nil),
		http.StatusForbidden); message != ErrNotAuthor.Error() {
		t.Fatalf("非作者删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, "", nil),
		http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, "not-a-token", nil),
		http.StatusUnauthorized); message != "invalid or expired token" {
		t.Fatalf("非法令牌删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, "/api/video/auth/abc", token, nil),
		http.StatusBadRequest); message != ErrInvalidVideoID.Error() {
		t.Fatalf("非法标识删除文案错误 got=%q", message)
	}

	deleted := videoJSONRequest(t, engine, http.MethodDelete, path, token, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除状态错误 got=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if deleted.Body.Len() != 0 {
		t.Fatalf("删除响应体应为空 got=%s", deleted.Body.String())
	}

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", video.ID), "", nil),
		http.StatusNotFound); message != "video not found" {
		t.Fatalf("删除后详情文案错误 got=%q", message)
	}
	assertVideoIDs(t, videoListIDs(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)), []uint{})
	assertVideoIDs(t, videoListIDs(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", token, nil)), []uint{})

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, token, nil),
		http.StatusNotFound); message != "video not found" {
		t.Fatalf("重复删除文案错误 got=%q", message)
	}
}

// videoQueryCapture 记录请求内真实执行的 SQL 语句数量
type videoQueryCapture struct {
	counts []int64
}

func (q *videoQueryCapture) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(dbpkg.WithQueryCounter(c.Request.Context()))
		c.Next()
		q.counts = append(q.counts, dbpkg.QueryCount(c.Request.Context()))
	}
}

// videoQueryBudget 断言目标请求的语句数量落在预算内并返回实际值
func videoQueryBudget(t *testing.T, capture *videoQueryCapture, before int, budget int64) int64 {
	t.Helper()
	if len(capture.counts) != before+1 {
		t.Fatalf("应只新增一次请求记录 got=%d want=%d", len(capture.counts), before+1)
	}
	got := capture.counts[len(capture.counts)-1]
	if got < 1 || got > budget {
		t.Fatalf("查询预算超限 got=%d want 1..%d", got, budget)
	}
	return got
}

// 测试目标：验证公开读取端点的真实 SQL 语句数量不随列表长度增长
// 预期效果：列表与详情各自最多四条语句且六条作品时不出现逐条查询
func TestVideoReadEndpointsQueryBudget(t *testing.T) {
	capture := &videoQueryCapture{}
	engine, gdb, repo, _ := newVideoHTTPEngine(t, capture.middleware())
	if err := dbpkg.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}

	author, _ := newVideoAuthor(t, gdb, "budget-author")
	liker, _ := newVideoAuthor(t, gdb, "budget-liker")
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
	var ids []uint
	for index := 0; index < 6; index++ {
		video := seedVideo(t, repo, author.ID, fmt.Sprintf("预算作品 %d", index), VideoStatusPublished, base.Add(time.Duration(index)*time.Minute))
		ids = append(ids, video.ID)
	}
	videoInteractions(t, gdb, ids[0], []uint{liker.ID}, []uint{liker.ID})

	capture.counts = nil
	list := videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("公开列表状态错误 got=%d body=%s", list.Code, list.Body.String())
	}
	if len(videoListIDs(t, list)) != 6 {
		t.Fatalf("公开列表条数错误 body=%s", list.Body.String())
	}
	listQueries := videoQueryBudget(t, capture, 0, 4)
	t.Logf("公开列表语句数量=%d", listQueries)

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", ids[0]), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("公开详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	detailQueries := videoQueryBudget(t, capture, 1, 4)
	t.Logf("公开详情语句数量=%d", detailQueries)
}
