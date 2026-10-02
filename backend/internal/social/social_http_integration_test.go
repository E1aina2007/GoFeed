package social

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authn "gofeed/internal/auth"
	dbpkg "gofeed/internal/db"
	jwtmw "gofeed/internal/middleware/jwt"
	"gofeed/internal/testutil"
	"gofeed/internal/user"
	"gofeed/internal/video"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// socialJSONRequest 发送可选 JSON 请求体与可选访问令牌的 HTTP 请求
func socialJSONRequest(t *testing.T, engine *gin.Engine, method, path, token string, payload any) *httptest.ResponseRecorder {
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

// socialResponseBody 解析响应体为通用映射以便断言字段契约
func socialResponseBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v body=%s", err, recorder.Body.String())
	}
	return body
}

// socialErrorMessage 断言状态码并读取错误响应的公开文案
func socialErrorMessage(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) string {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	message, ok := socialResponseBody(t, recorder)["error"].(string)
	if !ok {
		t.Fatalf("错误响应缺少 error 字段 body=%s", recorder.Body.String())
	}
	return message
}

// socialObject 读取响应体中的嵌套对象
func socialObject(t *testing.T, recorder *httptest.ResponseRecorder, key string) map[string]any {
	t.Helper()
	object, ok := socialResponseBody(t, recorder)[key].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 %s 对象 body=%s", key, recorder.Body.String())
	}
	return object
}

// socialItems 读取列表响应中的条目
func socialItems(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	value, exists := socialResponseBody(t, recorder)["items"]
	if !exists {
		t.Fatalf("列表响应缺少 items body=%s", recorder.Body.String())
	}
	if value == nil {
		return nil
	}
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("列表响应 items 格式错误 body=%s", recorder.Body.String())
	}
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("列表项格式错误 got=%v", entry)
		}
		items = append(items, item)
	}
	return items
}

// socialNumber 读取对象中的数值字段
func socialNumber(t *testing.T, object map[string]any, key string) int64 {
	t.Helper()
	value, ok := object[key].(float64)
	if !ok {
		t.Fatalf("字段 %s 不是数值 got=%v", key, object)
	}
	return int64(value)
}

// socialToken 为用户签发访问令牌
func socialToken(t *testing.T, gdb *gorm.DB, account *user.User) string {
	t.Helper()
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	pair, err := sessions.Create(context.Background(), account.ID, account.Username)
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	return pair.AccessToken
}

// newSocialHTTPEngine 装配社交互动端点并挂载公开视频详情以校验互动计数
func newSocialHTTPEngine(t *testing.T, middlewares ...gin.HandlerFunc) (*gin.Engine, *gorm.DB, *Repository) {
	t.Helper()
	gdb := testutil.DB(t)
	repo := NewRepository(gdb)
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	socialCtl := NewController(NewService(repo))
	videoCtl := video.NewController(
		video.NewService(video.NewRepository(gdb), video.NewUserAuthorReader(user.NewRepository(gdb)), repo),
		video.NewLocalStorage(t.TempDir()),
	)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middlewares...)
	engine.GET("/api/video/:id", videoCtl.GetVideo)
	engine.GET("/api/video/:id/comments", socialCtl.GetCommentList)
	engine.GET("/api/user/:id/followers", socialCtl.GetFollowerList)
	engine.GET("/api/user/:id/following", socialCtl.GetFollowingList)

	protectedUsers := engine.Group("/api/user/auth", jwtmw.Auth(sessions))
	protectedUsers.GET("/:id/follow", socialCtl.GetFollowState)
	protectedUsers.PUT("/:id/follow", socialCtl.CreateFollow)
	protectedUsers.DELETE("/:id/follow", socialCtl.RemoveFollow)

	protectedVideos := engine.Group("/api/video/auth", jwtmw.Auth(sessions))
	protectedVideos.GET("/:id/like", socialCtl.GetLikeState)
	protectedVideos.PUT("/:id/like", socialCtl.CreateLike)
	protectedVideos.DELETE("/:id/like", socialCtl.RemoveLike)
	protectedVideos.POST("/:id/comments", socialCtl.CreateComment)
	protectedVideos.DELETE("/:id/comments/:commentID", socialCtl.DeleteComment)
	return engine, gdb, repo
}

