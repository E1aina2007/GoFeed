package cache

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"gofeed/internal/config"
)

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
