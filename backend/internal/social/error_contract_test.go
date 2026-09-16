package social

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 测试目标：验证互动模块的领域与未知错误均经公共响应映射处理
// 预期效果：评论作者限制返回 403，未知错误返回 500 且不回显内部文本
func TestSocialErrorHandlerContract(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{name: "comment author", err: ErrCommentNotAuthor, status: http.StatusForbidden, message: ErrCommentNotAuthor.Error()},
		{name: "unknown", err: errors.New("internal database address"), status: http.StatusInternalServerError, message: "social operation failed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			handleError(context, testCase.err)
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
