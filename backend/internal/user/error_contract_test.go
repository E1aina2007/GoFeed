package user

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 测试目标：验证用户模块登录与领域错误保持统一的公共响应契约
// 预期效果：认证失败不泄露账户细节，用户名冲突返回 409 和安全领域文案
func TestUserErrorHandlersContract(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		handle  func(*gin.Context, error)
		err     error
		status  int
		message string
	}{
		{name: "invalid credentials", handle: handleLoginError, err: fmt.Errorf("wrapped: %w", ErrInvalidCredentials), status: http.StatusUnauthorized, message: "invalid username or password"},
		{name: "username taken", handle: handleUserError, err: ErrUsernameTaken, status: http.StatusConflict, message: ErrUsernameTaken.Error()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			testCase.handle(context, testCase.err)
			if recorder.Code != testCase.status {
				t.Fatalf("状态码错误 got=%d want=%d", recorder.Code, testCase.status)
			}
			var body map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析错误响应失败: %v", err)
			}
			if body["error"] != testCase.message {
				t.Fatalf("错误文案错误 got=%q want=%q", body["error"], testCase.message)
			}
		})
	}
}
