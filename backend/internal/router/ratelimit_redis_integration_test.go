package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gofeed/internal/config"
	"gofeed/internal/middleware/cache"
	"gofeed/internal/middleware/ratelimit"

	"github.com/joho/godotenv"
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
