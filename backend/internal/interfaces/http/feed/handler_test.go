package interfaceshttpfeed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"

	"github.com/gin-gonic/gin"
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

// 测试目标：构造字段完整的时间线卡片用于固定 HTTP 响应契约
// 预期效果：媒体原始文件名包含中文与特殊字符，可验证响应未被改写
func feedTestCard(videoID, authorID uint, publishedAt time.Time) domainfeed.FeedCard {
	return domainfeed.FeedCard{
		VideoID:           videoID,
		AuthorID:          authorID,
		Title:             fmt.Sprintf("标题 %d", videoID),
		Description:       fmt.Sprintf("描述 %d", videoID),
		PlayURL:           fmt.Sprintf("/static/play/%d.mp4", videoID),
		PlayFileName:      fmt.Sprintf("play_%d.mp4", videoID),
		PlayOriginalName:  fmt.Sprintf("原始 视频（第 %d 集）#副本.mp4", videoID),
		CoverURL:          fmt.Sprintf("/static/cover/%d.png", videoID),
		CoverFileName:     fmt.Sprintf("cover_%d.png", videoID),
		CoverOriginalName: fmt.Sprintf("封面 图（第 %d 集）!.png", videoID),
		PublishedAt:       publishedAt,
	}
}

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

// 测试目标：构造条数与发布时间递减的假时间线数据
// 预期效果：条目、卡片与统计互相一致，可被应用层校验通过
func newFakeFeedRepository(count int, firstVideoID uint) *fakeFeedRepository {
	repo := &fakeFeedRepository{
		cards:  make(map[uint]domainfeed.FeedCard, count),
		stats:  make(map[uint]domainfeed.FeedStat, count),
		author: domainfeed.Author{ID: feedTestAuthorID, Username: feedTestAuthorName, AvatarURL: feedTestAvatarURL},
	}
	for i := 0; i < count; i++ {
		videoID := firstVideoID + uint(i)
		publishedAt := feedTestBaseTime.Add(-time.Duration(i) * time.Minute)
		repo.items = append(repo.items, domainfeed.FeedPageItem{
			VideoID: videoID, AuthorID: feedTestAuthorID, PublishedAt: publishedAt,
		})
		repo.cards[videoID] = feedTestCard(videoID, feedTestAuthorID, publishedAt)
		repo.stats[videoID] = domainfeed.FeedStat{LikesCount: int64(videoID) * 3, CommentsCount: int64(videoID) * 5}
	}
	return repo
}

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

// 测试目标：构造只注册 GET /api/feed 的测试路由
// 预期效果：请求直接进入被测 handler，不依赖中间件与真实仓储
func newFeedRouter(t *testing.T, repo domainfeed.Repository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/feed", New(applicationfeed.New(repo)).GetFeed)
	return router
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

type feedTestAuthorResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type feedTestItemResponse struct {
	ID                uint                   `json:"id"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description"`
	PlayURL           string                 `json:"play_url"`
	PlayFileName      string                 `json:"play_file_name"`
	PlayOriginalName  string                 `json:"play_original_name"`
	CoverURL          string                 `json:"cover_url"`
	CoverFileName     string                 `json:"cover_file_name"`
	CoverOriginalName string                 `json:"cover_original_name"`
	PublishedAt       string                 `json:"published_at"`
	LikesCount        int64                  `json:"likes_count"`
	CommentsCount     int64                  `json:"comments_count"`
	Author            feedTestAuthorResponse `json:"author"`
}

type feedTestItemsResponse struct {
	Items      []feedTestItemResponse `json:"items"`
	NextCursor *string                `json:"next_cursor"`
}

// 测试目标：解析 Feed 成功响应并保留原始正文
// 预期效果：同时得到结构化条目与可断言字段省略的原始 JSON
func decodeFeedItemsResponse(t *testing.T, recorder *httptest.ResponseRecorder) (feedTestItemsResponse, string) {
	t.Helper()
	raw := recorder.Body.String()
	var payload feedTestItemsResponse
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v body=%s", err, raw)
	}
	return payload, raw
}

