package sweeper
import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePurger struct {
	cutoff time.Time
	purged int64
	err    error
}

func (f *fakePurger) PurgeExpired(_ context.Context, cutoff time.Time) (int64, error) {
	f.cutoff = cutoff
	return f.purged, f.err
}

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

func TestUserPurgeJobRunPropagatesError(t *testing.T) {
	want := errors.New("db down")
	job := NewUserPurgeJob(&fakePurger{err: want}, time.Hour)
	if _, err := job.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("应透传仓储错误 got=%v", err)
	}
}

func TestUserPurgeJobRunRequiresPurger(t *testing.T) {
	job := NewUserPurgeJob(nil, time.Hour)
	if _, err := job.Run(context.Background()); !errors.Is(err, ErrUserPurgerUnavailable) {
		t.Fatalf("nil 仓储应返回 ErrUserPurgerUnavailable got=%v", err)
	}
}
