package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authn "gofeed/internal/auth"
	dbpkg "gofeed/internal/db"
	jwtmw "gofeed/internal/middleware/jwt"
	"gofeed/internal/testutil"
	"gofeed/internal/user"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// videoTestVideoHeader 是校验通过的最小 mp4 文件头
var videoTestVideoHeader = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

// videoTestImageHeader 是校验通过的最小 png 文件头
var videoTestImageHeader = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// videoJSONRequest 发送可选 JSON 请求体与可选访问令牌的 HTTP 请求
func videoJSONRequest(t *testing.T, engine *gin.Engine, method, path, token string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if payload == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	request := httptest.NewRequest(method, path, reader)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// videoMultipartRequest 以多部分表单上传单个媒体文件
func videoMultipartRequest(t *testing.T, engine *gin.Engine, path, token, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("创建表单文件失败: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, path, &form)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// videoResponseBody 解析响应体为通用映射以便断言字段契约
func videoResponseBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v body=%s", err, recorder.Body.String())
	}
	return body
}

// videoErrorMessage 断言状态码并读取错误响应的公开文案
func videoErrorMessage(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) string {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	message, ok := videoResponseBody(t, recorder)["error"].(string)
	if !ok {
		t.Fatalf("错误响应缺少 error 字段 body=%s", recorder.Body.String())
	}
	return message
}

// videoListIDs 读取列表响应中的视频标识顺序
func videoListIDs(t *testing.T, recorder *httptest.ResponseRecorder) []uint {
	t.Helper()
	value, exists := videoResponseBody(t, recorder)["items"]
	if !exists {
		t.Fatalf("列表响应缺少 items body=%s", recorder.Body.String())
	}
	if value == nil {
		return []uint{}
	}
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("列表响应 items 格式错误 body=%s", recorder.Body.String())
	}
	ids := make([]uint, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("列表项格式错误 got=%v", entry)
		}
		value, ok := item["id"].(float64)
		if !ok {
			t.Fatalf("列表项缺少标识 got=%v", item)
		}
		ids = append(ids, uint(value))
	}
	return ids
}

// assertVideoIDs 断言列表响应中的视频标识与顺序
func assertVideoIDs(t *testing.T, got, want []uint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("列表长度错误 got=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("列表顺序错误 got=%v want=%v", got, want)
		}
	}
}

// sqlEngagementReader 使用真实 SQL 聚合互动计数以替代 social 包避免测试包循环依赖
type sqlEngagementReader struct {
	db *gorm.DB
}

func (r sqlEngagementReader) GetEngagementCounts(ctx context.Context, videoIDs []uint) (map[uint]EngagementCounts, error) {
	counts := make(map[uint]EngagementCounts, len(videoIDs))
	if len(videoIDs) == 0 {
		return counts, nil
	}
	for _, id := range videoIDs {
		counts[id] = EngagementCounts{}
	}

	type aggregate struct {
		VideoID uint
		Total   int64
	}
	var likes []aggregate
	if err := r.db.WithContext(ctx).Table("video_likes").
		Select("video_id, COUNT(*) AS total").Where("video_id IN ?", videoIDs).
		Group("video_id").Scan(&likes).Error; err != nil {
		return nil, err
	}
	for _, item := range likes {
		entry := counts[item.VideoID]
		entry.LikesCount = item.Total
		counts[item.VideoID] = entry
	}

	var comments []aggregate
	if err := r.db.WithContext(ctx).Table("video_comments").
		Select("video_id, COUNT(*) AS total").
		Where("video_id IN ? AND deleted_at IS NULL", videoIDs).
		Group("video_id").Scan(&comments).Error; err != nil {
		return nil, err
	}
	for _, item := range comments {
		entry := counts[item.VideoID]
		entry.CommentsCount = item.Total
		counts[item.VideoID] = entry
	}
	return counts, nil
}

