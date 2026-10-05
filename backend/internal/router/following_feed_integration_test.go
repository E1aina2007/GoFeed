package router

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	applicationfeed "gofeed/internal/application/feed"
	"gofeed/internal/auth"
	"gofeed/internal/db"
	domainfeed "gofeed/internal/domain/feed"
	"gofeed/internal/social"
	"gofeed/internal/testutil"
	"gofeed/internal/user"
	videoModel "gofeed/internal/video"
)

type followingCacheSpy struct{ calls atomic.Int64 }

func (s *followingCacheSpy) GetPage(context.Context, applicationfeed.PageCacheQuery) (applicationfeed.CachedPage, bool, error) {
	s.calls.Add(1)
	return applicationfeed.CachedPage{}, false, errors.New("unexpected page cache read")
}
func (s *followingCacheSpy) SetPage(context.Context, applicationfeed.PageCacheQuery, applicationfeed.CachedPage) error {
	s.calls.Add(1)
	return errors.New("unexpected page cache write")
}
func (s *followingCacheSpy) GetCards(context.Context, []uint) (applicationfeed.CachedCards, error) {
	s.calls.Add(1)
	return applicationfeed.CachedCards{}, errors.New("unexpected card cache read")
}
func (s *followingCacheSpy) SetCards(context.Context, []domainfeed.FeedCard) (applicationfeed.CardCacheWrite, error) {
	s.calls.Add(1)
	return applicationfeed.CardCacheWrite{}, errors.New("unexpected card cache write")
}
func (s *followingCacheSpy) DeleteCards(context.Context, []uint) error {
	s.calls.Add(1)
	return errors.New("unexpected card cache delete")
}

type followingHTTPEnv struct {
	t       *testing.T
	gdb     *gorm.DB
	server  *httptest.Server
	viewer  authSession
	capture *queryCapture
	faults  *faultInjection
	cache   *followingCacheSpy
}

// 测试目标：在隔离 MySQL 中装配生产路由、真实会话、查询计数与缓存探针
// 预期效果：关注流验证不依赖 Redis 或 MQ，缓存开启装配下仍只能读 MySQL
func newFollowingHTTPEnv(t *testing.T) *followingHTTPEnv {
	t.Helper()
	gdb := testutil.DB(t)
	if err := db.RegisterQueryCounter(gdb); err != nil {
		t.Fatal(err)
	}
	if err := registerFaultInjection(gdb); err != nil {
		t.Fatal(err)
	}
	capture := &queryCapture{}
	faults := &faultInjection{}
	cache := &followingCacheSpy{}
	engine := New(gdb, false, Options{UploadDir: t.TempDir(), FeedPageCache: cache, FeedCardCache: cache, Middlewares: []gin.HandlerFunc{capture.middleware(), faults.middleware(), func(c *gin.Context) { c.Header("Vary", "Origin"); c.Next() }}})
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	register(t, server.Client(), server.URL, "following-viewer", "following-password-123")
	viewer := login(t, server.Client(), server.URL, "following-viewer", "following-password-123")
	authors := []user.User{{ID: 20, Username: "following-author-a"}, {ID: 30, Username: "following-author-b"}, {ID: 40, Username: "following-other-author"}}
	if err := gdb.Create(&authors).Error; err != nil {
		t.Fatal(err)
	}
	return &followingHTTPEnv{t: t, gdb: gdb, server: server, viewer: viewer, capture: capture, faults: faults, cache: cache}
}

// 测试目标：为关注集合写入历史公开视频
// 预期效果：媒体展示字段完整，发布时间早于当前关注时间且视频 ID 独立于作者 ID
func (e *followingHTTPEnv) video(id, author uint) videoModel.Video {
	e.t.Helper()
	when := feedBaseTime
	row := videoModel.Video{ID: id, AuthorID: author, Title: fmt.Sprintf("关注视频%d", id), Description: "历史视频", Status: videoModel.VideoStatusPublished, PublishedAt: &when,
		PlayURL: "/static/videos/a.mp4", PlayFileName: "a.mp4", PlayOriginalName: "原始 视频.mp4", CoverURL: "/static/covers/a.png", CoverFileName: "a.png", CoverOriginalName: "原始 封面.png"}
	if err := e.gdb.Create(&row).Error; err != nil {
		e.t.Fatal(err)
	}
	return row
}

