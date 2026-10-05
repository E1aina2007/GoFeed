package sweeper

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"gofeed/internal/testutil"
	"gofeed/internal/user"
	"gofeed/internal/video"
)

type draftMediaMark struct {
	id   uint
	kind video.MediaKind
}

type fakeDraftPurger struct {
	recoverable []uint
	expired     []uint
	listErr     error
	listCalls   []string
	claims      map[uint]*video.DraftPurgeClaim
	claimErr    map[uint]error
	claimCalls  []uint
	renewOK     map[uint]bool
	renewErr    map[uint]error
	marked      []draftMediaMark
	markOK      map[draftMediaMark]bool
	markErr     map[draftMediaMark]error
	hardDeleted []uint
	hardOK      map[uint]bool
	hardErr     map[uint]error
}

func (f *fakeDraftPurger) GetRecoverableDraftPurgeList(_ context.Context, _ int) ([]uint, error) {
	f.listCalls = append(f.listCalls, "recoverable")
	return f.recoverable, f.listErr
}

func (f *fakeDraftPurger) GetExpiredDraftPurgeList(_ context.Context, _ time.Time, _ int) ([]uint, error) {
	f.listCalls = append(f.listCalls, "expired")
	return f.expired, f.listErr
}

func (f *fakeDraftPurger) UpdateDraftPurgeClaim(_ context.Context, id uint, _ time.Time, token string, _ time.Duration) (*video.DraftPurgeClaim, bool, error) {
	f.claimCalls = append(f.claimCalls, id)
	if err := f.claimErr[id]; err != nil {
		return nil, false, err
	}
	claim := f.claims[id]
	if claim == nil {
		return nil, false, nil
	}
	copy := *claim
	copy.Token = token
	return &copy, true, nil
}

func (f *fakeDraftPurger) UpdateDraftPurgeLease(_ context.Context, id uint, _ string, _ time.Duration) (bool, error) {
	if err := f.renewErr[id]; err != nil {
		return false, err
	}
	ok, configured := f.renewOK[id]
	if !configured {
		return true, nil
	}
	return ok, nil
}

func (f *fakeDraftPurger) UpdateDraftMediaPurge(_ context.Context, id uint, _ string, kind video.MediaKind, _ time.Duration) (bool, error) {
	call := draftMediaMark{id: id, kind: kind}
	f.marked = append(f.marked, call)
	if err := f.markErr[call]; err != nil {
		return false, err
	}
	ok, configured := f.markOK[call]
	if !configured {
		return true, nil
	}
	return ok, nil
}

func (f *fakeDraftPurger) RemovePurgedDraft(_ context.Context, id uint, _ string) (bool, error) {
	f.hardDeleted = append(f.hardDeleted, id)
	if err := f.hardErr[id]; err != nil {
		return false, err
	}
	ok, configured := f.hardOK[id]
	if !configured {
		return false, nil
	}
	return ok, nil
}

// 测试目标：验证部分媒体删除失败不会恢复草稿并且不会阻塞同批其他候选项
// 预期效果：已完成槽位会写入检查点，失败草稿不硬删除，后续草稿仍可完成
func TestDraftPurgeJobRunPersistsPartialProgressAndContinues(t *testing.T) {
	playOne := "/static/videos/1/20260810/a.mp4"
	coverOne := "/static/covers/1/20260810/a.png"
	playTwo := "/static/videos/2/20260810/b.mp4"
	purger := &fakeDraftPurger{
		expired: []uint{1, 2},
		claims: map[uint]*video.DraftPurgeClaim{
			1: {DraftID: 1, PlayURL: playOne, CoverURL: coverOne},
			2: {DraftID: 2, PlayURL: playTwo},
		},
		hardOK: map[uint]bool{2: true},
	}
	want := errors.New("disk unavailable")
	remover := &fakeMediaRemover{errFor: map[string]error{coverOne: want}}
	job := NewDraftPurgeJob(purger, remover, 24*time.Hour, 15*time.Minute)
	job.newToken = func() (string, error) { return "token", nil }

	purged, err := job.Run(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("应汇总媒体删除错误 got=%v", err)
	}
	if purged != 1 {
		t.Fatalf("第二条草稿应完成硬删除 got=%d", purged)
	}
	if got, wantURLs := remover.urls, []string{playOne, coverOne, playTwo}; len(got) != len(wantURLs) || got[0] != wantURLs[0] || got[1] != wantURLs[1] || got[2] != wantURLs[2] {
		t.Fatalf("媒体删除顺序错误 got=%v want=%v", got, wantURLs)
	}
	if got, wantMarks := purger.marked, []draftMediaMark{{id: 1, kind: video.MediaVideo}, {id: 2, kind: video.MediaVideo}}; len(got) != len(wantMarks) || got[0] != wantMarks[0] || got[1] != wantMarks[1] {
		t.Fatalf("媒体检查点错误 got=%v want=%v", got, wantMarks)
	}
	if got, wantIDs := purger.hardDeleted, []uint{2}; len(got) != len(wantIDs) || got[0] != wantIDs[0] {
		t.Fatalf("失败草稿不应硬删除 got=%v want=%v", got, wantIDs)
	}
}