// 测试目标：校验统一错误响应的文案
// 预期效果：只断言状态码无法发现的内部细节泄漏会在文案上暴露
func assertFeedErrorMessage(t *testing.T, recorder *httptest.ResponseRecorder, wantMessage string) {
	t.Helper()
	raw := recorder.Body.String()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v body=%s", err, raw)
	}
	if body.Error != wantMessage {
		t.Fatalf("错误文案错误 got=%q want=%q", body.Error, wantMessage)
	}
}

// 测试目标：验证无参数请求使用默认时间线与默认页大小
// 预期效果：返回 200 与 20 条数据，仓储收到 20 加一条下一页探测记录且无读取位置
func TestFeedDefaultTimelineUsesDefaultLimit(t *testing.T) {
	repo := newFakeFeedRepository(25, 1)
	recorder := performFeedGet(t, newFeedRouter(t, repo), "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if repo.listCalls != 1 {
		t.Fatalf("仓储调用次数错误 got=%d want=1", repo.listCalls)
	}
	if repo.lastCursor != nil {
		t.Fatalf("首页不应携带读取位置 got=%+v", repo.lastCursor)
	}
	if repo.lastLimit != feedTestDefaultLim+feedTestLimitProbe {
		t.Fatalf("默认页大小错误 got=%d want=%d", repo.lastLimit, feedTestDefaultLim+feedTestLimitProbe)
	}
	payload, _ := decodeFeedItemsResponse(t, recorder)
	if len(payload.Items) != feedTestDefaultLim {
		t.Fatalf("默认页条目数量错误 got=%d want=%d", len(payload.Items), feedTestDefaultLim)
	}
	if payload.NextCursor == nil || *payload.NextCursor == "" {
		t.Fatal("存在下一页时必须返回 next_cursor")
	}
}

// 测试目标：验证 limit 的下限、上限与非法取值
// 预期效果：1 和 50 返回 200，51、0、-1、abc、空值、小数返回 400 且不查询仓储
func TestFeedLimitValidation(t *testing.T) {
	cases := []struct {
		name      string
		rawQuery  string
		wantCode  int
		wantItems int
		wantLimit int
	}{
		{name: "下限 1 通过", rawQuery: "limit=1", wantCode: http.StatusOK, wantItems: 1, wantLimit: 1 + feedTestLimitProbe},
		{name: "上限 50 通过", rawQuery: "limit=50", wantCode: http.StatusOK, wantItems: 50, wantLimit: 50 + feedTestLimitProbe},
		{name: "超过上限拒绝", rawQuery: "limit=51", wantCode: http.StatusBadRequest},
		{name: "显式 0 不补默认值", rawQuery: "limit=0", wantCode: http.StatusBadRequest},
		{name: "负数拒绝", rawQuery: "limit=-1", wantCode: http.StatusBadRequest},
		{name: "非数字拒绝", rawQuery: "limit=abc", wantCode: http.StatusBadRequest},
		{name: "空值拒绝", rawQuery: "limit=", wantCode: http.StatusBadRequest},
		{name: "小数拒绝", rawQuery: "limit=1.5", wantCode: http.StatusBadRequest},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeFeedRepository(60, 1)
			recorder := performFeedGet(t, newFeedRouter(t, repo), testCase.rawQuery)

			if recorder.Code != testCase.wantCode {
				t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, testCase.wantCode, recorder.Body.String())
			}
			if testCase.wantCode != http.StatusOK {
				assertFeedErrorMessage(t, recorder, "invalid limit")
				if repo.listCalls != 0 {
					t.Fatalf("非法 limit 不应查询仓储 got=%d", repo.listCalls)
				}
				return
			}
			payload, _ := decodeFeedItemsResponse(t, recorder)
			if len(payload.Items) != testCase.wantItems {
				t.Fatalf("页条目数量错误 got=%d want=%d", len(payload.Items), testCase.wantItems)
			}
			if repo.lastLimit != testCase.wantLimit {
				t.Fatalf("仓储页大小错误 got=%d want=%d", repo.lastLimit, testCase.wantLimit)
			}
		})
	}
}

