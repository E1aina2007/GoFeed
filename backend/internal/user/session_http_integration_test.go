package user

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	authn "gofeed/internal/auth"
	dbpkg "gofeed/internal/db"
	jwtmw "gofeed/internal/middleware/jwt"
	"gofeed/internal/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// userJSONRequest 发送可选 JSON 请求体与可选访问令牌的 HTTP 请求
func userJSONRequest(t *testing.T, engine *gin.Engine, method, path, token string, payload any) *httptest.ResponseRecorder {
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

// userResponseBody 解析响应体为通用映射以便断言字段契约
func userResponseBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v body=%s", err, recorder.Body.String())
	}
	return body
}

// userErrorMessage 断言状态码并读取错误响应的公开文案
func userErrorMessage(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) string {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("状态码错误 got=%d want=%d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	message, ok := userResponseBody(t, recorder)["error"].(string)
	if !ok {
		t.Fatalf("错误响应缺少 error 字段 body=%s", recorder.Body.String())
	}
	return message
}

// newUserHTTPEngine 装配用户模块的注册、登录、会话与账号端点
func newUserHTTPEngine(t *testing.T) (*gin.Engine, *authn.SessionService) {
	t.Helper()
	gdb := testutil.DB(t)
	sessions := authn.NewSessionService(authn.NewSessionRepository(gdb))
	controller := NewController(NewService(NewRepository(gdb), nil), sessions)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/user/register", controller.CreateUser)
	engine.POST("/api/user/login", controller.Login)
	engine.POST("/api/user/refresh", controller.UpdateRefreshToken)
	engine.GET("/api/user/:id", controller.GetUser)

	protected := engine.Group("/api/user/auth", jwtmw.Auth(sessions))
	protected.POST("/logout", controller.UpdateSessionRevocation)
	protected.DELETE("", controller.DeleteUser)
	return engine, sessions
}

// userTokenPair 从登录响应中读取访问令牌与刷新令牌
func userTokenPair(t *testing.T, recorder *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("登录状态错误 got=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := userResponseBody(t, recorder)
	access, accessOK := body["access_token"].(string)
	refresh, refreshOK := body["refresh_token"].(string)
	if !accessOK || !refreshOK || access == "" || refresh == "" {
		t.Fatalf("登录响应缺少令牌 got=%v", body)
	}
	if _, ok := body["expires_at"].(string); !ok {
		t.Fatalf("登录响应缺少 expires_at got=%v", body)
	}
	return access, refresh
}

// 测试目标：验证注册、登录、刷新与登出的完整会话契约
// 预期效果：注册 201、登录 200、刷新轮换令牌、登出 204 后访问令牌与刷新令牌均失效
func TestUserSessionHTTPContract(t *testing.T) {
	engine, _ := newUserHTTPEngine(t)
	credentials := map[string]any{"username": "session-alice", "password": "secret-pass-1"}

	registered := userJSONRequest(t, engine, http.MethodPost, "/api/user/register", "", credentials)
	if registered.Code != http.StatusCreated {
		t.Fatalf("注册状态错误 got=%d body=%s", registered.Code, registered.Body.String())
	}
	registeredBody := userResponseBody(t, registered)
	account, ok := registeredBody["user"].(map[string]any)
	if !ok {
		t.Fatalf("注册响应缺少 user 字段 got=%v", registeredBody)
	}
	if account["username"] != "session-alice" {
		t.Fatalf("注册响应用户名错误 got=%v", account)
	}
	if _, leaked := account["password"]; leaked {
		t.Fatalf("注册响应不应包含密码字段 got=%v", account)
	}
	userID, ok := account["id"].(float64)
	if !ok || userID == 0 {
		t.Fatalf("注册响应缺少用户标识 got=%v", account)
	}

	duplicate := userJSONRequest(t, engine, http.MethodPost, "/api/user/register", "", credentials)
	if message := userErrorMessage(t, duplicate, http.StatusConflict); message != ErrUsernameTaken.Error() {
		t.Fatalf("重名注册文案错误 got=%q", message)
	}

	badPassword := userJSONRequest(t, engine, http.MethodPost, "/api/user/login", "",
		map[string]any{"username": "session-alice", "password": "wrong-pass-999"})
	if message := userErrorMessage(t, badPassword, http.StatusUnauthorized); message != "invalid username or password" {
		t.Fatalf("错误密码文案错误 got=%q", message)
	}

	access, refresh := userTokenPair(t, userJSONRequest(t, engine, http.MethodPost, "/api/user/login", "", credentials))

	rotatedRecorder := userJSONRequest(t, engine, http.MethodPost, "/api/user/refresh", "",
		map[string]any{"refresh_token": refresh})
	rotatedAccess, rotatedRefresh := userTokenPair(t, rotatedRecorder)
	if rotatedRefresh == refresh || rotatedAccess == "" {
		t.Fatalf("刷新应轮换刷新令牌 got=%q", rotatedRefresh)
	}

	reused := userJSONRequest(t, engine, http.MethodPost, "/api/user/refresh", "",
		map[string]any{"refresh_token": refresh})
	if message := userErrorMessage(t, reused, http.StatusUnauthorized); message != "invalid refresh token" {
		t.Fatalf("旧刷新令牌复用文案错误 got=%q", message)
	}

	logout := userJSONRequest(t, engine, http.MethodPost, "/api/user/auth/logout", access, nil)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("登出状态错误 got=%d body=%s", logout.Code, logout.Body.String())
	}
	if logout.Body.Len() != 0 {
		t.Fatalf("登出响应体应为空 got=%s", logout.Body.String())
	}

	repeated := userJSONRequest(t, engine, http.MethodPost, "/api/user/auth/logout", access, nil)
	if message := userErrorMessage(t, repeated, http.StatusUnauthorized); message != "invalid or expired token" {
		t.Fatalf("登出后访问令牌应失效 got=%q", message)
	}
	rotatedReuse := userJSONRequest(t, engine, http.MethodPost, "/api/user/refresh", "",
		map[string]any{"refresh_token": rotatedRefresh})
	if message := userErrorMessage(t, rotatedReuse, http.StatusUnauthorized); message != "invalid refresh token" {
		t.Fatalf("登出后刷新令牌应失效 got=%q", message)
	}
}

// 测试目标：验证账号注销后的可见性与凭据失效语义
// 预期效果：注销返回 204，之后公开详情 404，登录与刷新均被拒绝
func TestUserAccountRevocationHTTPContract(t *testing.T) {
	engine, _ := newUserHTTPEngine(t)
	credentials := map[string]any{"username": "revoke-bob", "password": "secret-pass-2"}

	registered := userJSONRequest(t, engine, http.MethodPost, "/api/user/register", "", credentials)
	if registered.Code != http.StatusCreated {
		t.Fatalf("注册状态错误 got=%d body=%s", registered.Code, registered.Body.String())
	}
	account := userResponseBody(t, registered)["user"].(map[string]any)
	path := "/api/user/" + userIDString(t, account)

	profile := userJSONRequest(t, engine, http.MethodGet, path, "", nil)
	if profile.Code != http.StatusOK {
		t.Fatalf("注销前详情状态错误 got=%d body=%s", profile.Code, profile.Body.String())
	}

	access, refresh := userTokenPair(t, userJSONRequest(t, engine, http.MethodPost, "/api/user/login", "", credentials))

	revoked := userJSONRequest(t, engine, http.MethodDelete, "/api/user/auth", access, nil)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("注销状态错误 got=%d body=%s", revoked.Code, revoked.Body.String())
	}

	missing := userJSONRequest(t, engine, http.MethodGet, path, "", nil)
	if message := userErrorMessage(t, missing, http.StatusNotFound); message != "user not found" {
		t.Fatalf("注销后详情文案错误 got=%q", message)
	}

	loginAgain := userJSONRequest(t, engine, http.MethodPost, "/api/user/login", "", credentials)
	if message := userErrorMessage(t, loginAgain, http.StatusUnauthorized); message != "invalid username or password" {
		t.Fatalf("注销后登录文案错误 got=%q", message)
	}
	refreshAgain := userJSONRequest(t, engine, http.MethodPost, "/api/user/refresh", "",
		map[string]any{"refresh_token": refresh})
	if message := userErrorMessage(t, refreshAgain, http.StatusUnauthorized); message != "invalid refresh token" {
		t.Fatalf("注销后刷新文案错误 got=%q", message)
	}
	revokeAgain := userJSONRequest(t, engine, http.MethodDelete, "/api/user/auth", access, nil)
	if message := userErrorMessage(t, revokeAgain, http.StatusUnauthorized); message != "invalid or expired token" {
		t.Fatalf("注销后访问令牌应失效 got=%q", message)
	}
}

