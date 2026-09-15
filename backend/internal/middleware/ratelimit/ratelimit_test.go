package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gofeed/internal/redis/redistest"

	"github.com/gin-gonic/gin"
)

type resultCache struct {
	result any
	err    error
}

func (c resultCache) Eval(context.Context, string, []string, ...any) (any, error) {
	return c.result, c.err
}

func newLimitedEngine(cache Cache, action string, maxRequests int64, window time.Duration) *gin.Engine {
	engine := gin.New()
	engine.POST("/limited", Limit(cache, action, maxRequests, window), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return engine
}

func executeLimitedRequest(engine *gin.Engine) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/limited", nil)
	request.RemoteAddr = "198.51.100.7:4242"
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

// 测试目标：验证 Lua 固定窗口在阈值处放行、超限后返回固定 429，并在 TTL 到期后重置
// 预期效果：同一动作和客户端在窗口内仅允许指定次数，过期后的下一次请求重新放行
func TestLimitEnforcesFixedWindowAndResetsAfterTTL(t *testing.T) {
	cache := redistest.New(t)
	engine := newLimitedEngine(cache, "test", 2, time.Second)

	for attempt := 1; attempt <= 2; attempt++ {
		response := executeLimitedRequest(engine)
		if response.Code != http.StatusNoContent {
			t.Fatalf("第 %d 次请求状态错误 got=%d want=%d", attempt, response.Code, http.StatusNoContent)
		}
	}

	limited := executeLimitedRequest(engine)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("超限状态错误 got=%d want=%d", limited.Code, http.StatusTooManyRequests)
	}
	if retryAfter := limited.Header().Get("Retry-After"); retryAfter != "1" {
		t.Fatalf("超限 Retry-After 错误 got=%q want=%q", retryAfter, "1")
	}
	var body map[string]string
	if err := json.Unmarshal(limited.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析超限响应失败: %v", err)
	}
	if body["error"] != "rate limit exceeded" {
		t.Fatalf("超限响应体错误 got=%q", body["error"])
	}

	cache.FastForward(time.Second)
	reset := executeLimitedRequest(engine)
	if reset.Code != http.StatusNoContent {
		t.Fatalf("TTL 到期后请求状态错误 got=%d want=%d", reset.Code, http.StatusNoContent)
	}
}

// 测试目标：验证剩余毫秒 TTL 会向上取整为正整数秒 Retry-After
// 预期效果：429 响应不返回零秒或截断后的等待时间
func TestLimitRoundsRetryAfterUpward(t *testing.T) {
	engine := newLimitedEngine(resultCache{result: []any{int64(3), int64(1501)}}, "test", 2, time.Second)
	response := executeLimitedRequest(engine)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("超限状态错误 got=%d want=%d", response.Code, http.StatusTooManyRequests)
	}
	if retryAfter := response.Header().Get("Retry-After"); retryAfter != "2" {
		t.Fatalf("Retry-After 向上取整错误 got=%q want=%q", retryAfter, "2")
	}
}

// 测试目标：验证 Redis 缺失、执行失败或脚本结果异常时限流器保持 fail-open
// 预期效果：非权威 Redis 故障不阻断后续业务 Handler
func TestLimitFailsOpenWhenCacheIsUnavailable(t *testing.T) {
	cases := []struct {
		name  string
		cache Cache
	}{
		{name: "nil cache"},
		{name: "eval error", cache: resultCache{err: errors.New("redis unavailable")}},
		{name: "invalid result", cache: resultCache{result: []any{int64(1)}}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := executeLimitedRequest(newLimitedEngine(testCase.cache, "test", 1, time.Second))
			if response.Code != http.StatusNoContent {
				t.Fatalf("fail-open 状态错误 got=%d want=%d", response.Code, http.StatusNoContent)
			}
		})
	}
}
