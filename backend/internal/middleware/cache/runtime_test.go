package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
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
		t.Fatalf("首次连接错误 got=%v", err)
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

// 测试目标：为 Redis 故障恢复用例分配独立回环端口
// 预期效果：临时 Redis 不与本机常驻服务共享监听地址
func dedicatedRedisPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配 Redis 测试端口失败: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// 测试目标：在独立端口启动真实 redis-server 进程
// 预期效果：进程不加载持久化数据且标准输出可用于失败诊断
func startDedicatedRedis(t *testing.T, port int) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	executable, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skipf("未找到 redis-server，集成测试跳过: %v", err)
	}
	output := &bytes.Buffer{}
	cmd := exec.Command(executable,
		"--port", strconv.Itoa(port),
		"--bind", "127.0.0.1",
		"--save", "",
		"--appendonly", "no",
	)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动专用 redis-server 失败: %v output=%s", err, output.String())
	}
	return cmd, output
}

// 测试目标：强制结束专用 redis-server 进程
// 预期效果：只影响当前测试启动的子进程，重复清理不会报错
func stopDedicatedRedis(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("结束专用 redis-server 失败: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("被强制结束的 redis-server 不应返回成功")
	}
}

// 测试目标：等待刚启动的专用 Redis 进入监听状态
// 预期效果：子进程启动时序不影响后续故障恢复断言，超时会返回最后一次连接错误
func waitForDedicatedRedis(t *testing.T, runtime *Runtime, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		lastErr = runtime.EnsureConnected(ctx)
		cancel()
		if lastErr == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待专用 redis-server 启动超时: %v", lastErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// 测试目标：验证真实 Redis 进程中断后 cache Runtime 按冷却与单探针恢复
// 预期效果：故障调用报错，重启独立服务后同一 Runtime 可重新 Ping 和读写测试键
func TestRuntimeRecoversAfterDedicatedRedisRestart(t *testing.T) {
	if os.Getenv("GOFEED_REDIS_PROCESS_INTEGRATION") != "1" {
		t.Skip("集成跳过：设置 GOFEED_REDIS_PROCESS_INTEGRATION=1 后运行")
	}
	port := dedicatedRedisPort(t)
	first, firstOutput := startDedicatedRedis(t, port)
	t.Cleanup(func() { stopDedicatedRedis(t, first) })
	runtime := NewRuntime(
		config.RedisConfig{Host: "127.0.0.1", Port: port},
		WithReconnectCooldown(100*time.Millisecond),
	)
	t.Cleanup(func() { _ = runtime.Close() })
	waitForDedicatedRedis(t, runtime, 5*time.Second)
	connectContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	key := fmt.Sprintf("gofeed:test:cache-runtime:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupContext, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_, _ = runtime.Del(cleanupContext, key)
	})
	if err := runtime.Set(connectContext, key, "before-restart", time.Minute); err != nil {
		t.Fatalf("重启前写入失败: %v", err)
	}

	stopDedicatedRedis(t, first)
	failureContext, stopFailure := context.WithTimeout(t.Context(), 2*time.Second)
	err := runtime.Ping(failureContext)
	stopFailure()
	if err == nil {
		t.Fatal("Redis 进程结束后 Ping 应失败")
	}

	second, secondOutput := startDedicatedRedis(t, port)
	t.Cleanup(func() { stopDedicatedRedis(t, second) })
	time.Sleep(150 * time.Millisecond)
	recoveryContext, stopRecovery := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopRecovery()
	if err := runtime.Ping(recoveryContext); err != nil {
		t.Fatalf("Redis 重启后 Runtime 未恢复: %v first=%s second=%s", err, firstOutput.String(), secondOutput.String())
	}
	if err := runtime.Set(recoveryContext, key, "after-restart", time.Minute); err != nil {
		t.Fatalf("恢复后写入失败: %v", err)
	}
	if value, err := runtime.Get(recoveryContext, key); err != nil || value != "after-restart" {
		t.Fatalf("恢复后读取错误 value=%q err=%v", value, err)
	}
}
