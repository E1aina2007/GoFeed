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
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"gofeed/internal/config"
	"gofeed/internal/middleware/cache"
	"gofeed/internal/middleware/ratelimit"
)

// 测试目标：在显式启用时读取真实 Redis 路由回归配置
// 预期效果：环境变量优先且缺少 Redis 时明确跳过
func realRedisRateLimitConfig(t *testing.T) config.RedisConfig {
	t.Helper()
	if os.Getenv("GOFEED_REDIS_INTEGRATION") != "1" {
		t.Skip("集成跳过：设置 GOFEED_REDIS_INTEGRATION=1 并配置本机 Redis 后运行")
	}
	values, err := godotenv.Read("../../.env")
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("读取本地 Redis 测试配置失败: %v", err)
	}
	for _, key := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_DB", "REDIS_PASSWORD"} {
		if _, exists := os.LookupEnv(key); !exists {
			if value, ok := values[key]; ok {
				t.Setenv(key, value)
			}
		}
	}
	path := "../../configs/config.dev.yaml"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = "../../configs/config.example.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("加载 Redis 测试配置失败: %v", err)
	}
	return cfg.Redis
}

// 测试目标：构造带指定 ClientIP 的限流路由请求
// 预期效果：真实 Redis 键仅属于当前测试 IP，路由会在 JSON 绑定前执行限流
func realRedisRateLimitRequest(path, clientIP string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{"))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = clientIP + ":8443"
	return request
}

// 测试目标：验证真实 Redis 在注册和登录路由上执行固定窗口限流
// 预期效果：窗口内请求保留既有 400，下一次返回固定 429、正整数 Retry-After 且随机测试键被清理
func TestRegisterAndLoginRateLimitAgainstRealRedis(t *testing.T) {
	redisRuntime := cache.NewRuntime(realRedisRateLimitConfig(t))
	connectContext, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := redisRuntime.EnsureConnected(connectContext); err != nil {
		t.Skipf("Redis 不可达，集成测试跳过: %v", err)
	}
	t.Cleanup(func() { _ = redisRuntime.Close() })
	engine := New(nil, false, Options{RateLimitCache: redisRuntime})

	for index, testCase := range []struct {
		name   string
		path   string
		action string
		limit  int64
	}{
		{name: "register", path: "/api/user/register", action: ratelimit.RegisterAction, limit: ratelimit.RegisterMaxRequests},
		{name: "login", path: "/api/user/login", action: ratelimit.LoginAction, limit: ratelimit.LoginMaxRequests},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Now().UnixNano() + int64(index)
			clientIP := fmt.Sprintf("198.18.%d.%d", (now>>8)&255, now&255)
			key := "rl:v1:" + testCase.action + ":" + clientIP
			t.Cleanup(func() {
				cleanupContext, done := context.WithTimeout(context.Background(), 3*time.Second)
				defer done()
				if _, err := redisRuntime.Del(cleanupContext, key); err != nil {
					t.Errorf("清理真实 Redis 限流测试键失败: %v", err)
				}
			})

			for attempt := int64(0); attempt < testCase.limit; attempt++ {
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, realRedisRateLimitRequest(testCase.path, clientIP))
				if response.Code != http.StatusBadRequest {
					t.Fatalf("窗口内第 %d 次请求状态错误 got=%d want=%d", attempt+1, response.Code, http.StatusBadRequest)
				}
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, realRedisRateLimitRequest(testCase.path, clientIP))
			if response.Code != http.StatusTooManyRequests {
				t.Fatalf("超限响应状态错误 got=%d want=%d", response.Code, http.StatusTooManyRequests)
			}
			if retryAfter, err := strconv.Atoi(response.Header().Get("Retry-After")); err != nil || retryAfter < 1 {
				t.Fatalf("Retry-After 应为正整数 got=%q err=%v", response.Header().Get("Retry-After"), err)
			}
			if body := response.Body.String(); body != `{"error":"rate limit exceeded"}` {
				t.Fatalf("超限响应体错误 got=%s", body)
			}
		})
	}
}

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

type rateLimitCall struct {
	keys []string
	args []any
}

type routeRateLimitCache struct {
	mu     sync.Mutex
	result any
	err    error
	calls  []rateLimitCall
}

func (c *routeRateLimitCache) Eval(_ context.Context, _ string, keys []string, args ...any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, rateLimitCall{
		keys: append([]string(nil), keys...),
		args: append([]any(nil), args...),
	})
	return c.result, c.err
}

func (c *routeRateLimitCache) callsSnapshot() []rateLimitCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]rateLimitCall(nil), c.calls...)
}

func routeRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.9:8443"
	return request
}

// 测试目标：验证注册和登录均在 JSON 绑定前使用各自动作和 ClientIP 执行限流
// 预期效果：格式错误请求仍会触发一次对应限流调用，然后保持原有 400 契约
func TestRegisterAndLoginRateLimitRunBeforeJSONBinding(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		action   string
		windowMS int64
	}{
		{name: "register", path: "/api/user/register", action: ratelimit.RegisterAction, windowMS: ratelimit.RegisterWindow.Milliseconds()},
		{name: "login", path: "/api/user/login", action: ratelimit.LoginAction, windowMS: ratelimit.LoginWindow.Milliseconds()},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cache := &routeRateLimitCache{result: []any{int64(1), int64(1000)}}
			engine := New(nil, false, Options{RateLimitCache: cache})
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, routeRequest(http.MethodPost, testCase.path, "{"))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("格式错误请求状态错误 got=%d want=%d", response.Code, http.StatusBadRequest)
			}
			calls := cache.callsSnapshot()
			if len(calls) != 1 {
				t.Fatalf("限流调用次数错误 got=%d want=1", len(calls))
			}
			if got, want := calls[0].keys, []string{"rl:v1:" + testCase.action + ":203.0.113.9"}; len(got) != 1 || got[0] != want[0] {
				t.Fatalf("限流键错误 got=%v want=%v", got, want)
			}
			if len(calls[0].args) != 1 || calls[0].args[0] != testCase.windowMS {
				t.Fatalf("限流窗口参数错误 got=%v want=%d", calls[0].args, testCase.windowMS)
			}
		})
	}
}
