package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"gofeed/internal/config"
	"gofeed/internal/middleware/cache"
	"gofeed/internal/middleware/ratelimit"

	"github.com/gin-gonic/gin"
)

const (
	// rateLimitFailureCooldown 缩短故障冷却，让用例在可接受时间内覆盖单探针恢复
	rateLimitFailureCooldown = 200 * time.Millisecond
	// rateLimitProbeLimit 是本用例使用的登录窗口上限
	rateLimitProbeLimit int64 = 3
)

// 测试目标：为限流故障用例分配独立回环端口
// 预期效果：临时 Redis 不与本机常驻服务共享监听地址
func rateLimitDedicatedPort(t *testing.T) int {
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
func startRateLimitRedis(t *testing.T, port int) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	executable, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skipf("未找到 redis-server，集成测试跳过: %v", err)
	}
	output := &strings.Builder{}
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

// 测试目标：强制结束用例启动的专用 redis-server 进程
// 预期效果：只影响当前用例的子进程，重复清理不会报错
func stopRateLimitRedis(t *testing.T, cmd *exec.Cmd) {
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

// 测试目标：等待限流运行时连上刚启动的专用 Redis
// 预期效果：子进程启动时序不影响后续断言，超时返回最后一次连接错误
func waitRateLimitRedis(t *testing.T, runtime *cache.Runtime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		err := runtime.EnsureConnected(ctx)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待专用 redis-server 启动超时: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// 测试目标：构造带固定 ClientIP 的登录限流请求
// 预期效果：用例只占用自己来源地址的限流键，不与真实流量互相污染
func rateLimitLoginRequest(clientIP string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/user/login", strings.NewReader("{"))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = clientIP + ":8443"
	return request
}

// 测试目标：清空专用 Redis 中本用例使用的限流键
// 预期效果：复位固定窗口，避免上一段断言影响恢复后的计数
func resetRateLimitKey(t *testing.T, runtime *cache.Runtime, clientIP string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := runtime.Del(ctx, "rl:v1:"+ratelimit.LoginAction+":"+clientIP); err != nil {
		t.Fatalf("清理专用 Redis 限流键失败: %v", err)
	}
}

// 测试目标：验证 Redis 运行中故障后登录路由按 fail-open 继续业务
// 预期效果：故障期间不返回 429 与 5xx，服务恢复后固定窗口继续生效
func TestLoginRateLimitFailsOpenAndRecoversWithDedicatedRedis(t *testing.T) {
	if os.Getenv("GOFEED_REDIS_PROCESS_INTEGRATION") != "1" {
		t.Skip("集成跳过：设置 GOFEED_REDIS_PROCESS_INTEGRATION=1 后运行")
	}
	port := rateLimitDedicatedPort(t)
	first, firstOutput := startRateLimitRedis(t, port)
	t.Cleanup(func() { stopRateLimitRedis(t, first) })

	runtime := cache.NewRuntime(
		config.RedisConfig{Host: "127.0.0.1", Port: port},
		cache.WithReconnectCooldown(rateLimitFailureCooldown),
	)
	t.Cleanup(func() { _ = runtime.Close() })
	waitRateLimitRedis(t, runtime)

	now := time.Now().UnixNano()
	clientIP := fmt.Sprintf("198.18.%d.%d", (now>>8)&255, now&255)
	// 测试目标：使用与登录路由相同的限流装配并收紧窗口上限
	// 预期效果：用例无需打满生产窗口即可覆盖窗口内、超限与故障三段行为
	engine := rateLimitLoginEngine(runtime, rateLimitProbeLimit, time.Minute)

	// 测试目标：确认专用实例上的固定窗口真实生效
	// 预期效果：窗口内保留既有 400，超限返回固定 429
	assertRateLimitWindow(t, engine, clientIP, rateLimitProbeLimit)

	// 测试目标：在窗口未过期时中断专用 Redis
	// 预期效果：故障请求按 fail-open 进入业务处理，不再返回 429
	stopRateLimitRedis(t, first)
	time.Sleep(rateLimitFailureCooldown + 100*time.Millisecond)
	assertRateLimitFailsOpen(t, engine, clientIP, firstOutput)

	// 测试目标：专用 Redis 恢复后同一运行时重新接管限流
	// 预期效果：窗口重新计数并再次返回固定 429
	second, secondOutput := startRateLimitRedis(t, port)
	t.Cleanup(func() { stopRateLimitRedis(t, second) })
	waitRateLimitRedis(t, runtime)
	resetRateLimitKey(t, runtime, clientIP)
	assertRateLimitWindowWithOutput(t, engine, clientIP, rateLimitProbeLimit, firstOutput, secondOutput)
}

// 测试目标：装配与真实登录路由相同的限流装配
// 预期效果：限流中间件在业务处理前执行，业务处理器保持既有 400 语义
func rateLimitLoginEngine(runtime *cache.Runtime, limit int64, window time.Duration) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.POST("/api/user/login",
		ratelimit.Limit(runtime, ratelimit.LoginAction, limit, window),
		func(c *gin.Context) { c.AbortWithStatus(http.StatusBadRequest) },
	)
	return engine
}

// 测试目标：在窗口内连续请求并确认超限响应
// 预期效果：窗口内保留既有业务状态，超限返回固定 429
func assertRateLimitWindow(t *testing.T, engine http.Handler, clientIP string, limit int64) {
	t.Helper()
	assertRateLimitWindowWithOutput(t, engine, clientIP, limit, nil, nil)
}

// 测试目标：在窗口内连续请求并确认超限响应，失败时附带子进程输出
// 预期效果：断言失败可直接定位是限流未生效还是专用实例未启动
func assertRateLimitWindowWithOutput(t *testing.T, engine http.Handler, clientIP string, limit int64, outputs ...*strings.Builder) {
	t.Helper()
	for attempt := int64(0); attempt < limit; attempt++ {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, rateLimitLoginRequest(clientIP))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("窗口内第 %d 次请求状态错误 got=%d want=%d outputs=%s",
				attempt+1, response.Code, http.StatusBadRequest, collectOutputs(outputs))
		}
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, rateLimitLoginRequest(clientIP))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("超限响应状态错误 got=%d want=%d outputs=%s",
			response.Code, http.StatusTooManyRequests, collectOutputs(outputs))
	}
	if body := response.Body.String(); body != `{"error":"rate limit exceeded"}` {
		t.Fatalf("超限响应体错误 got=%s", body)
	}
}

