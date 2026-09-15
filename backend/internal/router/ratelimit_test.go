package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gofeed/internal/middleware/ratelimit"
)

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

// 测试目标：验证注册和登录超限会在业务 Handler 前返回统一 429 响应
// 预期效果：两个路由均返回固定错误体和向上取整后的 Retry-After
func TestRegisterAndLoginRateLimitReturnFixed429(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		count int64
	}{
		{name: "register", path: "/api/user/register", count: ratelimit.RegisterMaxRequests + 1},
		{name: "login", path: "/api/user/login", count: ratelimit.LoginMaxRequests + 1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cache := &routeRateLimitCache{result: []any{testCase.count, int64(1501)}}
			engine := New(nil, false, Options{RateLimitCache: cache})
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, routeRequest(http.MethodPost, testCase.path, "{"))

			if response.Code != http.StatusTooManyRequests {
				t.Fatalf("超限状态错误 got=%d want=%d", response.Code, http.StatusTooManyRequests)
			}
			if retryAfter := response.Header().Get("Retry-After"); retryAfter != "2" {
				t.Fatalf("Retry-After 错误 got=%q want=%q", retryAfter, "2")
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析超限响应失败: %v", err)
			}
			if body["error"] != "rate limit exceeded" {
				t.Fatalf("超限响应体错误 got=%q", body["error"])
			}
		})
	}
}

// 测试目标：验证注册和登录遇到 Redis 错误时仍继续原有业务处理
// 预期效果：限流故障 fail-open，不改写格式错误请求的 400 响应
func TestRegisterAndLoginRateLimitFailOpen(t *testing.T) {
	for _, path := range []string{"/api/user/register", "/api/user/login"} {
		t.Run(path, func(t *testing.T) {
			cache := &routeRateLimitCache{err: errors.New("redis unavailable")}
			engine := New(nil, false, Options{RateLimitCache: cache})
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, routeRequest(http.MethodPost, path, "{"))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("fail-open 状态错误 got=%d want=%d", response.Code, http.StatusBadRequest)
			}
			if calls := cache.callsSnapshot(); len(calls) != 1 {
				t.Fatalf("限流调用次数错误 got=%d want=1", len(calls))
			}
		})
	}
}

// 测试目标：验证 Redis 限流依赖不会进入 API 就绪检查
// 预期效果：限流缓存故障时 /ready 仍只由注入的 MySQL 检查决定
func TestRateLimitCacheDoesNotAffectReadiness(t *testing.T) {
	cache := &routeRateLimitCache{err: errors.New("redis unavailable")}
	engine := New(nil, false, Options{
		RateLimitCache: cache,
		ReadinessCheck: func(context.Context) error {
			return nil
		},
	})
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, routeRequest(http.MethodGet, "/ready", ""))

	if response.Code != http.StatusOK {
		t.Fatalf("就绪检查状态错误 got=%d want=%d", response.Code, http.StatusOK)
	}
	if calls := cache.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("就绪检查不应调用限流缓存 got=%d", len(calls))
	}
}
