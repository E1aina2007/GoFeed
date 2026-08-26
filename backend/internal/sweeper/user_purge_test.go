package sweeper

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 测试目标：模拟过期用户清理仓储并记录截止时间
// 预期效果：返回预设的清理数量和错误
type fakePurger struct {
	cutoff time.Time
	purged int64
	err    error
}

// 测试目标：模拟执行清理操作
// 预期效果：保存接收到的截止时间并透传预设结果
func (f *fakePurger) RemoveExpiredUsers(_ context.Context, cutoff time.Time) (int64, error) {
	f.cutoff = cutoff
	return f.purged, f.err
}

// 测试目标：验证清理任务按保留时长计算截止时间并透传删除数量
// 预期效果：仓储收到当前时间减去保留时长的截止时间，任务返回仓储删除数量
func TestUserPurgeJobRunComputesCutoff(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	purger := &fakePurger{purged: 3}
	job := NewUserPurgeJob(purger, 7*24*time.Hour)
	job.now = func() time.Time { return now }

	purged, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if purged != 3 {
		t.Fatalf("应透传删除数 got=%d", purged)
	}
	wantCutoff := now.Add(-7 * 24 * time.Hour)
	if !purger.cutoff.Equal(wantCutoff) {
		t.Fatalf("cutoff 计算错误 got=%v want=%v", purger.cutoff, wantCutoff)
	}
}

// 测试目标：验证清理任务不会吞掉仓储错误
// 预期效果：任务返回与仓储完全相同的错误
func TestUserPurgeJobRunPropagatesError(t *testing.T) {
	want := errors.New("db down")
	job := NewUserPurgeJob(&fakePurger{err: want}, time.Hour)
	if _, err := job.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("应透传仓储错误 got=%v", err)
	}
}

// 测试目标：验证清理任务缺少仓储依赖时的保护行为
// 预期效果：任务返回明确的仓储不可用错误
func TestUserPurgeJobRunRequiresPurger(t *testing.T) {
	job := NewUserPurgeJob(nil, time.Hour)
	if _, err := job.Run(context.Background()); !errors.Is(err, ErrUserPurgerUnavailable) {
		t.Fatalf("nil 仓储应返回 ErrUserPurgerUnavailable got=%v", err)
	}
}
