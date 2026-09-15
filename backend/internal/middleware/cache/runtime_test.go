package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gofeed/internal/config"
	rediscfg "gofeed/internal/redis"
	"gofeed/internal/redis/redistest"
)

// 测试目标：验证首次连接失败在冷却期内不重复拨号，冷却后仅一个调用负责恢复探测
// 预期效果：并发调用复用最近错误，探测成功后 Runtime 恢复可用
func TestRuntimeCoolsDownAndUsesSingleRecoveryProbe(t *testing.T) {
	memory := redistest.New(t)
	initialFailure := errors.New("redis unavailable")
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int32

	dial := func(context.Context, config.RedisConfig) (rediscfg.Client, error) {
		switch calls.Add(1) {
		case 1:
			return nil, initialFailure
		case 2:
			close(probeStarted)
			<-releaseProbe
			return memory, nil
		default:
			return nil, errors.New("unexpected redis dial")
		}
	}
	runtime := NewRuntime(
		config.RedisConfig{},
		WithDialer(dial),
		WithReconnectCooldown(time.Hour),
	)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseProbe) })
		_ = runtime.Close()
	})

	if err := runtime.EnsureConnected(context.Background()); !errors.Is(err, initialFailure) {
		t.Fatalf("首次连接错误错误 got=%v", err)
	}
	if err := runtime.EnsureConnected(context.Background()); !errors.Is(err, initialFailure) {
		t.Fatalf("冷却期应返回首次错误 got=%v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("冷却期拨号次数错误 got=%d want=1", got)
	}

	runtime.mu.Lock()
	runtime.retryAfter = time.Time{}
	runtime.mu.Unlock()
	probeResult := make(chan error, 1)
	go func() { probeResult <- runtime.EnsureConnected(context.Background()) }()
	<-probeStarted

	if err := runtime.EnsureConnected(context.Background()); !errors.Is(err, initialFailure) {
		t.Fatalf("并发调用应复用最近错误 got=%v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("恢复期只能有一个探测拨号 got=%d want=2", got)
	}

	releaseOnce.Do(func() { close(releaseProbe) })
	if err := <-probeResult; err != nil {
		t.Fatalf("恢复探测失败: %v", err)
	}
	if err := runtime.Ping(context.Background()); err != nil {
		t.Fatalf("恢复后 Ping 失败: %v", err)
	}
}

// 测试目标：验证 Close 关闭当前客户端并永久拒绝后续操作
// 预期效果：关闭后 Runtime 返回 ErrClosed，不会重新建立 Redis 连接
func TestRuntimeCloseRejectsFurtherOperations(t *testing.T) {
	memory := redistest.New(t)
	var calls atomic.Int32
	runtime := NewRuntime(config.RedisConfig{}, WithDialer(func(context.Context, config.RedisConfig) (rediscfg.Client, error) {
		calls.Add(1)
		return memory, nil
	}))

	if err := runtime.EnsureConnected(context.Background()); err != nil {
		t.Fatalf("启动连接失败: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("关闭 Runtime 失败: %v", err)
	}
	if err := runtime.Ping(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 Ping 错误 got=%v want=%v", err, ErrClosed)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("关闭后不应重新拨号 got=%d want=1", got)
	}
}