// seedDraftVideo 写入仅作者可见的草稿作品以校验互动边界
func seedDraftVideo(t *testing.T, gdb *gorm.DB, authorID uint) *video.Video {
	t.Helper()
	draft := &video.Video{AuthorID: authorID, Title: "社交草稿", Status: video.VideoStatusDraft}
	if err := video.NewRepository(gdb).Create(context.Background(), draft); err != nil {
		t.Fatalf("写入草稿失败: %v", err)
	}
	return draft
}

// seedCommentRows 直接写入评论行以便校验列表预算与排序
func seedCommentRows(t *testing.T, gdb *gorm.DB, videoID uint, authorIDs []uint) {
	t.Helper()
	base := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for index, authorID := range authorIDs {
		createdAt := base.Add(time.Duration(index) * time.Minute)
		if err := gdb.Exec("INSERT INTO video_comments (video_id, author_id, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			videoID, authorID, fmt.Sprintf("预算评论 %d", index+1), createdAt, createdAt).Error; err != nil {
			t.Fatalf("写入评论失败: %v", err)
		}
	}
}

// 测试目标：验证点赞增删的状态与计数契约
// 预期效果：点赞 200 且计数递增、重复点赞幂等、取消后计数回落、不可见作品返回 404
func TestSocialLikeHTTPContract(t *testing.T) {
	engine, gdb, _ := newSocialHTTPEngine(t)
	author := seedUser(t, gdb, "like-author")
	published := seedPublishedVideo(t, gdb, author.ID)
	draft := seedDraftVideo(t, gdb, author.ID)
	viewer := seedUser(t, gdb, "like-viewer")
	token := socialToken(t, gdb, viewer)

	likePath := fmt.Sprintf("/api/video/auth/%d/like", published.ID)
	initial := socialJSONRequest(t, engine, http.MethodGet, likePath, token, nil)
	if initial.Code != http.StatusOK {
		t.Fatalf("点赞状态查询错误 got=%d body=%s", initial.Code, initial.Body.String())
	}
	initialBody := socialResponseBody(t, initial)
	if initialBody["liked"] != false || socialNumber(t, initialBody, "likes_count") != 0 {
		t.Fatalf("初始点赞状态错误 got=%v", initialBody)
	}

	liked := socialJSONRequest(t, engine, http.MethodPut, likePath, token, nil)
	if liked.Code != http.StatusOK {
		t.Fatalf("点赞状态错误 got=%d body=%s", liked.Code, liked.Body.String())
	}
	likedBody := socialResponseBody(t, liked)
	if likedBody["liked"] != true || socialNumber(t, likedBody, "likes_count") != 1 {
		t.Fatalf("点赞结果错误 got=%v", likedBody)
	}

	repeated := socialJSONRequest(t, engine, http.MethodPut, likePath, token, nil)
	repeatedBody := socialResponseBody(t, repeated)
	if repeatedBody["liked"] != true || socialNumber(t, repeatedBody, "likes_count") != 1 {
		t.Fatalf("重复点赞应幂等 got=%v", repeatedBody)
	}

	detail := socialJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", published.ID), "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("视频详情状态错误 got=%d body=%s", detail.Code, detail.Body.String())
	}
	if count := socialNumber(t, socialObject(t, detail, "video"), "likes_count"); count != 1 {
		t.Fatalf("视频详情点赞计数错误 got=%d want=1", count)
	}

	removed := socialJSONRequest(t, engine, http.MethodDelete, likePath, token, nil)
	removedBody := socialResponseBody(t, removed)
	if removedBody["liked"] != false || socialNumber(t, removedBody, "likes_count") != 0 {
		t.Fatalf("取消点赞结果错误 got=%v", removedBody)
	}
	removedAgain := socialJSONRequest(t, engine, http.MethodDelete, likePath, token, nil)
	removedAgainBody := socialResponseBody(t, removedAgain)
	if removedAgainBody["liked"] != false || socialNumber(t, removedAgainBody, "likes_count") != 0 {
		t.Fatalf("重复取消应幂等 got=%v", removedAgainBody)
	}

	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPut, "/api/video/auth/999999/like", token, nil),
		http.StatusNotFound); message != ErrVideoNotFound.Error() {
		t.Fatalf("不存在作品点赞文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPut,
		fmt.Sprintf("/api/video/auth/%d/like", draft.ID), token, nil), http.StatusNotFound); message != ErrVideoNotFound.Error() {
		t.Fatalf("草稿点赞文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPut, "/api/video/auth/abc/like", token, nil),
		http.StatusBadRequest); message != ErrInvalidVideoID.Error() {
		t.Fatalf("非法作品标识文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodGet, likePath, "", nil),
		http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录点赞文案错误 got=%q", message)
	}
}

// 测试目标：验证评论创建、列表、删除的状态与计数契约
// 预期效果：创建 201 且内容去空白、列表按时间与标识倒序、非作者删除 403、删除 204 且计数回落
func TestSocialCommentHTTPContract(t *testing.T) {
	engine, gdb, _ := newSocialHTTPEngine(t)
	author := seedUser(t, gdb, "comment-author")
	published := seedPublishedVideo(t, gdb, author.ID)
	first := seedUser(t, gdb, "comment-first")
	firstToken := socialToken(t, gdb, first)
	second := seedUser(t, gdb, "comment-second")
	secondToken := socialToken(t, gdb, second)

	commentsPath := fmt.Sprintf("/api/video/auth/%d/comments", published.ID)
	if items := socialItems(t, socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video/%d/comments", published.ID), "", nil)); len(items) != 0 {
		t.Fatalf("初始评论列表应为空 got=%v", items)
	}

	created := socialJSONRequest(t, engine, http.MethodPost, commentsPath, firstToken,
		map[string]any{"content": "  第一条评论  "})
	if created.Code != http.StatusCreated {
		t.Fatalf("创建评论状态错误 got=%d body=%s", created.Code, created.Body.String())
	}
	comment := socialObject(t, created, "comment")
	if comment["content"] != "第一条评论" {
		t.Fatalf("评论内容未按预期规范化 got=%v", comment)
	}
	if socialNumber(t, comment, "video_id") != int64(published.ID) {
		t.Fatalf("评论作品标识错误 got=%v", comment)
	}
	if authorObject := comment["author"].(map[string]any); authorObject["username"] != first.Username {
		t.Fatalf("评论作者错误 got=%v", authorObject)
	}
	firstCommentID := uint(socialNumber(t, comment, "id"))

	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPost, commentsPath, firstToken,
		map[string]any{"content": "   "}), http.StatusBadRequest); message != ErrInvalidCommentContent.Error() {
		t.Fatalf("空评论文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPost,
		"/api/video/auth/999999/comments", firstToken, map[string]any{"content": "无主评论"}),
		http.StatusNotFound); message != ErrVideoNotFound.Error() {
		t.Fatalf("不存在作品评论文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPost, commentsPath, "",
		map[string]any{"content": "未登录评论"}), http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录评论文案错误 got=%q", message)
	}

	secondCreated := socialJSONRequest(t, engine, http.MethodPost, commentsPath, secondToken,
		map[string]any{"content": "第二条评论"})
	if secondCreated.Code != http.StatusCreated {
		t.Fatalf("第二条评论状态错误 got=%d body=%s", secondCreated.Code, secondCreated.Body.String())
	}
	secondCommentID := uint(socialNumber(t, socialObject(t, secondCreated, "comment"), "id"))

	listed := socialJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d/comments", published.ID), "", nil)
	items := socialItems(t, listed)
	if len(items) != 2 {
		t.Fatalf("评论列表长度错误 got=%v", items)
	}
	if uint(socialNumber(t, items[0], "id")) != secondCommentID || uint(socialNumber(t, items[1], "id")) != firstCommentID {
		t.Fatalf("评论列表顺序错误 got=%v", items)
	}

	detail := socialJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", published.ID), "", nil)
	if count := socialNumber(t, socialObject(t, detail, "video"), "comments_count"); count != 2 {
		t.Fatalf("视频详情评论计数错误 got=%d want=2", count)
	}

	deletePath := fmt.Sprintf("/api/video/auth/%d/comments/%d", published.ID, firstCommentID)
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodDelete, deletePath, secondToken, nil),
		http.StatusForbidden); message != ErrCommentNotAuthor.Error() {
		t.Fatalf("非作者删除评论文案错误 got=%q", message)
	}

	deleted := socialJSONRequest(t, engine, http.MethodDelete, deletePath, firstToken, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除评论状态错误 got=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if deleted.Body.Len() != 0 {
		t.Fatalf("删除评论响应体应为空 got=%s", deleted.Body.String())
	}

	remaining := socialItems(t, socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video/%d/comments", published.ID), "", nil))
	if len(remaining) != 1 || uint(socialNumber(t, remaining[0], "id")) != secondCommentID {
		t.Fatalf("删除后评论列表错误 got=%v", remaining)
	}
	afterDelete := socialJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d", published.ID), "", nil)
	if count := socialNumber(t, socialObject(t, afterDelete, "video"), "comments_count"); count != 1 {
		t.Fatalf("删除后评论计数错误 got=%d want=1", count)
	}

	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodDelete, deletePath, firstToken, nil),
		http.StatusNotFound); message != ErrCommentNotFound.Error() {
		t.Fatalf("重复删除评论文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/video/auth/999999/comments/%d", secondCommentID), secondToken, nil),
		http.StatusNotFound); message != ErrCommentNotFound.Error() {
		t.Fatalf("作品与评论不匹配的删除文案错误 got=%q", message)
	}

	if err := gdb.Delete(&user.User{}, second.ID).Error; err != nil {
		t.Fatalf("软删除评论作者失败: %v", err)
	}
	placeholder := socialItems(t, socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video/%d/comments", published.ID), "", nil))
	if len(placeholder) != 1 {
		t.Fatalf("评论列表长度错误 got=%v", placeholder)
	}
	placeholderAuthor := placeholder[0]["author"].(map[string]any)
	if placeholderAuthor["username"] != deletedUsername {
		t.Fatalf("已注销评论作者占位错误 got=%v", placeholderAuthor)
	}
}