// 测试目标：验证场景校验与未启用场景的拒绝语义
// 预期效果：timeline 返回 200，未知场景 400，following 缺少鉴权装配返回 401，hot、recommend 返回 501 与 no-store 且不查询数据库
func TestFeedSceneContract(t *testing.T) {
	cases := []struct {
		name        string
		rawQuery    string
		wantCode    int
		wantMessage string
		wantNoStore bool
	}{
		{name: "默认场景", rawQuery: "", wantCode: http.StatusOK},
		{name: "显式时间线", rawQuery: "scene=timeline", wantCode: http.StatusOK},
		{name: "空场景回落时间线", rawQuery: "scene=", wantCode: http.StatusOK},
		{name: "未知场景", rawQuery: "scene=unknown", wantCode: http.StatusBadRequest, wantMessage: "invalid feed scene"},
		{name: "场景大小写敏感", rawQuery: "scene=Timeline", wantCode: http.StatusBadRequest, wantMessage: "invalid feed scene"},
		{name: "关注流需要认证", rawQuery: "scene=following", wantCode: http.StatusUnauthorized, wantMessage: "authentication required", wantNoStore: true},
		{name: "热门未启用", rawQuery: "scene=hot", wantCode: http.StatusNotImplemented, wantMessage: "feed scene is not enabled", wantNoStore: true},
		{name: "推荐未启用", rawQuery: "scene=recommend", wantCode: http.StatusNotImplemented, wantMessage: "feed scene is not enabled", wantNoStore: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeFeedRepository(3, 1)
			recorder := performFeedGet(t, newFeedRouter(t, repo), testCase.rawQuery)

			if recorder.Code != testCase.wantCode {
				t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, testCase.wantCode, recorder.Body.String())
			}
			if testCase.wantMessage != "" {
				assertFeedErrorMessage(t, recorder, testCase.wantMessage)
				if repo.listCalls != 0 || repo.authorCalls != 0 || repo.statsCalls != 0 {
					t.Fatalf("被拒绝的场景不应查询数据库 timeline=%d authors=%d stats=%d",
						repo.listCalls, repo.authorCalls, repo.statsCalls)
				}
			} else if repo.listCalls != 1 {
				t.Fatalf("时间线场景应查询一次仓储 got=%d", repo.listCalls)
			}

			gotCacheControl := recorder.Header().Get("Cache-Control")
			if testCase.wantNoStore && !strings.Contains(gotCacheControl, "no-store") {
				t.Fatalf("未启用场景必须禁用缓存 got=%q", gotCacheControl)
			}
			if !testCase.wantNoStore && gotCacheControl != "" {
				t.Fatalf("成功或参数错误响应不应写入 Cache-Control got=%q", gotCacheControl)
			}
		})
	}
}

// 测试目标：验证 HTTP 层非法查询在进入业务逻辑前被拒绝
// 预期效果：未知参数、重复参数、非法转义与空键名返回 400 且文案固定为 invalid feed query
func TestFeedRejectsInvalidQuery(t *testing.T) {
	cases := []struct {
		name     string
		rawQuery string
	}{
		{name: "未知参数", rawQuery: "author_id=1"},
		{name: "未知参数与合法参数混用", rawQuery: "scene=timeline&limit=1&author_id=1"},
		{name: "场景重复", rawQuery: "scene=timeline&scene=hot"},
		{name: "游标重复", rawQuery: "cursor=a&cursor=b"},
		{name: "页大小重复", rawQuery: "limit=1&limit=2"},
		{name: "非法转义", rawQuery: "%zz"},
		{name: "空键名", rawQuery: "=1"},
		{name: "空键名空值", rawQuery: "="},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeFeedRepository(3, 1)
			recorder := performFeedGet(t, newFeedRouter(t, repo), testCase.rawQuery)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			assertFeedErrorMessage(t, recorder, "invalid feed query")
			if repo.listCalls != 0 || repo.authorCalls != 0 || repo.statsCalls != 0 {
				t.Fatalf("非法查询不应查询数据库 timeline=%d authors=%d stats=%d",
					repo.listCalls, repo.authorCalls, repo.statsCalls)
			}
		})
	}
}

