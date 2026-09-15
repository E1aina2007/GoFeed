// ratelimit 包提供基于 Redis 原子计数的 Gin 限流中间件
// Redis 不可用时按 fail-open 继续请求，避免非权威能力阻断业务
package ratelimit

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	apierror "gofeed/internal/error"
	"gofeed/internal/observability"

	"github.com/gin-gonic/gin"
)

const (
	// RegisterAction 是注册限流键中的动作名
	RegisterAction = "register"
	// RegisterMaxRequests 是单个 IP 在注册窗口内允许的请求数
	RegisterMaxRequests int64 = 5
	// RegisterWindow 是注册限流窗口
	RegisterWindow = time.Hour

	// LoginAction 是登录限流键中的动作名
	LoginAction = "login"
	// LoginMaxRequests 是单个 IP 在登录窗口内允许的请求数
	LoginMaxRequests int64 = 10
	// LoginWindow 是登录限流窗口
	LoginWindow = time.Minute

	operationTimeout   = 300 * time.Millisecond
	failureLogInterval = time.Second
	keyPrefix          = "rl:v1:"
)

const incrementScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {count, ttl}
`

// Cache 是限流器依赖的最小脚本执行能力
type Cache interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}

// Limit 返回固定窗口限流中间件
// action、请求上限或窗口不合法时按配置故障执行 fail-open
func Limit(cache Cache, action string, maxRequests int64, window time.Duration) gin.HandlerFunc {
	action = strings.TrimSpace(action)
	windowMilliseconds := window.Milliseconds()
	policyValid := action != "" && !strings.Contains(action, ":") && maxRequests > 0 && windowMilliseconds > 0
	reporter := failureReporter{}

	return func(c *gin.Context) {
		if cache == nil {
			failOpen(c, action, "cache_unavailable", &reporter)
			return
		}
		if !policyValid {
			failOpen(c, action, "invalid_policy", &reporter)
			return
		}
		clientIP := c.ClientIP()
		if clientIP == "" {
			failOpen(c, action, "client_ip_unavailable", &reporter)
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), operationTimeout)
		result, err := cache.Eval(ctx, incrementScript, []string{keyPrefix + action + ":" + clientIP}, windowMilliseconds)
		cancel()
		if err != nil {
			failOpen(c, action, "cache_unavailable", &reporter)
			return
		}

		count, ttlMilliseconds, ok := parseResult(result)
		if !ok {
			failOpen(c, action, "invalid_result", &reporter)
			return
		}
		if count <= maxRequests {
			c.Next()
			return
		}

		retryAfter := ttlMilliseconds / int64(time.Second/time.Millisecond)
		if ttlMilliseconds%int64(time.Second/time.Millisecond) != 0 {
			retryAfter++
		}
		if retryAfter < 1 {
			retryAfter = 1
		}
		c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
		c.Abort()
		apierror.WriteCode(c, apierror.CodeRateLimited, "rate limit exceeded")
	}
}

func parseResult(result any) (int64, int64, bool) {
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return 0, 0, false
	}
	count, countOK := values[0].(int64)
	ttlMilliseconds, ttlOK := values[1].(int64)
	if !countOK || !ttlOK || count < 1 || ttlMilliseconds < 1 {
		return 0, 0, false
	}
	return count, ttlMilliseconds, true
}

type failureReporter struct {
	lastLogUnixNano atomic.Int64
}

func failOpen(c *gin.Context, action, reason string, reporter *failureReporter) {
	if reporter.shouldLog(time.Now()) {
		log.Printf(
			"event=rate_limit result=fail_open action=%q request_id=%q reason=%q",
			action,
			observability.RequestID(c),
			reason,
		)
	}
	c.Next()
}

func (r *failureReporter) shouldLog(now time.Time) bool {
	current := now.UnixNano()
	for {
		previous := r.lastLogUnixNano.Load()
		if previous != 0 && current-previous < int64(failureLogInterval) {
			return false
		}
		if r.lastLogUnixNano.CompareAndSwap(previous, current) {
			return true
		}
	}
}
