package sweeper

import (
	"context"
	"errors"
	"testing"
	"time"
)

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

// 测试目标：验证孤儿清扫拒绝不完整依赖和非法保留期
// 预期效果：任务在执行删除前返回对应的可识别错误
func TestMediaOrphanPurgeJobRunRequiresDependencies(t *testing.T) {
	if _, err := NewMediaOrphanPurgeJob(nil, &fakeMediaCandidateLister{}, &fakeMediaRemover{}, time.Hour).Run(context.Background()); !errors.Is(err, ErrMediaReferenceReaderUnavailable) {
		t.Fatalf("缺少引用读取器错误不正确: %v", err)
	}
	if _, err := NewMediaOrphanPurgeJob(&fakeMediaReferenceReader{}, nil, &fakeMediaRemover{}, time.Hour).Run(context.Background()); !errors.Is(err, ErrMediaCandidateListerUnavailable) {
		t.Fatalf("缺少候选读取器错误不正确: %v", err)
	}
	if _, err := NewMediaOrphanPurgeJob(&fakeMediaReferenceReader{}, &fakeMediaCandidateLister{}, nil, time.Hour).Run(context.Background()); !errors.Is(err, ErrMediaRemoverUnavailable) {
		t.Fatalf("缺少删除器错误不正确: %v", err)
	}
	if _, err := NewMediaOrphanPurgeJob(&fakeMediaReferenceReader{}, &fakeMediaCandidateLister{}, &fakeMediaRemover{}, 0).Run(context.Background()); !errors.Is(err, ErrInvalidMediaOrphanRetention) {
		t.Fatalf("非法保留期错误不正确: %v", err)
	}
}
