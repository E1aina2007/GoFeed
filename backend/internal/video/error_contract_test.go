package video

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 测试目标：验证视频模块复用公共错误规则时保持关键状态码与安全文案
// 预期效果：互动暂不可用返回固定 503，草稿冲突返回 409 和已定义领域文案
func TestHandleVideoErrorContract(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{name: "engagement unavailable", err: fmt.Errorf("wrapped: %w", ErrEngagementUnavailable), status: http.StatusServiceUnavailable, message: "engagement stats temporarily unavailable"},
		{name: "draft incomplete", err: ErrDraftIncomplete, status: http.StatusConflict, message: ErrDraftIncomplete.Error()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			handleVideoError(context, testCase.err)
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