// newVideoHTTPEngine 装配视频模块的公开读取与认证写入端点
func newVideoHTTPEngine(t *testing.T, middlewares ...gin.HandlerFunc) (*gin.Engine, *gorm.DB, *Repository, *authn.SessionService) {
	t.Helper()
	gdb := testutil.DB(t)
	repo := NewRepository(gdb)
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	service := NewService(repo, NewUserAuthorReader(user.NewRepository(gdb)), sqlEngagementReader{db: gdb})
	controller := NewController(service, NewLocalStorage(t.TempDir()))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middlewares...)
	engine.GET("/api/video", controller.GetVideoList)
	engine.GET("/api/video/:id", controller.GetVideo)

	authorized := engine.Group("/api/video/auth", jwtmw.Auth(sessions))
	authorized.POST("/drafts", controller.CreateDraft)
	authorized.GET("/drafts/:id", controller.GetDraft)
	authorized.POST("/drafts/:id/play", controller.UpdateDraftVideo)
	authorized.POST("/drafts/:id/cover", controller.UpdateDraftCover)
	authorized.POST("/drafts/:id/publish", controller.UpdateDraftPublication)
	authorized.DELETE("/drafts/:id", controller.DiscardDraft)
	authorized.GET("/mine", controller.GetMyVideoList)
	authorized.GET("/:id/status", controller.GetVideoStatus)
	authorized.DELETE("/:id", controller.DeleteVideo)
	return engine, gdb, repo, sessions
}

// newVideoAuthor 创建作者账号并签发访问令牌
func newVideoAuthor(t *testing.T, gdb *gorm.DB, username string) (*user.User, string) {
	t.Helper()
	account := &user.User{Username: username, Password: "test-password-hash"}
	if err := gdb.Create(account).Error; err != nil {
		t.Fatalf("创建用户 %q 失败: %v", username, err)
	}
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	pair, err := sessions.Create(context.Background(), account.ID, account.Username)
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	return account, pair.AccessToken
}

// videoInteractions 写入真实点赞与评论行以验证互动计数
func videoInteractions(t *testing.T, gdb *gorm.DB, videoID uint, likerIDs []uint, commenters []uint) {
	t.Helper()
	now := time.Now()
	for _, likerID := range likerIDs {
		if err := gdb.Exec("INSERT INTO video_likes (video_id, user_id, created_at) VALUES (?, ?, ?)",
			videoID, likerID, now).Error; err != nil {
			t.Fatalf("写入点赞失败: %v", err)
		}
	}
	for index, authorID := range commenters {
		if err := gdb.Exec("INSERT INTO video_comments (video_id, author_id, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			videoID, authorID, fmt.Sprintf("评论 %d", index+1), now, now).Error; err != nil {
			t.Fatalf("写入评论失败: %v", err)
		}
	}
}

