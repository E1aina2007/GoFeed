package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 测试目标：验证错误规则按顺序优先匹配且保留可追溯底层原因
// 预期效果：最具体规则决定类别和默认文案，Descriptor.Cause 仍可被 errors.Is 识别
func TestResolvePrefersEarlierRuleAndPreservesCause(t *testing.T) {
	specific := errors.New("specific")
	err := fmt.Errorf("wrapped: %w", specific)
	descriptor := Resolve(err, "fallback",
		Rule{Match: Is(specific), Code: CodeConflict},
		Rule{Match: func(error) bool { return true }, Code: CodeUnavailable},
	)
	if descriptor.Code != CodeConflict || descriptor.PublicMessage != "resource conflict" {
		t.Fatalf("规则优先级错误 got=%+v", descriptor)
	}
	if !errors.Is(descriptor.Cause, specific) {
		t.Fatalf("底层原因应保留 got=%v", descriptor.Cause)
	}
}

// 测试目标：验证未知和空错误始终降级为安全的内部错误响应
// 预期效果：未知错误使用模块 fallback 且不回显底层文本，空错误使用通用默认文案
func TestResolveUnknownAndNilErrorsUseSafeFallback(t *testing.T) {
	unknown := errors.New("database password leaked")
	descriptor := Resolve(unknown, "video operation failed")
	if descriptor.Code != CodeInternal || descriptor.PublicMessage != "video operation failed" || !errors.Is(descriptor.Cause, unknown) {
		t.Fatalf("未知错误解析错误 got=%+v", descriptor)
	}
	if descriptor.PublicMessage == unknown.Error() {
		t.Fatal("未知错误不应回显底层文本")
	}
	nilDescriptor := Resolve(nil, "")
	if nilDescriptor.Code != CodeInternal || nilDescriptor.PublicMessage != "internal server error" || nilDescriptor.Cause != nil {
		t.Fatalf("空错误解析错误 got=%+v", nilDescriptor)
	}
}

// 测试目标：验证公共错误类别映射稳定的 HTTP 状态与 JSON 形状
// 预期效果：所有公开类别使用预期状态码，未知类别回退 500 且响应只含 error 字段
func TestHTTPStatusAndWriteJSONContract(t *testing.T) {
	statuses := map[Code]int{
		CodeInvalid:      http.StatusBadRequest,
		CodeUnauthorized: http.StatusUnauthorized,
		CodeForbidden:    http.StatusForbidden,
		CodeNotFound:     http.StatusNotFound,
		CodeConflict:     http.StatusConflict,
		CodeTooLarge:     http.StatusRequestEntityTooLarge,
		CodeRateLimited:  http.StatusTooManyRequests,
		CodeUnavailable:  http.StatusServiceUnavailable,
		CodeInternal:     http.StatusInternalServerError,
		Code("unknown"):  http.StatusInternalServerError,
	}
	for code, want := range statuses {
		if got := HTTPStatus(code); got != want {
			t.Fatalf("状态码映射错误 code=%s got=%d want=%d", code, got, want)
		}
	}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	WriteJSON(context, Descriptor{Code: CodeUnavailable, PublicMessage: "try again later"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("响应状态错误 got=%d", recorder.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if len(body) != 1 || body["error"] != "try again later" {
		t.Fatalf("错误响应形状错误 got=%v", body)
	}
}