// 测试目标：确认限流故障期间请求按 fail-open 放行
// 预期效果：不出现 429 与 5xx，业务处理仍以既有 400 响应非法请求体
func assertRateLimitFailsOpen(t *testing.T, engine http.Handler, clientIP string, outputs ...*strings.Builder) {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, rateLimitLoginRequest(clientIP))
		if response.Code == http.StatusTooManyRequests {
			t.Fatalf("Redis 故障时不应返回 429 attempt=%d outputs=%s", attempt+1, collectOutputs(outputs))
		}
		if response.Code >= http.StatusInternalServerError {
			t.Fatalf("Redis 故障时不应返回 5xx got=%d attempt=%d outputs=%s",
				response.Code, attempt+1, collectOutputs(outputs))
		}
		if response.Code != http.StatusBadRequest {
			t.Fatalf("故障放行后应进入业务处理并返回 400 got=%d attempt=%d outputs=%s",
				response.Code, attempt+1, collectOutputs(outputs))
		}
	}
	if value := responseHeader(t, engine, clientIP, "Retry-After"); value != "" {
		t.Fatalf("fail-open 响应不应携带 Retry-After got=%q", value)
	}
}

// 测试目标：读取一次请求的响应头用于断言
// 预期效果：返回指定响应头，缺失时为空字符串
func responseHeader(t *testing.T, engine http.Handler, clientIP, header string) string {
	t.Helper()
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, rateLimitLoginRequest(clientIP))
	return response.Header().Get(header)
}

// 测试目标：汇总专用 Redis 子进程输出供失败诊断
// 预期效果：无输出时返回占位文本，避免空字符串掩盖原因
func collectOutputs(outputs []*strings.Builder) string {
	parts := make([]string, 0, len(outputs))
	for index, output := range outputs {
		if output == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("#%d=%s", index+1, output.String()))
	}
	if len(parts) == 0 {
		return "(无子进程输出)"
	}
	return strings.Join(parts, " ")
}
