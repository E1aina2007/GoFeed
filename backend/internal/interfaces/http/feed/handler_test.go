package interfaceshttpfeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
)

const (
	feedTestAuthorID   = uint(42)
	feedTestAuthorName = "作者 甲"
	feedTestAvatarURL  = "/static/avatars/头像 甲.png"
	// 仓储按 页大小 加一条探测记录读取，用于判断是否存在下一页
	feedTestLimitProbe = 1
	feedTestDefaultLim = 20
)

var feedTestBaseTime = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// 测试目标：实现 domainfeed.Repository 的假仓储
// 预期效果：HTTP 测试不访问数据库，并可观察仓储收到的读取位置与页大小
type fakeFeedRepository struct {
	pageErr    error
	authorsErr error
	statsErr   error

	items  []domainfeed.FeedPageItem
	cards  map[uint]domainfeed.FeedCard
	stats  map[uint]domainfeed.FeedStat
	author domainfeed.Author

	listCalls   int
	authorCalls int
	statsCalls  int
	lastLimit   int
	lastCursor  *domainfeed.TimelineCursor

	// onList 可选，用于按游标返回不同分页
	onList func(cursor *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error)
}

var _ domainfeed.Repository = (*fakeFeedRepository)(nil)

func (f *fakeFeedRepository) ListTimelinePage(_ context.Context, cursor *domainfeed.TimelineCursor, limit int) (domainfeed.TimelinePage, error) {
	f.listCalls++
	f.lastLimit = limit
	f.lastCursor = cursor
	if f.onList != nil {
		return f.onList(cursor, limit)
	}
	if f.pageErr != nil {
		return domainfeed.TimelinePage{}, f.pageErr
	}
	items := f.items
	if limit < len(items) {
		items = items[:limit]
	}
	page := domainfeed.TimelinePage{Items: items, Cards: make(map[uint]domainfeed.FeedCard, len(items))}
	for _, item := range items {
		page.Cards[item.VideoID] = f.cards[item.VideoID]
	}
	return page, nil
}

func (f *fakeFeedRepository) BatchGetAuthors(_ context.Context, authorIDs []uint) (map[uint]domainfeed.Author, error) {
	f.authorCalls++
	if f.authorsErr != nil {
		return nil, f.authorsErr
	}
	result := make(map[uint]domainfeed.Author, len(authorIDs))
	for _, id := range authorIDs {
		author := f.author
		if author.ID == 0 {
			author.ID = id
		}
		result[id] = author
	}
	return result, nil
}

func (f *fakeFeedRepository) BatchGetStats(_ context.Context, videoIDs []uint) (map[uint]domainfeed.FeedStat, error) {
	f.statsCalls++
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	result := make(map[uint]domainfeed.FeedStat, len(videoIDs))
	for _, id := range videoIDs {
		result[id] = f.stats[id]
	}
	return result, nil
}

// 测试目标：按原始查询串发送 Feed 请求
// 预期效果：非法转义等原始串可绕过 URL 解析直接进入被测 handler
func performFeedGet(t *testing.T, router *gin.Engine, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/feed", nil)
	request.URL.RawQuery = rawQuery
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

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