// 测试目标：验证关注关系的状态与计数契约
// 预期效果：关注 200 且计数递增、重复关注幂等、自关注 400、注销账号不计入关注列表
func TestSocialFollowHTTPContract(t *testing.T) {
	engine, gdb, _ := newSocialHTTPEngine(t)
	followee := seedUser(t, gdb, "follow-target")
	follower := seedUser(t, gdb, "follow-source")
	followerToken := socialToken(t, gdb, follower)
	other := seedUser(t, gdb, "follow-other")
	otherToken := socialToken(t, gdb, other)

	followPath := fmt.Sprintf("/api/user/auth/%d/follow", followee.ID)
	initial := socialJSONRequest(t, engine, http.MethodGet, followPath, followerToken, nil)
	if initial.Code != http.StatusOK {
		t.Fatalf("关注状态查询错误 got=%d body=%s", initial.Code, initial.Body.String())
	}
	initialBody := socialResponseBody(t, initial)
	if initialBody["following"] != false || socialNumber(t, initialBody, "follower_count") != 0 {
		t.Fatalf("初始关注状态错误 got=%v", initialBody)
	}

	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPut,
		fmt.Sprintf("/api/user/auth/%d/follow", follower.ID), followerToken, nil),
		http.StatusBadRequest); message != ErrSelfFollow.Error() {
		t.Fatalf("自关注文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPut,
		"/api/user/auth/999999/follow", followerToken, nil), http.StatusNotFound); message != ErrUserNotFound.Error() {
		t.Fatalf("不存在用户关注文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodPut, followPath, "", nil),
		http.StatusUnauthorized); message != "missing authorization header" {
		t.Fatalf("未登录关注文案错误 got=%q", message)
	}

	followed := socialJSONRequest(t, engine, http.MethodPut, followPath, followerToken, nil)
	if followed.Code != http.StatusOK {
		t.Fatalf("关注状态错误 got=%d body=%s", followed.Code, followed.Body.String())
	}
	followedBody := socialResponseBody(t, followed)
	if followedBody["following"] != true || socialNumber(t, followedBody, "follower_count") != 1 {
		t.Fatalf("关注结果错误 got=%v", followedBody)
	}
	repeated := socialResponseBody(t, socialJSONRequest(t, engine, http.MethodPut, followPath, followerToken, nil))
	if repeated["following"] != true || socialNumber(t, repeated, "follower_count") != 1 {
		t.Fatalf("重复关注应幂等 got=%v", repeated)
	}

	followers := socialItems(t, socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/user/%d/followers", followee.ID), "", nil))
	if len(followers) != 1 {
		t.Fatalf("关注者列表长度错误 got=%v", followers)
	}
	if account := followers[0]["user"].(map[string]any); account["username"] != follower.Username {
		t.Fatalf("关注者列表内容错误 got=%v", account)
	}
	following := socialItems(t, socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/user/%d/following", follower.ID), "", nil))
	if len(following) != 1 {
		t.Fatalf("关注列表长度错误 got=%v", following)
	}
	if account := following[0]["user"].(map[string]any); account["username"] != followee.Username {
		t.Fatalf("关注列表内容错误 got=%v", account)
	}

	secondFollow := socialResponseBody(t, socialJSONRequest(t, engine, http.MethodPut, followPath, otherToken, nil))
	if socialNumber(t, secondFollow, "follower_count") != 2 {
		t.Fatalf("第二位关注者计数错误 got=%v", secondFollow)
	}

	unfollowed := socialResponseBody(t, socialJSONRequest(t, engine, http.MethodDelete, followPath, followerToken, nil))
	if unfollowed["following"] != false || socialNumber(t, unfollowed, "follower_count") != 1 {
		t.Fatalf("取关结果错误 got=%v", unfollowed)
	}
	unfollowedAgain := socialResponseBody(t, socialJSONRequest(t, engine, http.MethodDelete, followPath, followerToken, nil))
	if unfollowedAgain["following"] != false || socialNumber(t, unfollowedAgain, "follower_count") != 1 {
		t.Fatalf("重复取关应幂等 got=%v", unfollowedAgain)
	}

	refollowed := socialResponseBody(t, socialJSONRequest(t, engine, http.MethodPut, followPath, followerToken, nil))
	if socialNumber(t, refollowed, "follower_count") != 2 {
		t.Fatalf("重新关注计数错误 got=%v", refollowed)
	}

	if err := gdb.Delete(&user.User{}, other.ID).Error; err != nil {
		t.Fatalf("软删除关注者失败: %v", err)
	}
	active := socialItems(t, socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/user/%d/followers", followee.ID), "", nil))
	if len(active) != 1 {
		t.Fatalf("注销关注者不应出现在列表 got=%v", active)
	}
	if account := active[0]["user"].(map[string]any); account["username"] != follower.Username {
		t.Fatalf("注销后关注者列表内容错误 got=%v", account)
	}

	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodGet,
		"/api/user/999999/followers", "", nil), http.StatusNotFound); message != ErrUserNotFound.Error() {
		t.Fatalf("不存在用户关注者列表文案错误 got=%q", message)
	}
	if message := socialErrorMessage(t, socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/user/%d/followers?limit=99", followee.ID), "", nil),
		http.StatusBadRequest); message != ErrInvalidLimit.Error() {
		t.Fatalf("非法分页文案错误 got=%q", message)
	}
}

// socialQueryCapture 记录请求内真实执行的 SQL 语句数量
type socialQueryCapture struct {
	counts []int64
}

func (q *socialQueryCapture) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(dbpkg.WithQueryCounter(c.Request.Context()))
		c.Next()
		q.counts = append(q.counts, dbpkg.QueryCount(c.Request.Context()))
	}
}