// 测试目标：验证重试时只删除尚未写入检查点的媒体槽位
// 预期效果：已删除视频不会重复传给删除器，封面完成后草稿可被硬删除
func TestDraftPurgeJobRunRetriesOnlyUnfinishedMedia(t *testing.T) {
	completed := time.Now().Add(-time.Minute)
	coverURL := "/static/covers/1/20260810/a.png"
	purger := &fakeDraftPurger{
		expired: []uint{1},
		claims: map[uint]*video.DraftPurgeClaim{
			1: {DraftID: 1, PlayURL: "/static/videos/1/20260810/a.mp4", PlayPurgedAt: &completed, CoverURL: coverURL},
		},
		hardOK: map[uint]bool{1: true},
	}
	remover := &fakeMediaRemover{}
	job := NewDraftPurgeJob(purger, remover, time.Hour, time.Minute)
	job.newToken = func() (string, error) { return "token", nil }

	purged, err := job.Run(context.Background())
	if err != nil || purged != 1 {
		t.Fatalf("重试未完成槽位应成功 purged=%d err=%v", purged, err)
	}
	if got, want := remover.urls, []string{coverURL}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("重试不应删除已完成视频 got=%v want=%v", got, want)
	}
	if got, want := purger.marked, []draftMediaMark{{id: 1, kind: video.MediaCover}}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("重试检查点错误 got=%v want=%v", got, want)
	}
}

// 测试目标：验证已完成槽位不会重复删除，丢失租约后停止处理该草稿
// 预期效果：不会删除已标记视频，租约失效时不会删除封面或硬删除记录
func TestDraftPurgeJobRunSkipsCompletedMediaAndLostLease(t *testing.T) {
	completed := time.Now().Add(-time.Minute)
	coverURL := "/static/covers/1/20260810/a.png"
	purger := &fakeDraftPurger{
		expired: []uint{1},
		claims: map[uint]*video.DraftPurgeClaim{
			1: {DraftID: 1, PlayURL: "/static/videos/1/20260810/a.mp4", PlayPurgedAt: &completed, CoverURL: coverURL},
		},
		renewOK: map[uint]bool{1: false},
	}
	remover := &fakeMediaRemover{}
	job := NewDraftPurgeJob(purger, remover, time.Hour, time.Minute)
	job.newToken = func() (string, error) { return "token", nil }

	purged, err := job.Run(context.Background())
	if err != nil || purged != 0 {
		t.Fatalf("丢失租约应平静停止 purged=%d err=%v", purged, err)
	}
	if len(remover.urls) != 0 || len(purger.marked) != 0 || len(purger.hardDeleted) != 0 {
		t.Fatalf("丢失租约后不应继续删除 urls=%v marked=%v hard=%v", remover.urls, purger.marked, purger.hardDeleted)
	}
}

type fakeVideoPurger struct {
	cutoff       time.Time
	videos       []video.Video
	listErr      error
	hardDelete   []uint
	deleteResult map[uint]bool
	deleteErr    error
}

func (f *fakeVideoPurger) GetExpiredDeletedVideoList(_ context.Context, cutoff time.Time) ([]video.Video, error) {
	f.cutoff = cutoff
	return f.videos, f.listErr
}

func (f *fakeVideoPurger) RemoveExpiredVideo(_ context.Context, id uint, cutoff time.Time) (bool, error) {
	f.cutoff = cutoff
	f.hardDelete = append(f.hardDelete, id)
	return f.deleteResult[id], f.deleteErr
}

type fakeMediaRemover struct {
	urls   []string
	errFor map[string]error
}

func (f *fakeMediaRemover) Remove(_ context.Context, publicURL string) error {
	f.urls = append(f.urls, publicURL)
	return f.errFor[publicURL]
}

// 测试目标：验证媒体删除失败时视频记录会保留以便下次重试
// 预期效果：任务返回错误且不调用对应视频的硬删除
func TestVideoPurgeJobRunRetainsRecordWhenMediaRemovalFails(t *testing.T) {
	coverURL := "/static/covers/1/20260810/a.png"
	purger := &fakeVideoPurger{
		videos:       []video.Video{{ID: 1, PlayURL: "/static/videos/1/20260810/a.mp4", CoverURL: coverURL}},
		deleteResult: map[uint]bool{1: true},
	}
	want := errors.New("disk unavailable")
	remover := &fakeMediaRemover{errFor: map[string]error{coverURL: want}}

	purged, err := NewVideoPurgeJob(purger, remover, time.Hour).Run(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("应透传媒体删除错误 got=%v", err)
	}
	if purged != 0 || len(purger.hardDelete) != 0 {
		t.Fatalf("删除媒体失败时不应硬删除记录 purged=%d hardDelete=%v", purged, purger.hardDelete)
	}
}