// 测试目标：写入本观看者的当前关注关系
// 预期效果：无需读请求补发历史事件即可把当前可见历史视频加入集合
func (e *followingHTTPEnv) follow(author uint) {
	e.t.Helper()
	if err := e.gdb.Create(&social.Follow{FollowerID: e.viewer.UserID, FolloweeID: author}).Error; err != nil {
		e.t.Fatal(err)
	}
}

// 测试目标：通过生产 HTTP 入口读取关注页并核对私有头
// 预期效果：状态与 JSON 准确，已有 Origin 的 Vary 保留且包含 Authorization
func (e *followingHTTPEnv) get(query, token string, status int) feedTimelineResponse {
	e.t.Helper()
	var page feedTimelineResponse
	response := doJSON(e.t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?scene=following"+query, token, nil, status, &page)
	if response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("Vary") != "Origin, Authorization" {
		e.t.Fatalf("关注响应头=%v", response.Header)
	}
	return page
}

// 测试目标：验证真实关注查询的历史可见性、同刻 keyset、探测截断与实时批量统计
// 预期效果：只返回关注的活动作者，视频字段不被 JOIN 覆盖，非空六次查询且缓存零调用
func TestFollowingFeedMySQLPagingAndQueryBudget(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.follow(30)
	e.video(101, 20)
	e.video(102, 30)
	e.video(103, 20)
	e.video(999, 40)
	private := e.video(98, 20)
	if err := e.gdb.Model(&private).UpdateColumn("status", videoModel.VideoStatusProcessing).Error; err != nil {
		t.Fatal(err)
	}
	e.capture.reset()
	first := e.get("&limit=2", e.viewer.AccessToken, 200)
	if ids := followingIDs(first); !reflect.DeepEqual(ids, []uint{103, 102}) || first.NextCursor == "" {
		t.Fatalf("首屏 ids=%v cursor=%s", ids, first.NextCursor)
	}
	if first.Items[0].Author.ID != 20 || first.Items[0].PlayOriginalName != "原始 视频.mp4" || first.Items[0].CoverOriginalName != "原始 封面.png" {
		t.Fatalf("JOIN 字段映射=%+v", first.Items[0])
	}
	if err := e.gdb.Create(&social.VideoLike{VideoID: 103, UserID: e.viewer.UserID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.gdb.Create(&social.Comment{VideoID: 103, AuthorID: 30, Content: "当前评论"}).Error; err != nil {
		t.Fatal(err)
	}
	updated := e.get("&limit=2", e.viewer.AccessToken, 200)
	if updated.Items[0].LikesCount != 1 || updated.Items[0].CommentsCount != 1 {
		t.Fatalf("当前统计=%+v", updated.Items[0])
	}
	second := e.get("&limit=1&cursor="+url.QueryEscape(first.NextCursor), e.viewer.AccessToken, 200)
	if ids := followingIDs(second); !reflect.DeepEqual(ids, []uint{101}) || second.NextCursor != "" {
		t.Fatalf("末页=%+v", second)
	}
	if counts := e.capture.snapshot(); !reflect.DeepEqual(counts, []int64{6, 6, 6}) {
		t.Fatalf("非空查询预算=%v", counts)
	}
	if e.cache.calls.Load() != 0 {
		t.Fatalf("Following 缓存调用=%d", e.cache.calls.Load())
	}
}

func followingIDs(page feedTimelineResponse) []uint {
	ids := make([]uint, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// 测试目标：真实 SQL 排除不公开、软删、未关注和注销作者的视频
// 预期效果：无关注空页三次查询，六项媒体缺陷与非发布状态不进入结果，Timeline 注销占位保留
func TestFollowingFeedMySQLVisibilityAndEmptyPage(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.video(101, 20)
	e.capture.reset()
	if page := e.get("", e.viewer.AccessToken, 200); page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("无关注页=%+v", page)
	}
	if counts := e.capture.snapshot(); !reflect.DeepEqual(counts, []int64{3}) {
		t.Fatalf("空页预算=%v", counts)
	}
	e.follow(20)
	e.follow(30)
	e.video(102, 30)
	if err := e.gdb.Delete(&user.User{}, 30).Error; err != nil {
		t.Fatal(err)
	}
	for i, column := range []string{"play_url", "play_file_name", "play_original_name", "cover_url", "cover_file_name", "cover_original_name"} {
		row := e.video(uint(200+i), 20)
		if err := e.gdb.Model(&row).UpdateColumn(column, "").Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, state := range []string{videoModel.VideoStatusDraft, videoModel.VideoStatusProcessing, videoModel.VideoStatusRejected, videoModel.VideoStatusPurging} {
		row := e.video(uint(300+i), 20)
		if err := e.gdb.Model(&row).UpdateColumn("status", state).Error; err != nil {
			t.Fatal(err)
		}
	}
	deleted := e.video(400, 20)
	if err := e.gdb.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	nilTime := e.video(401, 20)
	if err := e.gdb.Model(&nilTime).UpdateColumn("published_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	e.video(999, 40)
	page := e.get("", e.viewer.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(page), []uint{101}) {
		t.Fatalf("公开集合=%v", followingIDs(page))
	}
	var timeline feedTimelineResponse
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?scene=timeline", "", nil, 200, &timeline)
	found := false
	for _, item := range timeline.Items {
		if item.ID == 102 {
			found = true
			if item.Author.Username != "已注销用户" {
				t.Fatalf("Timeline 注销占位=%+v", item.Author)
			}
		}
	}
	if !found {
		t.Fatal("Following 的作者过滤不得改变 Timeline")
	}
}

// 测试目标：取关、重新关注、作者注销与视频删除按每次 MySQL 查询的当前事实生效
// 预期效果：同一游标只读取边界之后的当前集合，重新关注可读历史，删除后为空且不写缓存
func TestFollowingFeedMySQLDynamicRelations(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.follow(30)
	e.video(101, 20)
	e.video(102, 30)
	first := e.get("&limit=1", e.viewer.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(first), []uint{102}) || first.NextCursor == "" {
		t.Fatalf("首屏=%+v", first)
	}
	if err := e.gdb.Where("follower_id = ? AND followee_id = ?", e.viewer.UserID, 20).Delete(&social.Follow{}).Error; err != nil {
		t.Fatal(err)
	}
	query := "&limit=1&cursor=" + url.QueryEscape(first.NextCursor)
	if page := e.get(query, e.viewer.AccessToken, 200); len(page.Items) != 0 {
		t.Fatalf("取关续页=%+v", page)
	}
	e.follow(20)
	if page := e.get(query, e.viewer.AccessToken, 200); !reflect.DeepEqual(followingIDs(page), []uint{101}) {
		t.Fatalf("重新关注续页=%+v", page)
	}
	if err := e.gdb.Delete(&user.User{}, 30).Error; err != nil {
		t.Fatal(err)
	}
	if page := e.get("", e.viewer.AccessToken, 200); !reflect.DeepEqual(followingIDs(page), []uint{101}) {
		t.Fatalf("作者注销页=%+v", page)
	}
	if err := e.gdb.Delete(&videoModel.Video{}, 101).Error; err != nil {
		t.Fatal(err)
	}
	if page := e.get("", e.viewer.AccessToken, 200); len(page.Items) != 0 {
		t.Fatalf("视频删除页=%+v", page)
	}
	if e.cache.calls.Load() != 0 {
		t.Fatal("动态关注集合不得触发缓存")
	}
}

// 测试目标：生产 JWT/session 校验与观看者游标边界阻止跨用户、过期和撤销访问
// 预期效果：认证失败优先于游标错误，非法跨场景游标为 400，认证错误与成功均带私有头
func TestFollowingFeedAuthenticationAndCursorIsolation(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.video(101, 20)
	e.video(102, 20)
	first := e.get("&limit=1", e.viewer.AccessToken, 200)
	e.get("&cursor=invalid", "", 401)
	e.get("&cursor=invalid", "broken", 401)
	e.get("&cursor=invalid", e.viewer.AccessToken, 400)
	e.get("&viewer_id=999", e.viewer.AccessToken, 400)
	register(t, e.server.Client(), e.server.URL, "following-other-viewer", "following-password-123")
	other := login(t, e.server.Client(), e.server.URL, "following-other-viewer", "following-password-123")
	e.get("&cursor="+url.QueryEscape(first.NextCursor), other.AccessToken, 400)
	// 游标可被重编码，观看者仍必须使用认证身份，不能读取原观看者的关注集合
	data, err := base64.RawURLEncoding.DecodeString(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var forged map[string]any
	if err := json.Unmarshal(data, &forged); err != nil {
		t.Fatal(err)
	}
	forged["viewer_id"] = other.UserID
	data, err = json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if page := e.get("&cursor="+url.QueryEscape(base64.RawURLEncoding.EncodeToString(data)), other.AccessToken, 200); len(page.Items) != 0 {
		t.Fatal("重编码游标不能读取他人的关注视频")
	}
	var timeline feedTimelineResponse
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?limit=1", "", nil, 200, &timeline)
	e.get("&cursor="+url.QueryEscape(timeline.NextCursor), e.viewer.AccessToken, 400)
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/feed?cursor="+url.QueryEscape(first.NextCursor), "", nil, 400, nil)
	claims, err := auth.ParseToken(e.viewer.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	claims.ExpiresAt = jwtlib.NewNumericDate(time.Now().Add(-time.Minute))
	expired, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims).SignedString([]byte(os.Getenv("JWT_SECRET")))
	if err != nil {
		t.Fatal(err)
	}
	e.get("", expired, 401)
	doJSON(t, e.server.Client(), http.MethodPost, e.server.URL+"/api/user/auth/logout", e.viewer.AccessToken, nil, 204, nil)
	e.get("", e.viewer.AccessToken, 401)
}

// 测试目标：旧接口游标与匿名有效关注游标不能进入关注流，相同用户的新活动 session 可续用游标
// 预期效果：旧 /api/video 游标返回 400，匿名携带有效游标仍 401，重登录后同一游标继续分页
func TestFollowingFeedCursorSourcesAndSessionContinuity(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.video(101, 20)
	e.video(102, 20)
	first := e.get("&limit=1", e.viewer.AccessToken, 200)
	var legacy struct {
		NextCursor string `json:"next_cursor"`
	}
	doJSON(t, e.server.Client(), http.MethodGet, e.server.URL+"/api/video?author_id=20&limit=1", "", nil, http.StatusOK, &legacy)
	if legacy.NextCursor == "" {
		t.Fatal("旧接口应返回游标")
	}
	e.get("&cursor="+url.QueryEscape(legacy.NextCursor), e.viewer.AccessToken, 400)
	e.get("&cursor="+url.QueryEscape(first.NextCursor), "", 401)
	current := e.get("&cursor="+url.QueryEscape(first.NextCursor), e.viewer.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(current), []uint{101}) {
		t.Fatalf("当前 session 续页=%+v", current)
	}
	renewed := login(t, e.server.Client(), e.server.URL, "following-viewer", "following-password-123")
	if renewed.AccessToken == e.viewer.AccessToken || renewed.UserID != e.viewer.UserID {
		t.Fatalf("应取得同用户的新 session user_id=%d want=%d 令牌与旧值相同=%v 令牌长度 old=%d new=%d",
			renewed.UserID, e.viewer.UserID, renewed.AccessToken == e.viewer.AccessToken, len(e.viewer.AccessToken), len(renewed.AccessToken))
	}
	next := e.get("&cursor="+url.QueryEscape(first.NextCursor), renewed.AccessToken, 200)
	if !reflect.DeepEqual(followingIDs(next), []uint{101}) || next.NextCursor != "" {
		t.Fatalf("新 session 续用游标=%+v", next)
	}
}

// 测试目标：有效 session 不能使已注销观看者继续读关注页，依赖故障不能伪装空页
// 预期效果：活动用户检查为 401，业务 SQL 故障安全映射 503，保留现有 session 故障 401 语义
func TestFollowingFeedDeletedViewerAndDatabaseFailures(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	e.follow(20)
	e.video(101, 20)
	for _, tc := range []struct {
		table  string
		status int
	}{{"auth_sessions", 401}, {"users", 503}, {"videos", 503}, {"video_likes", 503}, {"video_comments", 503}} {
		e.faults.arm(tc.table, errors.New("injected private database failure"))
		page := e.get("", e.viewer.AccessToken, tc.status)
		if len(page.Items) != 0 {
			t.Fatal("错误响应不得带伪成功页")
		}
		e.faults.disarm()
	}
	if err := e.gdb.Delete(&user.User{}, e.viewer.UserID).Error; err != nil {
		t.Fatal(err)
	}
	e.get("", e.viewer.AccessToken, 401)
}

// 测试目标：记录无关注、少量与大量关注且多数视频不可见时的真实 MySQL 计划与查询数
// 预期效果：分页最多五十条且查询数保持三或六，EXPLAIN ANALYZE 使用实际执行的关联 SQL
func TestFollowingFeedMySQLQueryPlans(t *testing.T) {
	e := newFollowingHTTPEnv(t)
	authors := make([]user.User, 0, 128)
	videos := make([]videoModel.Video, 0, 1152)
	when := feedBaseTime
	for i := 0; i < 128; i++ {
		author := uint(100 + i)
		authors = append(authors, user.User{ID: author, Username: fmt.Sprintf("following-plan-%d", i)})
		for j := 0; j < 9; j++ {
			state := videoModel.VideoStatusDraft
			if j == 0 {
				state = videoModel.VideoStatusPublished
			}
			videos = append(videos, videoModel.Video{AuthorID: author, Title: "计划视频", Status: state, PublishedAt: &when, PlayURL: "/v.mp4", PlayFileName: "v.mp4", PlayOriginalName: "v.mp4", CoverURL: "/c.png", CoverFileName: "c.png", CoverOriginalName: "c.png"})
		}
	}
	if err := e.gdb.CreateInBatches(&authors, 128).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.gdb.CreateInBatches(&videos, 256).Error; err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var statement string
	var arguments []any
	callback := "gofeed:test_following_plan"
	if err := e.gdb.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "videos" || !strings.Contains(tx.Statement.SQL.String(), "user_follows") {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		statement = tx.Statement.SQL.String()
		arguments = append([]any(nil), tx.Statement.Vars...)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.gdb.Callback().Query().Remove(callback); err != nil {
			t.Error(err)
		}
	})
	for _, count := range []int{0, 1, 32, 128} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			if err := e.gdb.Where("follower_id = ?", e.viewer.UserID).Delete(&social.Follow{}).Error; err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				e.follow(uint(100 + i))
			}
			e.capture.reset()
			page := e.get("&limit=50", e.viewer.AccessToken, 200)
			if len(page.Items) != min(count, 50) || (page.NextCursor != "") != (count > 50) {
				t.Fatalf("数量 count=%d page=%+v", count, page)
			}
			budget := int64(6)
			if count == 0 {
				budget = 3
			}
			if counts := e.capture.snapshot(); !reflect.DeepEqual(counts, []int64{budget}) {
				t.Fatalf("查询预算=%v", counts)
			}
			mu.Lock()
			sql := statement
			args := append([]any(nil), arguments...)
			mu.Unlock()
			if sql == "" || !strings.Contains(sql, "videos.*") || !strings.Contains(sql, "following_author") {
				t.Fatalf("未捕获关联查询 SQL=%s", sql)
			}
			var plan string
			if err := e.gdb.Raw("EXPLAIN ANALYZE "+sql, args...).Row().Scan(&plan); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan, "actual time") {
				t.Fatalf("未返回真实执行计划=%s", plan)
			}
			metadata, _ := json.Marshal(map[string]any{"following_count": count, "video_rows": len(videos), "visible_per_author": 1, "query_count": budget})
			t.Logf("metadata=%s plan=%s", metadata, plan)
		})
	}
	if e.cache.calls.Load() != 0 {
		t.Fatal("计划用例不应访问缓存")
	}
}

// 测试目标：认证载荷通过内联字段、参数列表或 SHA1 资源写入 trace 时均完成脱敏
// 预期效果：登录、注册和刷新凭据不可从压缩包恢复，普通响应与事件结构仍可读取
func TestFollowingTraceRedactionRemovesAuthRequestResources(t *testing.T) {
	for _, endpoint := range []string{"login", "register", "refresh"} {
		for _, resourcesFirst := range []bool{false, true} {
			name := endpoint + "/network_first"
			if resourcesFirst {
				name = endpoint + "/resources_first"
			}
			t.Run(name, func(t *testing.T) {
				const requestSecret = "opaque-unit-request-credential-unmatched-by-jwt-and-password-patterns"
				const responseSecret = "opaque-unit-response-credential-unmatched-by-jwt-and-password-patterns"
				const headerSecret = "opaque-unit-header-credential"
				requestBody, err := json.Marshal(map[string]string{"refresh_token": requestSecret})
				if err != nil {
					t.Fatal(err)
				}
				network, err := json.Marshal(map[string]any{
					"type": "resource-snapshot",
					"snapshot": map[string]any{
						"request": map[string]any{
							"url":     "http://localhost/api/user/" + endpoint,
							"headers": []any{map[string]any{"name": "Authorization", "value": "Bearer " + headerSecret}},
							"postData": map[string]any{
								"mimeType": "application/json",
								"text":     string(requestBody),
								"_sha1":    "auth-request.json",
								"params":   []any{map[string]any{"name": "refresh_token", "value": requestSecret}},
							},
						},
						"response": map[string]any{
							"status":  200,
							"content": map[string]any{"_sha1": "auth-response.json"},
						},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				resources := []followingTraceEntry{
					{header: &zip.FileHeader{Name: "resources/auth-request.json", Method: zip.Deflate}, data: requestBody},
					{header: &zip.FileHeader{Name: "resources/auth-response.json", Method: zip.Deflate}, data: []byte(responseSecret)},
					{header: &zip.FileHeader{Name: "resources/feed.json", Method: zip.Deflate}, data: []byte(`{"items":[{"id":42}],"next_cursor":"public-cursor"}`)},
				}
				entries := []followingTraceEntry{{header: &zip.FileHeader{Name: "0-trace.network", Method: zip.Deflate}, data: append(network, '\n')}}
				if resourcesFirst {
					entries = append(resources, entries...)
				} else {
					entries = append(entries, resources...)
				}
				tracePath := filepath.Join(t.TempDir(), "trace.zip")
				if err := writeFollowingZip(tracePath, entries); err != nil {
					t.Fatal(err)
				}
				if err := sanitizeFollowingTraceZip(tracePath); err != nil {
					t.Fatal(err)
				}
				reader, err := zip.OpenReader(tracePath)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				if len(reader.File) != len(entries) {
					t.Fatalf("脱敏前后条目数变化 got=%d want=%d", len(reader.File), len(entries))
				}
				for _, entry := range reader.File {
					opened, err := entry.Open()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(opened)
					opened.Close()
					if err != nil {
						t.Fatal(err)
					}
					for _, secret := range []string{requestSecret, responseSecret, headerSecret} {
						if bytes.Contains(data, []byte(secret)) {
							t.Fatalf("认证凭据仍存在于 trace 条目 %s", entry.Name)
						}
					}
					if !json.Valid(bytes.TrimSpace(data)) {
						t.Fatalf("脱敏后的条目不是有效 JSON: %s", entry.Name)
					}
					if entry.Name == "resources/feed.json" && !bytes.Equal(data, resources[2].data) {
						t.Fatal("普通 Feed 响应被脱敏过程修改")
					}
				}
			})
		}
	}
}