// 测试目标：验证游标在分页之间的往返与非法游标拒绝
// 预期效果：首页游标被解析为上一页末条位置并驱动下一页，非法游标返回 400 且不查询仓储，空游标按首页处理
func TestFeedCursorRoundTrip(t *testing.T) {
	repo := newFakeFeedRepository(3, 1)
	router := newFeedRouter(t, repo)

	firstPage := performFeedGet(t, router, "limit=2")
	if firstPage.Code != http.StatusOK {
		t.Fatalf("首页状态码错误 got=%d body=%s", firstPage.Code, firstPage.Body.String())
	}
	firstPayload, _ := decodeFeedItemsResponse(t, firstPage)
	if len(firstPayload.Items) != 2 || firstPayload.NextCursor == nil || *firstPayload.NextCursor == "" {
		t.Fatalf("首页应返回 2 条与下一页游标 got=%+v", firstPayload)
	}
	if repo.lastCursor != nil {
		t.Fatalf("首页不应携带读取位置 got=%+v", repo.lastCursor)
	}

	// 第二页由首页游标驱动，仓储只返回剩余的最后一条
	repo.onList = func(cursor *domainfeed.TimelineCursor, _ int) (domainfeed.TimelinePage, error) {
		tail := repo.items[2:]
		page := domainfeed.TimelinePage{Items: tail, Cards: make(map[uint]domainfeed.FeedCard, len(tail))}
		for _, item := range tail {
			page.Cards[item.VideoID] = repo.cards[item.VideoID]
		}
		return page, nil
	}

	secondPage := performFeedGet(t, router, "limit=2&cursor="+url.QueryEscape(*firstPayload.NextCursor))
	if secondPage.Code != http.StatusOK {
		t.Fatalf("第二页状态码错误 got=%d body=%s", secondPage.Code, secondPage.Body.String())
	}
	secondCursor := repo.lastCursor
	if secondCursor == nil {
		t.Fatal("第二页必须携带解析后的读取位置")
	}
	if secondCursor.VideoID != 2 {
		t.Fatalf("游标视频标识错误 got=%d want=2", secondCursor.VideoID)
	}
	if want := feedTestBaseTime.Add(-time.Minute); !secondCursor.PublishedAt.Equal(want) {
		t.Fatalf("游标发布时间错误 got=%s want=%s", secondCursor.PublishedAt, want)
	}
	secondPayload, secondRaw := decodeFeedItemsResponse(t, secondPage)
	if len(secondPayload.Items) != 1 || secondPayload.Items[0].ID != 3 {
		t.Fatalf("第二页条目错误 got=%+v", secondPayload.Items)
	}
	if secondPayload.NextCursor != nil || strings.Contains(secondRaw, "next_cursor") {
		t.Fatalf("末页必须省略 next_cursor body=%s", secondRaw)
	}

	rejected := performFeedGet(t, router, "cursor=not-a-cursor")
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("非法游标状态码错误 got=%d body=%s", rejected.Code, rejected.Body.String())
	}
	assertFeedErrorMessage(t, rejected, "invalid feed cursor")
	if repo.listCalls != 2 {
		t.Fatalf("非法游标不应查询仓储 got=%d want=2", repo.listCalls)
	}

	emptyCursorRepo := newFakeFeedRepository(1, 1)
	emptyCursor := performFeedGet(t, newFeedRouter(t, emptyCursorRepo), "cursor=")
	if emptyCursor.Code != http.StatusOK {
		t.Fatalf("空游标状态码错误 got=%d body=%s", emptyCursor.Code, emptyCursor.Body.String())
	}
	if emptyCursorRepo.lastCursor != nil {
		t.Fatalf("空游标应按首页处理 got=%+v", emptyCursorRepo.lastCursor)
	}
}

