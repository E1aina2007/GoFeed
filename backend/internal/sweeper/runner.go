package sweeper

import (
	"context"
	"time"
)

// RunEvery 周期执行 fn，直到 ctx 取消；首次执行由调用方自行触发
func RunEvery(ctx context.Context, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn()
		}
	}
}