// 测试目标：验证草稿上传与发布的完整 HTTP 状态契约
// 预期效果：建草稿 201、媒体不完整发布 409、上传媒体 201、发布 202 且状态转为 processing
func TestVideoDraftPublishHTTPContract(t *testing.T) {
	engine, gdb, _, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "draft-author")

	created := videoJSONRequest(t, engine, http.MethodPost, "/api/video/auth/drafts", token,
		map[string]any{"title": "我的第一个作品", "description": "草稿描述"})
	if created.Code != http.StatusCreated {
		t.Fatalf("建草稿状态错误 got=%d body=%s", created.Code, created.Body.String())
	}
	draft, ok := videoResponseBody(t, created)["draft"].(map[string]any)
	if !ok {
		t.Fatalf("建草稿响应缺少 draft body=%s", created.Body.String())
	}
	if draft["status"] != VideoStatusDraft || draft["has_video"] != false || draft["has_cover"] != false {
		t.Fatalf("草稿初始字段错误 got=%v", draft)
	}
	draftID := uint(draft["id"].(float64))

	incomplete := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, nil)
	if message := videoErrorMessage(t, incomplete, http.StatusConflict); message != ErrDraftIncomplete.Error() {
		t.Fatalf("媒体不完整发布文案错误 got=%q", message)
	}

	play := videoMultipartRequest(t, engine, fmt.Sprintf("/api/video/auth/drafts/%d/play", draftID), token,
		"clip.mp4", videoTestVideoHeader)
	if play.Code != http.StatusCreated {
		t.Fatalf("上传视频状态错误 got=%d body=%s", play.Code, play.Body.String())
	}
	playBody := videoResponseBody(t, play)
	if uint(playBody["draft_id"].(float64)) != draftID {
		t.Fatalf("上传视频 draft_id 错误 got=%v", playBody)
	}
	if !strings.HasPrefix(playBody["play_url"].(string), fmt.Sprintf("/static/videos/%d/", author.ID)) ||
		!strings.HasSuffix(playBody["play_url"].(string), playBody["play_file_name"].(string)) {
		t.Fatalf("上传视频地址归属错误 got=%v", playBody)
	}
	if playBody["play_original_name"] != "clip.mp4" {
		t.Fatalf("上传视频原始文件名错误 got=%v", playBody)
	}

	cover := videoMultipartRequest(t, engine, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draftID), token,
		"cover.png", videoTestImageHeader)
	if cover.Code != http.StatusCreated {
		t.Fatalf("上传封面状态错误 got=%d body=%s", cover.Code, cover.Body.String())
	}
	coverBody := videoResponseBody(t, cover)
	if !strings.HasPrefix(coverBody["cover_url"].(string), fmt.Sprintf("/static/covers/%d/", author.ID)) ||
		!strings.HasSuffix(coverBody["cover_url"].(string), coverBody["cover_file_name"].(string)) {
		t.Fatalf("上传封面地址归属错误 got=%v", coverBody)
	}

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/auth/drafts/%d", draftID), token, nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("草稿详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	detailDraft := videoResponseBody(t, detail)["draft"].(map[string]any)
	if detailDraft["has_video"] != true || detailDraft["has_cover"] != true {
		t.Fatalf("草稿媒体标记错误 got=%v", detailDraft)
	}

	withBody := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, map[string]any{"title": "覆盖"})
	if message := videoErrorMessage(t, withBody, http.StatusBadRequest); message != "publish draft does not accept a request body" {
		t.Fatalf("发布请求体文案错误 got=%q", message)
	}

	published := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, nil)
	if published.Code != http.StatusAccepted {
		t.Fatalf("发布状态错误 got=%d body=%s", published.Code, published.Body.String())
	}
	publishedDraft := videoResponseBody(t, published)["draft"].(map[string]any)
	if publishedDraft["status"] != VideoStatusProcessing {
		t.Fatalf("发布后状态错误 got=%v", publishedDraft)
	}

	repeated := videoJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/drafts/%d/publish", draftID), token, nil)
	if message := videoErrorMessage(t, repeated, http.StatusConflict); message != ErrDraftNotWritable.Error() {
		t.Fatalf("重复发布文案错误 got=%q", message)
	}

	processing := videoJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video/auth/%d/status", draftID), token, nil)
	if processing.Code != http.StatusOK {
		t.Fatalf("处理状态查询错误 got=%d body=%s", processing.Code, processing.Body.String())
	}
	if videoResponseBody(t, processing)["status"] != VideoStatusProcessing {
		t.Fatalf("处理状态字段错误 got=%s", processing.Body.String())
	}

	anonymous := videoJSONRequest(t, engine, http.MethodPost, "/api/video/auth/drafts", "",
		map[string]any{"title": "未登录"})
	if message := videoErrorMessage(t, anonymous, http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录建草稿文案错误 got=%q", message)
	}
}