// socialQueryBudget 断言目标请求的语句数量落在预算内并返回实际值
func socialQueryBudget(t *testing.T, capture *socialQueryCapture, before int, budget int64) int64 {
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

// 测试目标：验证社交读取端点的真实 SQL 语句数量不随列表长度增长
// 预期效果：评论列表与关注者列表各最多两条语句，点赞状态最多四条且无逐条查询
func TestSocialReadEndpointsQueryBudget(t *testing.T) {
	capture := &socialQueryCapture{}
	engine, gdb, _ := newSocialHTTPEngine(t, capture.middleware())
	if err := dbpkg.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}

	author := seedUser(t, gdb, "budget-social-author")
	published := seedPublishedVideo(t, gdb, author.ID)
	commenters := []uint{author.ID}
	for index := 0; index < 5; index++ {
		commenters = append(commenters, seedUser(t, gdb, fmt.Sprintf("budget-commenter-%d", index)).ID)
	}
	seedCommentRows(t, gdb, published.ID, commenters)

	followers := []*user.User{}
	for index := 0; index < 4; index++ {
		account := seedUser(t, gdb, fmt.Sprintf("budget-follower-%d", index))
		followers = append(followers, account)
		if err := gdb.Exec("INSERT INTO user_follows (follower_id, followee_id, created_at) VALUES (?, ?, ?)",
			account.ID, author.ID, time.Now()).Error; err != nil {
			t.Fatalf("写入关注关系失败: %v", err)
		}
	}
	viewerToken := socialToken(t, gdb, followers[0])

	capture.counts = nil
	comments := socialJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/video/%d/comments", published.ID), "", nil)
	if len(socialItems(t, comments)) != len(commenters) {
		t.Fatalf("评论列表条数错误 body=%s", comments.Body.String())
	}
	t.Logf("评论列表语句数量=%d", socialQueryBudget(t, capture, 0, 2))
	followerList := socialJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/user/%d/followers", author.ID), "", nil)
	if len(socialItems(t, followerList)) != len(followers) {
		t.Fatalf("关注者列表条数错误 body=%s", followerList.Body.String())
	}
	t.Logf("关注者列表语句数量=%d", socialQueryBudget(t, capture, 1, 2))

	likeState := socialJSONRequest(t, engine, http.MethodGet,
		fmt.Sprintf("/api/video/auth/%d/like", published.ID), viewerToken, nil)
	if likeState.Code != http.StatusOK {
		t.Fatalf("点赞状态查询错误 got=%d body=%s", likeState.Code, likeState.Body.String())
	}
	// 认证请求额外包含一次 auth_sessions 会话校验语句
	t.Logf("点赞状态语句数量=%d", socialQueryBudget(t, capture, 2, 5))

	created := socialJSONRequest(t, engine, http.MethodPost,
		fmt.Sprintf("/api/video/auth/%d/comments", published.ID), viewerToken, map[string]any{"content": "预算评论"})
	if created.Code != http.StatusCreated {
		t.Fatalf("创建评论状态错误 got=%d body=%s", created.Code, created.Body.String())
	}
	t.Logf("创建评论语句数量=%d", socialQueryBudget(t, capture, 3, 5))
}