// 测试目标：验证仓储故障的公开错误契约
// 预期效果：包装 ErrUnavailable 的数据库故障返回 503 且不回显底层文本，未分类故障降级为 500
func TestFeedRepositoryFailureContract(t *testing.T) {
	sqlFailure := func() error {
		return fmt.Errorf("%w: %w", domainfeed.ErrUnavailable,
			errors.New("Error 1045 (28000): Access denied for user 'gofeed'@'localhost' (using password: YES)"))
	}
	cases := []struct {
		name        string
		prepare     func(repo *fakeFeedRepository)
		wantCode    int
		wantMessage string
		wantHidden  []string
	}{
		{
			name:        "时间线读取故障",
			prepare:     func(repo *fakeFeedRepository) { repo.pageErr = sqlFailure() },
			wantCode:    http.StatusServiceUnavailable,
			wantMessage: "feed temporarily unavailable",
			wantHidden:  []string{"1045", "Access denied", "gofeed", "password", "localhost"},
		},
		{
			name:        "作者读取故障",
			prepare:     func(repo *fakeFeedRepository) { repo.authorsErr = sqlFailure() },
			wantCode:    http.StatusServiceUnavailable,
			wantMessage: "feed temporarily unavailable",
			wantHidden:  []string{"1045", "Access denied", "gofeed", "password", "localhost"},
		},
		{
			name:        "统计读取故障",
			prepare:     func(repo *fakeFeedRepository) { repo.statsErr = sqlFailure() },
			wantCode:    http.StatusServiceUnavailable,
			wantMessage: "feed temporarily unavailable",
			wantHidden:  []string{"1045", "Access denied", "gofeed", "password", "localhost"},
		},
		{
			name:        "未分类故障降级内部错误",
			prepare:     func(repo *fakeFeedRepository) { repo.pageErr = errors.New("boom: nil entry in feed page") },
			wantCode:    http.StatusInternalServerError,
			wantMessage: "feed operation failed",
			wantHidden:  []string{"boom", "nil entry"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeFeedRepository(2, 1)
			testCase.prepare(repo)
			recorder := performFeedGet(t, newFeedRouter(t, repo), "")

			if recorder.Code != testCase.wantCode {
				t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, testCase.wantCode, recorder.Body.String())
			}
			assertFeedErrorMessage(t, recorder, testCase.wantMessage)
			raw := recorder.Body.String()
			for _, hidden := range testCase.wantHidden {
				if strings.Contains(raw, hidden) {
					t.Fatalf("响应泄漏底层细节 %q body=%s", hidden, raw)
				}
			}
		})
	}
}

// 测试目标：验证下一页游标只在存在更多数据时出现
// 预期效果：仓储可多取一条时响应含 next_cursor，末页完全省略该字段
func TestFeedPaginationCursorPresence(t *testing.T) {
	moreRepo := newFakeFeedRepository(3, 1)
	more := performFeedGet(t, newFeedRouter(t, moreRepo), "limit=2")
	if more.Code != http.StatusOK {
		t.Fatalf("状态码错误 got=%d body=%s", more.Code, more.Body.String())
	}
	morePayload, moreRaw := decodeFeedItemsResponse(t, more)
	if morePayload.NextCursor == nil || *morePayload.NextCursor == "" {
		t.Fatal("存在下一页时必须返回 next_cursor")
	}
	if !strings.Contains(moreRaw, `"next_cursor"`) {
		t.Fatalf("响应 JSON 缺少 next_cursor body=%s", moreRaw)
	}
	if len(morePayload.Items) != 2 {
		t.Fatalf("页条目数量错误 got=%d want=2", len(morePayload.Items))
	}

	lastRepo := newFakeFeedRepository(3, 1)
	last := performFeedGet(t, newFeedRouter(t, lastRepo), "limit=3")
	if last.Code != http.StatusOK {
		t.Fatalf("状态码错误 got=%d body=%s", last.Code, last.Body.String())
	}
	lastPayload, lastRaw := decodeFeedItemsResponse(t, last)
	if len(lastPayload.Items) != 3 {
		t.Fatalf("末页条目数量错误 got=%d want=3", len(lastPayload.Items))
	}
	if lastPayload.NextCursor != nil {
		t.Fatalf("末页不应返回 next_cursor got=%q", *lastPayload.NextCursor)
	}
	if strings.Contains(lastRaw, "next_cursor") {
		t.Fatalf("末页 JSON 必须省略 next_cursor body=%s", lastRaw)
	}
}

