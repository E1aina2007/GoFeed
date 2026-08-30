package observability

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 测试目标：捕获观测日志并恢复全局 logger 状态
// 预期效果：请求日志断言不会影响其他测试输出
func captureLog(t *testing.T, fn func()) string {
	t.Helper()

	originalWriter := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	})

	fn()
	return output.String()
}

// 测试目标：验证请求日志会回传已有的关联 ID并记录路由结果
// 预期效果：调用方可以用同一个 ID 关联响应和服务端日志
func TestRequestLoggerPropagatesIncomingID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())
	router.GET("/health", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/health?secret=hidden", nil)
	request.Header.Set(RequestIDHeader, "trace-123")
	response := httptest.NewRecorder()
	output := captureLog(t, func() {
		router.ServeHTTP(response, request)
	})

	if response.Code != http.StatusNoContent {
		t.Fatalf("响应状态错误 got=%d", response.Code)
	}
	if response.Header().Get(RequestIDHeader) != "trace-123" {
		t.Fatalf("响应未回传请求 ID got=%q", response.Header().Get(RequestIDHeader))
	}
	for _, fragment := range []string{
		`http_request request_id="trace-123"`,
		`method="GET"`,
		`route="/health"`,
		"status=204",
	} {
		if !strings.Contains(output, fragment) {
			t.Errorf("请求日志缺少字段 %q output=%q", fragment, output)
		}
	}
	if strings.Contains(output, "secret=hidden") {
		t.Fatal("请求日志不应记录查询参数")
	}
}

// 测试目标：验证非法或过长的外部请求 ID 会被替换
// 预期效果：日志关联字段始终为可控的可打印值
func TestRequestLoggerReplacesInvalidID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())
	router.GET("/health", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set(RequestIDHeader, "invalid\nrequest")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	requestID := response.Header().Get(RequestIDHeader)
	if requestID == "" || requestID == "invalid\nrequest" || strings.ContainsAny(requestID, "\r\n") {
		t.Fatalf("非法请求 ID 未被替换 got=%q", requestID)
	}
}

// 测试目标：验证未匹配路径不会把用户输入写入请求日志
// 预期效果：异常路由仍可统计，同时避免泄露路径中的敏感内容
func TestRequestLoggerRedactsUnmatchedPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())

	response := httptest.NewRecorder()
	output := captureLog(t, func() {
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/unknown/secret-token", nil))
	})

	if response.Code != http.StatusNotFound {
		t.Fatalf("未匹配路由状态错误 got=%d", response.Code)
	}
	if !strings.Contains(output, `route="unmatched"`) {
		t.Fatalf("未匹配路由日志错误 output=%q", output)
	}
	if strings.Contains(output, "secret-token") {
		t.Fatalf("未匹配路由日志不应包含用户输入 output=%q", output)
	}
}

// 测试目标：验证 panic 被恢复后仍记录最终错误状态
// 预期效果：服务端异常可通过关联 ID 和 500 请求日志定位
func TestRequestLoggerRecordsRecoveredPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger(), gin.Recovery())
	router.GET("/panic", func(*gin.Context) {
		panic("test panic")
	})

	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set(RequestIDHeader, "panic-trace")
	response := httptest.NewRecorder()
	output := captureLog(t, func() {
		router.ServeHTTP(response, request)
	})

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("恢复后的响应状态错误 got=%d", response.Code)
	}
	if response.Header().Get(RequestIDHeader) != "panic-trace" {
		t.Fatalf("恢复后的响应未回传请求 ID got=%q", response.Header().Get(RequestIDHeader))
	}
	if !strings.Contains(output, `http_request_error request_id="panic-trace"`) ||
		!strings.Contains(output, "status=500") {
		t.Fatalf("恢复后的请求日志错误 output=%q", output)
	}
}

// 测试目标：验证就绪检查成功时返回依赖状态
// 预期效果：部署探针可确认 API 与数据库均已准备完成
func TestReadinessHandlerReportsReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	handler := ReadinessHandler(func(ctx context.Context) error {
		called = true
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("就绪检查应带有超时上下文")
		}
		return nil
	})
	router := gin.New()
	router.Use(RequestLogger())
	router.GET("/ready", handler)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if !called {
		t.Fatal("就绪检查未被调用")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("就绪响应状态错误 got=%d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"status":"ready"`) ||
		!strings.Contains(response.Body.String(), `"database":"ok"`) {
		t.Fatalf("就绪响应内容错误 body=%s", response.Body.String())
	}
}

// 测试目标：验证依赖不可用时就绪检查返回服务不可用
// 预期效果：编排系统不会把数据库未就绪的 API 判定为可接收流量
func TestReadinessHandlerReportsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())
	router.GET("/ready", ReadinessHandler(func(context.Context) error {
		return errors.New("database connection refused")
	}))

	response := httptest.NewRecorder()
	output := captureLog(t, func() {
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	})

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("不可用就绪响应状态错误 got=%d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"status":"not_ready"`) ||
		!strings.Contains(response.Body.String(), `"database":"unavailable"`) {
		t.Fatalf("不可用就绪响应内容错误 body=%s", response.Body.String())
	}
	if !strings.Contains(output, "readiness_check status=not_ready") {
		t.Fatalf("不可用就绪日志缺少事件 output=%q", output)
	}
}

// 测试目标：验证数据库就绪适配器在未装配连接时安全失败
// 预期效果：错误配置返回不可用而不是触发空指针崩溃
func TestDatabaseReadinessHandlesNilDatabase(t *testing.T) {
	if err := DatabaseReadiness(nil)(context.Background()); err == nil {
		t.Fatal("空数据库连接应返回错误")
	}
}

// 测试目标：验证请求完成日志输出数据库查询计数字段
// 预期效果：无数据库访问的请求在日志中携带 db_queries=0，字段随日志稳定存在
func TestRequestLoggerRecordsQueryCountField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())
	router.GET("/health", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	output := captureLog(t, func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
		if response.Code != http.StatusNoContent {
			t.Errorf("响应状态错误 got=%d", response.Code)
		}
	})

	if !strings.Contains(output, "db_queries=0") {
		t.Fatalf("请求日志缺少查询计数字段 output=%q", output)
	}
}
