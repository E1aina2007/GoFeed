package apierror

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// contractError 用于验证 Cause 上的 errors.As 追溯能力
type contractError struct {
	reason string
}

func (e *contractError) Error() string { return "contract failed: " + e.reason }

// 测试目标：验证规则文案按显式文案、错误自身文案、类别默认文案依次降级
// 预期效果：显式文案覆盖错误文本，未声明文案时回落到类别默认文案
func TestRuleMessagePriorityOrder(t *testing.T) {
	err := errors.New("raw internal detail")
	cases := []struct {
		name string
		rule Rule
		want string
	}{
		{"显式文案优先", Rule{Code: CodeConflict, PublicMessage: "username already exists", UseErrorText: true}, "username already exists"},
		{"错误自身文案", Rule{Code: CodeInvalid, UseErrorText: true}, "raw internal detail"},
		{"类别默认文案", Rule{Code: CodeRateLimited}, "rate limit exceeded"},
	}
	for _, tc := range cases {
		if got := ruleMessage(tc.rule, err); got != tc.want {
			t.Fatalf("%s 文案错误 got=%q want=%q", tc.name, got, tc.want)
		}
	}

	descriptor := Resolve(err, "fallback text", Rule{Match: Is(err), Code: CodeUnavailable})
	if descriptor.Code != CodeUnavailable || descriptor.PublicMessage != "service temporarily unavailable" {
		t.Fatalf("未声明文案的规则应回落到类别默认文案 got=%+v", descriptor)
	}
}

// 测试目标：验证规则匹配穿透多层包装并可用 errors.As 追溯底层原因
// 预期效果：空 Match 规则被跳过，双重包装的错误仍命中规则且 Cause 保留原始类型
func TestResolveSkipsNilMatcherAndKeepsErrorsAsCause(t *testing.T) {
	origin := &contractError{reason: "draft_incomplete"}
	wrapped := fmt.Errorf("service layer: %w", fmt.Errorf("repository layer: %w", origin))

	matcher := Is(nil, origin)
	if !matcher(wrapped) {
		t.Fatal("errors.Is 应穿透双重包装命中目标错误")
	}
	if matcher(errors.New("unrelated failure")) {
		t.Fatal("无关错误不应命中规则")
	}

	descriptor := Resolve(wrapped, "video operation failed",
		Rule{Code: CodeConflict, PublicMessage: "空 Match 规则不应命中"},
		Rule{Match: matcher, Code: CodeConflict, UseErrorText: true},
	)
	// UseErrorText 直接采用传入错误的完整文本，被包装的调用链文本会一并对外
	if descriptor.Code != CodeConflict || descriptor.PublicMessage != wrapped.Error() {
		t.Fatalf("命中规则结果错误 got=%+v", descriptor)
	}
	if !errors.Is(descriptor.Cause, origin) {
		t.Fatalf("Cause 应可追溯到原始错误 got=%v", descriptor.Cause)
	}
	var target *contractError
	if !errors.As(descriptor.Cause, &target) || target.reason != "draft_incomplete" {
		t.Fatalf("Cause 应支持 errors.As 取回原始类型 got=%v", descriptor.Cause)
	}
}

// 测试目标：验证未命中规则的数据库错误不会把内部细节写入响应体
// 预期效果：状态为 500 且响应只包含模块兜底文案，不含 SQL 片段与表名
func TestUnknownDatabaseErrorIsNotExposed(t *testing.T) {
	databaseErr := errors.New("Error 1146 (42S02): Table 'gofeed.users' doesn't exist (SELECT * FROM users WHERE id = 1)")
	const fallback = "user operation failed"
	leaks := []string{"1146", "gofeed.users", "SELECT", "doesn't exist"}

	descriptor := Resolve(fmt.Errorf("query failed: %w", databaseErr), fallback)
	if descriptor.Code != CodeInternal || descriptor.PublicMessage != fallback {
		t.Fatalf("未知错误应使用模块兜底文案 got=%+v", descriptor)
	}
	for _, leak := range leaks {
		if strings.Contains(descriptor.PublicMessage, leak) {
			t.Fatalf("公开文案泄漏内部细节 %q got=%q", leak, descriptor.PublicMessage)
		}
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/leak", func(c *gin.Context) {
		Write(c, fmt.Errorf("query failed: %w", databaseErr), fallback)
	})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/leak", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("未知错误状态错误 got=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, leak := range leaks {
		if strings.Contains(body, leak) {
			t.Fatalf("响应体泄漏内部细节 %q got=%s", leak, body)
		}
	}
	if !strings.Contains(body, fallback) {
		t.Fatalf("响应体应包含模块兜底文案 got=%s", body)
	}
}