// 测试目标：验证公开列表与详情的可见性、排序与互动计数语义
// 预期效果：仅完整已发布作品可见，同发布时间按标识倒序，计数随真实互动增长
func TestVideoPublicVisibilityHTTPContract(t *testing.T) {
	engine, gdb, repo, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "list-author")
	other, otherToken := newVideoAuthor(t, gdb, "list-other")

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
	first := seedVideo(t, repo, author.ID, "第一条", VideoStatusPublished, base)
	second := seedVideo(t, repo, author.ID, "第二条", VideoStatusPublished, base)
	newest := seedVideo(t, repo, author.ID, "最新一条", VideoStatusPublished, base.Add(time.Hour))
	draft := seedVideo(t, repo, author.ID, "未发布草稿", VideoStatusDraft, base)
	foreign := seedVideo(t, repo, other.ID, "他人作品", VideoStatusPublished, base.Add(2*time.Hour))
	removed := seedVideo(t, repo, author.ID, "已删除作品", VideoStatusPublished, base.Add(3*time.Hour))
	setVideoDeletedAt(t, gdb, removed.ID, time.Now())
	incomplete := newVideoFixture(author.ID, "媒体不完整作品", VideoStatusPublished, base.Add(4*time.Hour))
	incomplete.CoverFileName = ""
	if err := repo.Create(context.Background(), incomplete); err != nil {
		t.Fatalf("写入不完整作品失败: %v", err)
	}

	public := videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)
	if public.Code != http.StatusOK {
		t.Fatalf("公开列表状态错误 got=%d body=%s", public.Code, public.Body.String())
	}
	assertVideoIDs(t, videoListIDs(t, public), []uint{foreign.ID, newest.ID, second.ID, first.ID})

	byAuthor := videoJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video?author_id=%d", author.ID), "", nil)
	assertVideoIDs(t, videoListIDs(t, byAuthor), []uint{newest.ID, second.ID, first.ID})

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video?author_id=abc", "", nil),
		http.StatusBadRequest); message != ErrInvalidAuthorID.Error() {
		t.Fatalf("非法作者参数文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video?limit=99", "", nil),
		http.StatusBadRequest); message != ErrInvalidLimit.Error() {
		t.Fatalf("非法分页文案错误 got=%q", message)
	}

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", first.ID), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("公开详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	item := videoResponseBody(t, detail)["video"].(map[string]any)
	detailAuthor := item["author"].(map[string]any)
	if detailAuthor["username"] != "list-author" || detailAuthor["id"].(float64) != float64(author.ID) {
		t.Fatalf("公开详情作者错误 got=%v", detailAuthor)
	}
	if item["likes_count"].(float64) != 0 || item["comments_count"].(float64) != 0 {
		t.Fatalf("初始互动计数错误 got=%v", item)
	}

	videoInteractions(t, gdb, first.ID, []uint{other.ID, author.ID}, []uint{other.ID})
	liked := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", first.ID), "", nil)
	likedItem := videoResponseBody(t, liked)["video"].(map[string]any)
	if likedItem["likes_count"].(float64) != 2 || likedItem["comments_count"].(float64) != 1 {
		t.Fatalf("互动计数错误 got=%v", likedItem)
	}

	for name, id := range map[string]uint{
		"草稿":    draft.ID,
		"已删除":   removed.ID,
		"媒体不完整": incomplete.ID,
	} {
		message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", id), "", nil),
			http.StatusNotFound)
		if message != "video not found" {
			t.Fatalf("%s作品详情文案错误 got=%q", name, message)
		}
	}

	mine := videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", token, nil)
	if mine.Code != http.StatusOK {
		t.Fatalf("我的视频状态错误 got=%d body=%s", mine.Code, mine.Body.String())
	}
	assertVideoIDs(t, videoListIDs(t, mine), []uint{newest.ID, second.ID, first.ID})

	foreignMine := videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", otherToken, nil)
	assertVideoIDs(t, videoListIDs(t, foreignMine), []uint{foreign.ID})
}

// 测试目标：验证已注销作者的公开作品保留占位作者信息
// 预期效果：作者账号软删除后作品仍可读且作者名为已注销用户
func TestVideoDeletedAuthorPlaceholderHTTPContract(t *testing.T) {
	engine, gdb, repo, _ := newVideoHTTPEngine(t)
	author, _ := newVideoAuthor(t, gdb, "gone-author")
	video := seedVideo(t, repo, author.ID, "作者已注销", VideoStatusPublished, time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local))

	if err := gdb.Delete(&user.User{}, author.ID).Error; err != nil {
		t.Fatalf("软删除作者失败: %v", err)
	}

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", video.ID), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("已注销作者作品状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	item := videoResponseBody(t, detail)["video"].(map[string]any)
	placeholder := item["author"].(map[string]any)
	if placeholder["username"] != deletedUsername {
		t.Fatalf("已注销作者占位错误 got=%v", placeholder)
	}
	if uint(placeholder["id"].(float64)) != author.ID {
		t.Fatalf("已注销作者标识丢失 got=%v", placeholder)
	}

	list := videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)
	assertVideoIDs(t, videoListIDs(t, list), []uint{video.ID})
}

