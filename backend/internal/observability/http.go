package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"gofeed/internal/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	RequestIDHeader  = "X-Request-ID"
	requestIDKey     = "gofeed.request_id"
	readinessTimeout = 2 * time.Second
	maxRequestIDSize = 128
)

var requestIDSequence uint64

// ReadinessCheck 描述服务依赖的就绪检查
type ReadinessCheck func(context.Context) error

// DatabaseReadiness 返回检查 MySQL 连接可用性的就绪函数
func DatabaseReadiness(db *gorm.DB) ReadinessCheck {
	return func(ctx context.Context) error {
		if db == nil {
			return errors.New("database unavailable")
		}
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
}

// RequestID 从当前请求上下文读取关联 ID
func RequestID(c *gin.Context) string {
	if value, exists := c.Get(requestIDKey); exists {
		if requestID, ok := value.(string); ok {
			return requestID
		}
	}
	return ""
}

// RequestLogger 为每个请求补充关联 ID 与数据库查询计数器，并在请求结束时记录稳定字段
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := requestIDFromHeader(c.GetHeader(RequestIDHeader))
		c.Set(requestIDKey, requestID)
		c.Header(RequestIDHeader, requestID)
		// 查询计数器由本中间件安装，仓储语句经上下文自动累计，完成后随日志输出
		c.Request = c.Request.WithContext(db.WithQueryCounter(c.Request.Context()))

		started := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}

		event := "http_request"
		if c.Writer.Status() >= http.StatusInternalServerError {
			event = "http_request_error"
		}
		log.Printf(
			"%s request_id=%q method=%q route=%q status=%d duration_ms=%d db_queries=%d",
			event,
			requestID,
			c.Request.Method,
			route,
			c.Writer.Status(),
			time.Since(started).Milliseconds(),
			db.QueryCount(c.Request.Context()),
		)
	}
}

// LivenessHandler 返回进程存活状态，不依赖外部服务
func LivenessHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"name": "GoFeed", "status": "ok"})
}

// ReadinessHandler 返回依赖检查结果，失败时使用 503 但不暴露内部错误
func ReadinessHandler(check ReadinessCheck) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
		defer cancel()

		readinessCheck := check
		if readinessCheck == nil {
			readinessCheck = func(context.Context) error { return errors.New("readiness check unavailable") }
		}
		if err := readinessCheck(ctx); err != nil {
			log.Printf("readiness_check status=not_ready request_id=%q error=%q", RequestID(c), err)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"name":   "GoFeed",
				"status": "not_ready",
				"dependencies": gin.H{
					"database": "unavailable",
				},
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"name":   "GoFeed",
			"status": "ready",
			"dependencies": gin.H{
				"database": "ok",
			},
		})
	}
}

func requestIDFromHeader(value string) string {
	value = strings.TrimSpace(value)
	if validRequestID(value) {
		return value
	}
	return newRequestID()
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDSize {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}

	sequence := atomic.AddUint64(&requestIDSequence, 1)
	return fmt.Sprintf("fallback-%d-%d", time.Now().UnixNano(), sequence)
}