// userIDString 把注册响应中的数字标识转换成路径片段
func userIDString(t *testing.T, account map[string]any) string {
	t.Helper()
	raw, ok := account["id"].(float64)
	if !ok || raw <= 0 {
		t.Fatalf("注册响应缺少用户标识 got=%v", account)
	}
	return strconv.FormatUint(uint64(raw), 10)
}

// sqlVideoCounter 使用真实 SQL 统计作者已发布作品数，替代 video 包避免测试包循环依赖
type sqlVideoCounter struct {
	db *gorm.DB
}

func (c sqlVideoCounter) GetPublishedVideoCountByAuthor(ctx context.Context, authorID uint) (int64, error) {
	var total int64
	err := c.db.WithContext(ctx).Table("videos").
		Where("author_id = ? AND status = ? AND deleted_at IS NULL AND published_at IS NOT NULL", authorID, "published").
		Where("play_url <> '' AND play_file_name <> '' AND play_original_name <> ''").
		Where("cover_url <> '' AND cover_file_name <> '' AND cover_original_name <> ''").
		Count(&total).Error
	return total, err
}

// userQueryCapture 记录请求内真实执行的 SQL 语句数量
type userQueryCapture struct {
	counts []int64
}

func (q *userQueryCapture) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(dbpkg.WithQueryCounter(c.Request.Context()))
		c.Next()
		q.counts = append(q.counts, dbpkg.QueryCount(c.Request.Context()))
	}
}

