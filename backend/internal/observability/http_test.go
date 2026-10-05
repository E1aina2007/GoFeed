package observability

import (
	"bytes"
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