// 测试目标：验证空时间线序列化为空数组而不是 null
// 预期效果：响应正文为 items 空数组且省略 next_cursor
func TestFeedEmptyResultSerializesEmptyItems(t *testing.T) {
	repo := newFakeFeedRepository(0, 1)
	recorder := performFeedGet(t, newFeedRouter(t, repo), "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码错误 got=%d body=%s", recorder.Code, recorder.Body.String())
	}
	payload, raw := decodeFeedItemsResponse(t, recorder)
	if payload.Items == nil || len(payload.Items) != 0 {
		t.Fatalf("空结果必须是空数组 got=%+v", payload.Items)
	}
	if strings.TrimSpace(raw) != `{"items":[]}` {
		t.Fatalf("空结果 JSON 错误 got=%s want=%s", strings.TrimSpace(raw), `{"items":[]}`)
	}
	if strings.Contains(raw, "null") || strings.Contains(raw, "next_cursor") {
		t.Fatalf("空结果不应出现 null 或 next_cursor body=%s", raw)
	}
	if repo.listCalls != 1 {
		t.Fatalf("空结果仍应查询一次仓储 got=%d", repo.listCalls)
	}
}

// 测试目标：验证 Feed 条目 JSON 字段完整且媒体原始文件名不被改写
// 预期效果：条目字段与作者字段同领域数据一一对应，中文与特殊字符原样输出
func TestFeedResponseFieldContract(t *testing.T) {
	repo := newFakeFeedRepository(1, 7)
	recorder := performFeedGet(t, newFeedRouter(t, repo), "limit=1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码错误 got=%d body=%s", recorder.Code, recorder.Body.String())
	}
	payload, raw := decodeFeedItemsResponse(t, recorder)
	if len(payload.Items) != 1 {
		t.Fatalf("条目数量错误 got=%d want=1", len(payload.Items))
	}

	card := repo.cards[7]
	stat := repo.stats[7]
	item := payload.Items[0]

	assertFeedField := func(name string, got, want any) {
		t.Helper()
		if got != want {
			t.Fatalf("字段 %s 不匹配 got=%v want=%v", name, got, want)
		}
	}
	assertFeedField("id", item.ID, card.VideoID)
	assertFeedField("title", item.Title, card.Title)
	assertFeedField("description", item.Description, card.Description)
	assertFeedField("play_url", item.PlayURL, card.PlayURL)
	assertFeedField("play_file_name", item.PlayFileName, card.PlayFileName)
	assertFeedField("play_original_name", item.PlayOriginalName, card.PlayOriginalName)
	assertFeedField("cover_url", item.CoverURL, card.CoverURL)
	assertFeedField("cover_file_name", item.CoverFileName, card.CoverFileName)
	assertFeedField("cover_original_name", item.CoverOriginalName, card.CoverOriginalName)
	assertFeedField("published_at", item.PublishedAt, card.PublishedAt.Format(time.RFC3339))
	assertFeedField("likes_count", item.LikesCount, stat.LikesCount)
	assertFeedField("comments_count", item.CommentsCount, stat.CommentsCount)
	assertFeedField("author.id", item.Author.ID, feedTestAuthorID)
	assertFeedField("author.username", item.Author.Username, feedTestAuthorName)
	assertFeedField("author.avatar_url", item.Author.AvatarURL, feedTestAvatarURL)

	if !strings.Contains(raw, card.PlayOriginalName) {
		t.Fatalf("play_original_name 被改写 got body=%s want=%s", raw, card.PlayOriginalName)
	}
	if !strings.Contains(raw, card.CoverOriginalName) {
		t.Fatalf("cover_original_name 被改写 got body=%s want=%s", raw, card.CoverOriginalName)
	}
	if item.PlayFileName == item.PlayOriginalName {
		t.Fatalf("物理存储名与原始文件名不应相同 got=%q", item.PlayFileName)
	}
}

