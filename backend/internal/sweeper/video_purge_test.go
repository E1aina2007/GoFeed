package sweeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"gofeed/internal/video"
)

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

// 测试目标：验证到期视频先清理两类媒体，再硬删除记录。
// 预期效果：任务传递正确截止时间并统计实际删除的记录数。
func TestVideoPurgeJobRunRemovesMediaThenHardDeletes(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	purger := &fakeVideoPurger{
		videos: []video.Video{
			{ID: 1, PlayURL: "/static/videos/1/20260810/a.mp4", CoverURL: "/static/covers/1/20260810/a.png"},
			{ID: 2, PlayURL: "/static/videos/2/20260810/b.mp4", CoverURL: "/static/covers/2/20260810/b.png"},
		},
		deleteResult: map[uint]bool{1: true, 2: true},
	}
	remover := &fakeMediaRemover{}
	job := NewVideoPurgeJob(purger, remover, 7*24*time.Hour)
	job.now = func() time.Time { return now }

	purged, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if purged != 2 {
		t.Fatalf("应硬删除 2 条视频 got=%d", purged)
	}
	if want := now.Add(-7 * 24 * time.Hour); !purger.cutoff.Equal(want) {
		t.Fatalf("cutoff 错误 got=%v want=%v", purger.cutoff, want)
	}
	if got, want := purger.hardDelete, []uint{1, 2}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("硬删除顺序错误 got=%v want=%v", got, want)
	}
	if got, want := remover.urls, []string{
		"/static/videos/1/20260810/a.mp4", "/static/covers/1/20260810/a.png",
		"/static/videos/2/20260810/b.mp4", "/static/covers/2/20260810/b.png",
	}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("媒体删除顺序错误 got=%v want=%v", got, want)
	}
}

// 测试目标：验证媒体删除失败时视频记录会保留以便下次重试。
// 预期效果：任务返回错误且不调用对应视频的硬删除。
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

// 测试目标：验证视频清扫任务要求完整依赖。
// 预期效果：缺失仓储或媒体删除器返回明确错误。
func TestVideoPurgeJobRunRequiresDependencies(t *testing.T) {
	if _, err := NewVideoPurgeJob(nil, &fakeMediaRemover{}, time.Hour).Run(context.Background()); !errors.Is(err, ErrVideoPurgerUnavailable) {
		t.Fatalf("nil 视频仓储错误不正确: %v", err)
	}
	if _, err := NewVideoPurgeJob(&fakeVideoPurger{}, nil, time.Hour).Run(context.Background()); !errors.Is(err, ErrMediaRemoverUnavailable) {
		t.Fatalf("nil 媒体删除器错误不正确: %v", err)
	}
}
