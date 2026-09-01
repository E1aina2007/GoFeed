package sweeper

import (
	"context"
	"errors"
	"testing"
	"time"

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

// 测试目标：验证仓储提供的到期 rejected 候选会复用草稿清扫主流程
// 预期效果：清扫器不区分来源状态，仍按租约、媒体检查点和硬删除顺序完成回收
func TestDraftPurgeJobRunPurgesRejectedCandidate(t *testing.T) {
	playURL := "/static/videos/1/20260810/rejected.mp4"
	coverURL := "/static/covers/1/20260810/rejected.png"
	purger := &fakeDraftPurger{
		expired: []uint{1},
		claims: map[uint]*video.DraftPurgeClaim{
			1: {DraftID: 1, PlayURL: playURL, CoverURL: coverURL},
		},
		hardOK: map[uint]bool{1: true},
	}
	remover := &fakeMediaRemover{}
	job := NewDraftPurgeJob(purger, remover, 24*time.Hour, 15*time.Minute)
	job.newToken = func() (string, error) { return "rejected-token", nil }

	purged, err := job.Run(context.Background())
	if err != nil || purged != 1 {
		t.Fatalf("rejected 候选清扫失败 purged=%d err=%v", purged, err)
	}
	if got, want := remover.urls, []string{playURL, coverURL}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rejected 媒体删除顺序错误 got=%v want=%v", got, want)
	}
	if got, want := purger.hardDeleted, []uint{1}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("rejected 候选未硬删除 got=%v want=%v", got, want)
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

// 测试目标：验证新到期草稿不能挤占清扫中草稿的重试配额
// 预期效果：候选交错合并且去重，达到批次上限后两类均有机会执行
func TestInterleaveDraftPurgeCandidates(t *testing.T) {
	got := interleaveDraftPurgeCandidates([]uint{1, 2, 3}, []uint{2, 4, 5}, 4)
	want := []uint{1, 2, 4, 3}
	if len(got) != len(want) {
		t.Fatalf("候选数量错误 got=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("候选顺序错误 got=%v want=%v", got, want)
		}
	}
}

// 测试目标：验证草稿清扫任务要求完整依赖和有效租约
// 预期效果：缺失仓储、删除器或租约时返回明确错误
func TestDraftPurgeJobRunRequiresDependencies(t *testing.T) {
	if _, err := NewDraftPurgeJob(nil, &fakeMediaRemover{}, time.Hour, time.Minute).Run(context.Background()); !errors.Is(err, ErrDraftPurgerUnavailable) {
		t.Fatalf("nil 草稿仓储错误不正确: %v", err)
	}
	if _, err := NewDraftPurgeJob(&fakeDraftPurger{}, nil, time.Hour, time.Minute).Run(context.Background()); !errors.Is(err, ErrMediaRemoverUnavailable) {
		t.Fatalf("nil 媒体删除器错误不正确: %v", err)
	}
	if _, err := NewDraftPurgeJob(&fakeDraftPurger{}, &fakeMediaRemover{}, time.Hour, 0).Run(context.Background()); !errors.Is(err, ErrInvalidDraftPurgeLease) {
		t.Fatalf("无效租约错误不正确: %v", err)
	}
	for _, retention := range []time.Duration{0, -time.Hour} {
		if _, err := NewDraftPurgeJob(&fakeDraftPurger{}, &fakeMediaRemover{}, retention, time.Minute).Run(context.Background()); !errors.Is(err, ErrInvalidDraftPurgeRetention) {
			t.Fatalf("无效保留期错误 retention=%s err=%v", retention, err)
		}
	}
}