// 测试目标：验证 published_at 的 RFC3339 序列化契约
// 预期效果：时间按 RFC3339 输出且时刻不变，UTC 时间以 Z 结尾，带偏移时间保留自身偏移
func TestFeedPublishedAtSerializationContract(t *testing.T) {
	cases := []struct {
		name        string
		publishedAt time.Time
		wantEncoded string
	}{
		{
			name:        "UTC 时间",
			publishedAt: time.Date(2026, 3, 1, 12, 30, 45, 0, time.UTC),
			wantEncoded: "2026-03-01T12:30:45Z",
		},
		{
			name:        "带偏移时间",
			publishedAt: time.Date(2026, 3, 1, 20, 30, 45, 0, time.FixedZone("UTC+8", 8*60*60)),
			wantEncoded: "2026-03-01T20:30:45+08:00",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeFeedRepository(1, 1)
			card := repo.cards[1]
			card.PublishedAt = testCase.publishedAt
			repo.cards[1] = card
			entry := repo.items[0]
			entry.PublishedAt = testCase.publishedAt
			repo.items[0] = entry

			recorder := performFeedGet(t, newFeedRouter(t, repo), "")
			if recorder.Code != http.StatusOK {
				t.Fatalf("状态码错误 got=%d body=%s", recorder.Code, recorder.Body.String())
			}
			payload, _ := decodeFeedItemsResponse(t, recorder)
			if len(payload.Items) != 1 {
				t.Fatalf("条目数量错误 got=%d want=1", len(payload.Items))
			}

			encoded := payload.Items[0].PublishedAt
			if encoded != testCase.wantEncoded {
				t.Fatalf("published_at 编码错误 got=%q want=%q", encoded, testCase.wantEncoded)
			}
			parsed, err := time.Parse(time.RFC3339, encoded)
			if err != nil {
				t.Fatalf("published_at 不是 RFC3339 got=%q err=%v", encoded, err)
			}
			if !parsed.Equal(testCase.publishedAt) {
				t.Fatalf("published_at 时刻被改变 got=%s want=%s", parsed, testCase.publishedAt)
			}
			if got := parsed.UTC().Format(time.RFC3339); got != "2026-03-01T12:30:45Z" {
				t.Fatalf("published_at 归一时刻错误 got=%s want=2026-03-01T12:30:45Z", got)
			}
		})
	}
}

// 测试目标：验证旧视频接口游标不能用于 Feed 时间线
// 预期效果：视频游标、升级前旧载荷与非法载荷返回 400 且不查询仓储
func TestFeedRejectsForeignCursorFormats(t *testing.T) {
	encode := func(payload string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(payload))
	}
	cases := []struct {
		name   string
		cursor string
	}{
		{name: "非 base64 游标", cursor: "!!!not-base64!!!"},
		{name: "超长游标", cursor: strings.Repeat("A", 2048)},
		{name: "非 JSON 载荷", cursor: encode("not-json")},
		{name: "视频公开游标", cursor: encode(`{"v":1,"k":"public","p":"2026-08-29T08:00:00Z","i":100}`)},
		{name: "视频旧格式游标", cursor: encode(`{"published_at":"2026-08-29T08:00:00Z","id":100}`)},
		// 结构合法但位置为空的载荷同样必须拒绝，避免跨接口按零值误读分页位置
		{name: "零位置时间线游标", cursor: encode(`{"version":1,"scene":"timeline","sort_version":1,"published_at":"0001-01-01T00:00:00Z","video_id":0}`)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newFakeFeedRepository(2, 1)
			recorder := performFeedGet(t, newFeedRouter(t, repo), "cursor="+url.QueryEscape(testCase.cursor))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			assertFeedErrorMessage(t, recorder, "invalid feed cursor")
			if repo.listCalls != 0 {
				t.Fatalf("非法游标不应查询仓储 got=%d", repo.listCalls)
			}
		})
	}
}