// 测试目标：验证视频删除的权限与可见性契约
// 预期效果：非作者删除 403、作者删除 204 后详情 404 且公开列表不再包含
func TestVideoDeleteHTTPContract(t *testing.T) {
	engine, gdb, repo, _ := newVideoHTTPEngine(t)
	author, token := newVideoAuthor(t, gdb, "delete-author")
	_, otherToken := newVideoAuthor(t, gdb, "delete-other")
	video := seedVideo(t, repo, author.ID, "待删除作品", VideoStatusPublished, time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local))
	path := fmt.Sprintf("/api/video/auth/%d", video.ID)

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, otherToken, nil),
		http.StatusForbidden); message != ErrNotAuthor.Error() {
		t.Fatalf("非作者删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, "", nil),
		http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, "not-a-token", nil),
		http.StatusUnauthorized); message != "invalid or expired token" {
		t.Fatalf("非法令牌删除文案错误 got=%q", message)
	}
	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, "/api/video/auth/abc", token, nil),
		http.StatusBadRequest); message != ErrInvalidVideoID.Error() {
		t.Fatalf("非法标识删除文案错误 got=%q", message)
	}

	deleted := videoJSONRequest(t, engine, http.MethodDelete, path, token, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除状态错误 got=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if deleted.Body.Len() != 0 {
		t.Fatalf("删除响应体应为空 got=%s", deleted.Body.String())
	}

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", video.ID), "", nil),
		http.StatusNotFound); message != "video not found" {
		t.Fatalf("删除后详情文案错误 got=%q", message)
	}
	assertVideoIDs(t, videoListIDs(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)), []uint{})
	assertVideoIDs(t, videoListIDs(t, videoJSONRequest(t, engine, http.MethodGet, "/api/video/auth/mine", token, nil)), []uint{})

	if message := videoErrorMessage(t, videoJSONRequest(t, engine, http.MethodDelete, path, token, nil),
		http.StatusNotFound); message != "video not found" {
		t.Fatalf("重复删除文案错误 got=%q", message)
	}
}

// videoQueryCapture 记录请求内真实执行的 SQL 语句数量
type videoQueryCapture struct {
	counts []int64
}

func (q *videoQueryCapture) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(dbpkg.WithQueryCounter(c.Request.Context()))
		c.Next()
		q.counts = append(q.counts, dbpkg.QueryCount(c.Request.Context()))
	}
}

// videoQueryBudget 断言目标请求的语句数量落在预算内并返回实际值
func videoQueryBudget(t *testing.T, capture *videoQueryCapture, before int, budget int64) int64 {
	t.Helper()
	if len(capture.counts) != before+1 {
		t.Fatalf("应只新增一次请求记录 got=%d want=%d", len(capture.counts), before+1)
	}
	got := capture.counts[len(capture.counts)-1]
	if got < 1 || got > budget {
		t.Fatalf("查询预算超限 got=%d want 1..%d", got, budget)
	}
	return got
}

// 测试目标：验证公开读取端点的真实 SQL 语句数量不随列表长度增长
// 预期效果：列表与详情各自最多四条语句且六条作品时不出现逐条查询
func TestVideoReadEndpointsQueryBudget(t *testing.T) {
	capture := &videoQueryCapture{}
	engine, gdb, repo, _ := newVideoHTTPEngine(t, capture.middleware())
	if err := dbpkg.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}

	author, _ := newVideoAuthor(t, gdb, "budget-author")
	liker, _ := newVideoAuthor(t, gdb, "budget-liker")
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local)
	var ids []uint
	for index := 0; index < 6; index++ {
		video := seedVideo(t, repo, author.ID, fmt.Sprintf("预算作品 %d", index), VideoStatusPublished, base.Add(time.Duration(index)*time.Minute))
		ids = append(ids, video.ID)
	}
	videoInteractions(t, gdb, ids[0], []uint{liker.ID}, []uint{liker.ID})

	capture.counts = nil
	list := videoJSONRequest(t, engine, http.MethodGet, "/api/video", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("公开列表状态错误 got=%d body=%s", list.Code, list.Body.String())
	}
	if len(videoListIDs(t, list)) != 6 {
		t.Fatalf("公开列表条数错误 body=%s", list.Body.String())
	}
	listQueries := videoQueryBudget(t, capture, 0, 4)
	t.Logf("公开列表语句数量=%d", listQueries)

	detail := videoJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", ids[0]), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("公开详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	detailQueries := videoQueryBudget(t, capture, 1, 4)
	t.Logf("公开详情语句数量=%d", detailQueries)
}