// userQueryBudget 断言目标请求的语句数量落在预算内
func userQueryBudget(t *testing.T, capture *userQueryCapture, before int, budget int64) int64 {
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

// 测试目标：验证用户读侧接口的真实 SQL 语句数量收敛在预算内
// 预期效果：公开资料最多两条语句并正确计数作品，无参数列表只执行一条语句
func TestUserReadEndpointsQueryBudget(t *testing.T) {
	gdb := testutil.DB(t)
	if err := dbpkg.RegisterQueryCounter(gdb); err != nil {
		t.Fatalf("注册查询计数回调失败: %v", err)
	}
	repo := NewRepository(gdb)
	author := &User{Username: "budget-author", Password: "test-hash"}
	if err := repo.Create(t.Context(), author); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	now := time.Now()
	if err := gdb.Create(&publishedVideoRow{
		AuthorID: author.ID, Title: "预算作品", Status: "published", PublishedAt: &now,
		PlayURL: "/static/videos/1/budget.mp4", PlayFileName: "budget.mp4", PlayOriginalName: "预算视频.mp4",
		CoverURL: "/static/covers/1/budget.webp", CoverFileName: "budget.webp", CoverOriginalName: "预算封面.webp",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}

	capture := &userQueryCapture{}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(capture.middleware())
	controller := NewController(NewService(repo, sqlVideoCounter{db: gdb}), nil)
	engine.GET("/api/user/:id/profile", controller.GetProfile)
	engine.GET("/api/user", controller.GetUserList)

	capture.counts = nil
	profile := userJSONRequest(t, engine, http.MethodGet, fmt.Sprintf("/api/user/%d/profile", author.ID), "", nil)
	if profile.Code != http.StatusOK {
		t.Fatalf("资料状态错误 got=%d body=%s", profile.Code, profile.Body.String())
	}
	var profileBody struct {
		VideoCount int64 `json:"video_count"`
	}
	if err := json.Unmarshal(profile.Body.Bytes(), &profileBody); err != nil {
		t.Fatalf("解析资料响应失败: %v", err)
	}
	if profileBody.VideoCount != 1 {
		t.Fatalf("作品计数错误 got=%d want=1", profileBody.VideoCount)
	}
	afterProfile := len(capture.counts)
	userQueryBudget(t, capture, afterProfile-1, 2)

	list := userJSONRequest(t, engine, http.MethodGet, "/api/user", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("列表状态错误 got=%d body=%s", list.Code, list.Body.String())
	}
	userQueryBudget(t, capture, afterProfile, 1)
}

// publishedVideoRow 是查询预算用例写入 videos 表的最小字段集合
type publishedVideoRow struct {
	ID                uint `gorm:"primaryKey"`
	AuthorID          uint
	Title             string
	Description       string
	Status            string
	PlayURL           string
	PlayFileName      string
	PlayOriginalName  string
	CoverURL          string
	CoverFileName     string
	CoverOriginalName string
	PublishedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (publishedVideoRow) TableName() string { return "videos" }