type fakeMediaReferenceReader struct {
	urls []string
	err  error
}

func (f *fakeMediaReferenceReader) ListReferencedMediaURLs(context.Context) ([]string, error) {
	return f.urls, f.err
}

type fakeMediaCandidateLister struct {
	urls   []string
	cutoff time.Time
	err    error
}

func (f *fakeMediaCandidateLister) ListMediaCandidates(_ context.Context, cutoff time.Time, _ int) ([]string, error) {
	f.cutoff = cutoff
	return f.urls, f.err
}

// 测试目标：验证孤儿清扫保留所有数据库引用并继续处理后续候选
// 预期效果：引用对象不删除，单个删除失败会汇总返回且其他未引用对象仍被回收
func TestMediaOrphanPurgeJobRunPreservesReferencesAndContinues(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	referenced := "/static/videos/1/20260927/kept_0123456789abcdef0123456789abcdef.mp4"
	reclaimed := "/static/covers/1/20260927/reclaimed_0123456789abcdef0123456789abcdef.png"
	failed := "/static/avatars/1/20260927/failed_0123456789abcdef0123456789abcdef.png"
	removeErr := errors.New("disk unavailable")
	candidates := &fakeMediaCandidateLister{urls: []string{referenced, reclaimed, failed}}
	remover := &fakeMediaRemover{errFor: map[string]error{failed: removeErr}}
	job := NewMediaOrphanPurgeJob(
		&fakeMediaReferenceReader{urls: []string{"https://example.test" + referenced}},
		candidates,
		remover,
		24*time.Hour,
	)
	job.now = func() time.Time { return now }

	purged, err := job.Run(context.Background())
	if !errors.Is(err, removeErr) {
		t.Fatalf("应汇总删除失败 got=%v", err)
	}
	if purged != 1 {
		t.Fatalf("应只回收一个成功对象 got=%d", purged)
	}
	if want := now.Add(-24 * time.Hour); !candidates.cutoff.Equal(want) {
		t.Fatalf("候选截止时间错误 got=%v want=%v", candidates.cutoff, want)
	}
	if got, want := remover.urls, []string{reclaimed, failed}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("删除列表错误 got=%v want=%v", got, want)
	}
}

// 测试目标：为 sweeper 仓储用例初始化隔离的真实 MySQL 数据库
// 预期效果：媒体引用查询与迁移后的 users/videos 表结构保持一致
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：验证媒体引用查询包含视频和头像，并保留软删除保留期中的引用
// 预期效果：孤儿清扫不会删除活跃或尚未硬删除记录仍持有的本地媒体
func TestMediaReferenceRepositoryListReferencedMediaURLsIncludesSoftDeletedRecords(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	activeAvatar := "/static/avatars/1/20260928/active_0123456789abcdef0123456789abcdef.png"
	deletedAvatar := "/static/avatars/2/20260928/deleted_0123456789abcdef0123456789abcdef.png"
	if err := db.Create(&user.User{Username: "active", Password: "hash", AvatarURL: activeAvatar}).Error; err != nil {
		t.Fatalf("创建活跃用户失败: %v", err)
	}
	deletedUser := &user.User{Username: "deleted", Password: "hash", AvatarURL: deletedAvatar}
	if err := db.Create(deletedUser).Error; err != nil {
		t.Fatalf("创建软删用户失败: %v", err)
	}
	if err := db.Delete(deletedUser).Error; err != nil {
		t.Fatalf("软删除用户失败: %v", err)
	}

	activePlay := "/static/videos/1/20260928/play_0123456789abcdef0123456789abcdef.mp4"
	deletedCover := "/static/covers/2/20260928/cover_0123456789abcdef0123456789abcdef.png"
	activeVideo := &video.Video{AuthorID: 1, Title: "active", PlayURL: activePlay, Status: video.VideoStatusDraft}
	if err := db.Create(activeVideo).Error; err != nil {
		t.Fatalf("创建活跃视频失败: %v", err)
	}
	deletedVideo := &video.Video{AuthorID: 2, Title: "deleted", CoverURL: deletedCover, Status: video.VideoStatusDraft}
	if err := db.Create(deletedVideo).Error; err != nil {
		t.Fatalf("创建软删视频失败: %v", err)
	}
	if err := db.Delete(deletedVideo).Error; err != nil {
		t.Fatalf("软删除视频失败: %v", err)
	}

	urls, err := NewMediaReferenceRepository(db).ListReferencedMediaURLs(ctx)
	if err != nil {
		t.Fatalf("ListReferencedMediaURLs: %v", err)
	}
	got := make(map[string]struct{}, len(urls))
	for _, value := range urls {
		got[value] = struct{}{}
	}
	for _, want := range []string{activeAvatar, deletedAvatar, activePlay, deletedCover} {
		if _, exists := got[want]; !exists {
			t.Fatalf("媒体引用缺失 want=%s got=%v", want, urls)
		}
	}
}
