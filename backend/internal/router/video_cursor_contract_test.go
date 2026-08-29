package router

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// 测试目标：验证视频列表游标绑定全局、作者和当前用户查询范围
// 预期效果：跨范围复用游标以及升级前旧格式均返回 400
func TestVideoCursorScopeContract(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "cursor_contract", "cursor-contract-password-123")
	sess := login(t, client, base, "cursor_contract", "cursor-contract-password-123")
	publishCompleteVideo(t, client, base, sess.AccessToken, "游标范围视频一")
	publishCompleteVideo(t, client, base, sess.AccessToken, "游标范围视频二")

	var globalPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video?limit=1", "", nil, http.StatusOK, &globalPage)
	if len(globalPage.Items) != 1 || globalPage.NextCursor == "" {
		t.Fatalf("全局列表应返回可继续分页的游标 got=%+v", globalPage)
	}
	globalCursor := url.QueryEscape(globalPage.NextCursor)

	// 全局游标不能用于作者范围或当前用户范围
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video?author_id=%d&limit=1&cursor=%s", base, sess.UserID, globalCursor),
		"", nil, http.StatusBadRequest, nil)
	doJSON(t, client, http.MethodGet,
		base+"/api/video/auth/mine?limit=1&cursor="+globalCursor,
		sess.AccessToken, nil, http.StatusBadRequest, nil)

	var authorPage struct {
		Items      []videoItem `json:"items"`
		NextCursor string      `json:"next_cursor"`
	}
	doJSON(t, client, http.MethodGet,
		fmt.Sprintf("%s/api/video?author_id=%d&limit=1", base, sess.UserID),
		"", nil, http.StatusOK, &authorPage)
	if authorPage.NextCursor == "" {
		t.Fatal("作者列表存在下一页时必须返回游标")
	}
	wrongAuthorCursor := url.QueryEscape(authorPage.NextCursor)
	doJSON(t, client, http.MethodGet,
		base+"/api/video?author_id=999999&limit=1&cursor="+wrongAuthorCursor,
		"", nil, http.StatusBadRequest, nil)

	oldPayload := `{"published_at":"2026-08-29T08:00:00Z","id":100}`
	oldCursor := url.QueryEscape(base64.RawURLEncoding.EncodeToString([]byte(oldPayload)))
	doJSON(t, client, http.MethodGet,
		base+"/api/video?limit=1&cursor="+oldCursor,
		"", nil, http.StatusBadRequest, nil)
}
