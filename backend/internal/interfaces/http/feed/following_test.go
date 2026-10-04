package interfaceshttpfeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
)

type followingHTTPReader struct {
	calls  int
	viewer uint
}

func (r *followingHTTPReader) ListFollowingPage(_ context.Context, viewer uint, _ *domainfeed.FollowingCursor, _ int) (domainfeed.TimelinePage, error) {
	r.calls++
	r.viewer = viewer
	return domainfeed.TimelinePage{}, nil
}

// 测试目标：查询校验先于 Following 认证，认证先于游标校验，认证中止后不读取业务
// 预期效果：错误状态与调用次数准确，Following 的成功与错误响应均为私有且合并 Vary
func TestFollowingHTTPAuthenticationOrderAndPrivateHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, query                   string
		authenticated, abort, missing bool
		status, authCalls, reads      int
	}{
		{name: "empty_page", query: "scene=following", authenticated: true, status: 200, authCalls: 1, reads: 1},
		{name: "missing_auth_option", query: "scene=following", missing: true, status: 401},
		{name: "abort", query: "scene=following", abort: true, status: 401, authCalls: 1},
		{name: "missing_identity", query: "scene=following", status: 401, authCalls: 1},
		{name: "unauthenticated_bad_cursor", query: "scene=following&cursor=invalid", status: 401, authCalls: 1},
		{name: "authenticated_bad_cursor", query: "scene=following&cursor=invalid", authenticated: true, status: 400, authCalls: 1},
		{name: "invalid_limit", query: "scene=following&limit=0", authenticated: true, status: 400},
		{name: "unknown_parameter", query: "scene=following&viewer_id=42", authenticated: true, status: 400},
		{name: "duplicate_scene", query: "scene=following&scene=timeline", authenticated: true, status: 400},
		{name: "bad_encoding", query: "scene=following&cursor=%zz", authenticated: true, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeFeedRepository{}
			reader := &followingHTTPReader{}
			s := applicationfeed.New(repo, applicationfeed.WithFollowingReader(reader))
			authCalls := 0
			auth := func(c *gin.Context) {
				authCalls++
				if tc.abort {
					c.AbortWithStatusJSON(401, gin.H{"error": "invalid or expired token"})
					return
				}
				if tc.authenticated {
					c.Set("gofeed.jwt.user_id", uint(42))
				}
				c.Next()
			}
			h := New(s, WithFollowingAuth(auth))
			if tc.missing {
				h = New(s)
			}
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Header("Vary", "Origin"); c.Next() })
			router.GET("/api/feed", h.GetFeed)
			response := performFeedGet(t, router, tc.query)
			if response.Code != tc.status || authCalls != tc.authCalls || reader.calls != tc.reads {
				t.Fatalf("status=%d auth=%d reads=%d body=%s", response.Code, authCalls, reader.calls, response.Body)
			}
			if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Vary") != "Origin, Authorization" {
				t.Fatalf("私有响应头=%v", response.Header())
			}
			if reader.calls > 0 && reader.viewer != 42 {
				t.Fatalf("观看者必须来自认证上下文 got=%d", reader.viewer)
			}
			if repo.listCalls != 0 || repo.authorCalls != 0 || repo.statsCalls != 0 {
				t.Fatal("关注空页不得查询 Timeline 或作者统计")
			}
		})
	}
}

// 测试目标：公开 Timeline 不调用 Following 鉴权且不改写已有 Vary
// 预期效果：携带损坏凭据仍返回公开空页，重复大小写不同的 Authorization 不重复添加
func TestFollowingHTTPLeavesTimelineAnonymousAndMergesVary(t *testing.T) {
	repo := &fakeFeedRepository{}
	h := New(applicationfeed.New(repo), WithFollowingAuth(func(*gin.Context) { t.Fatal("Timeline 不得调用鉴权") }))
	router := gin.New()
	router.GET("/api/feed", h.GetFeed)
	request := httptest.NewRequest(http.MethodGet, "/api/feed?scene=timeline", nil)
	request.Header.Set("Authorization", "Bearer broken")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || repo.listCalls != 1 || response.Header().Get("Cache-Control") != "" {
		t.Fatalf("Timeline status=%d calls=%d headers=%v", response.Code, repo.listCalls, response.Header())
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Header("Vary", "Origin, authorization")
	privateFollowingResponse(ctx)
	if strings.Count(strings.ToLower(ctx.Writer.Header().Get("Vary")), "authorization") != 1 {
		t.Fatalf("Vary=%s", ctx.Writer.Header().Get("Vary"))
	}
}
