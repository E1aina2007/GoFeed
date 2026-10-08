package infraaccount_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"
	infrajwt "gofeed/internal/infra/jwt"
	infraaccount "gofeed/internal/infra/persistence/account"
	interfaceshttpaccount "gofeed/internal/interfaces/http/account"
	jwtmw "gofeed/internal/interfaces/http/auth"
	"gofeed/internal/testutil"
)

// 测试目标：配置用户仓储集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(testutil.Main(m))
}

// 测试目标：指定强制会话更新失败的临时触发器名称
// 预期效果：测试结束时清理该触发器
const failSessionUpdateTrigger = "test_fail_auth_session_update"

// 测试目标：验证撤销会话失败时修改密码会整体回滚
// 预期效果：旧密码和原会话保持有效，新密码不会写入数据库
func TestUpdatePasswordRollsBackWhenSessionRevocationFails(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	repo := infraaccount.NewRepository(db)
	account := createUserWithSession(t, ctx, db, repo, "atomic_password_user", "old-password-123")
	security := applicationaccount.NewAccountSecurity(infraaccount.NewCredentialReader(repo),
		infraaccount.BcryptPasswordVerifier{}, infraaccount.BcryptPasswordHasher{}, infraaccount.NewAccountSecurityWriter(db))

	forceSessionUpdateFailure(t, db)
	if err := security.UpdatePassword(ctx, account.user.ID, "old-password-123", "new-password-456"); err == nil {
		t.Fatal("expected forced session revocation failure")
	}

	stored, err := repo.GetByID(ctx, account.user.ID)
	if err != nil {
		t.Fatalf("GetByID after rollback: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte("old-password-123")); err != nil {
		t.Fatal("old password should remain valid after rollback")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte("new-password-456")); err == nil {
		t.Fatal("new password should not be committed after rollback")
	}
	if err := account.sessions.Validate(ctx, account.sessionID, account.user.ID); err != nil {
		t.Fatalf("session should remain active after rollback: %v", err)
	}
}

// 测试目标：验证撤销会话失败时注销账号会整体回滚
// 预期效果：用户和原会话均保持有效，不会留下部分删除状态
func TestDeleteRollsBackWhenSessionRevocationFails(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	repo := infraaccount.NewRepository(db)
	account := createUserWithSession(t, ctx, db, repo, "atomic_delete_user", "delete-password-123")
	security := applicationaccount.NewAccountSecurity(infraaccount.NewCredentialReader(repo),
		infraaccount.BcryptPasswordVerifier{}, infraaccount.BcryptPasswordHasher{}, infraaccount.NewAccountSecurityWriter(db))

	forceSessionUpdateFailure(t, db)
	if err := security.DeleteUser(ctx, account.user.ID); err == nil {
		t.Fatal("expected forced session revocation failure")
	}

	if _, err := repo.GetByID(ctx, account.user.ID); err != nil {
		t.Fatalf("user should remain active after rollback: %v", err)
	}
	if err := account.sessions.Validate(ctx, account.sessionID, account.user.ID); err != nil {
		t.Fatalf("session should remain active after rollback: %v", err)
	}
}

// 测试目标：汇集测试用户、会话服务和会话标识
// 预期效果：可同时断言事务后的用户与会话状态
type userWithSession struct {
	user      domainaccount.PublicAccount
	sessions  *applicationaccount.SessionLifecycleService
	sessionID string
}

// 测试目标：创建带有效会话的测试用户
// 预期效果：返回可用于事务回滚断言的完整上下文
func createUserWithSession(t *testing.T, ctx context.Context, db *gorm.DB, repo *infraaccount.Repository, username, password string) userWithSession {
	t.Helper()
	registration := applicationaccount.NewRegistration(infraaccount.NewCreator(repo), infraaccount.BcryptPasswordHasher{})
	user, err := registration.CreateUser(ctx, domainaccount.RegistrationInput{Username: username, Password: password})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sessionRepo := infraaccount.NewSessionRepository(db)
	sessions := applicationaccount.NewSessionLifecycle(sessionRepo, sessionRepo,
		infrajwt.RefreshTokenGenerator{}, infrajwt.RefreshTokenHasher{}, infrajwt.AccessTokenIssuer{})
	pair, err := sessions.Create(ctx, user.ID, user.Username)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	claims, err := infrajwt.ParseToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	return userWithSession{user: user, sessions: sessions, sessionID: claims.SessionID}
}

// 测试目标：安装使会话更新失败的临时触发器
// 预期效果：后续撤销操作返回数据库错误
func forceSessionUpdateFailure(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("DROP TRIGGER IF EXISTS " + failSessionUpdateTrigger).Error; err != nil {
		t.Fatalf("drop stale test trigger: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP TRIGGER IF EXISTS " + failSessionUpdateTrigger).Error; err != nil {
			t.Errorf("drop test trigger: %v", err)
		}
	})
	statement := "CREATE TRIGGER " + failSessionUpdateTrigger +
		" BEFORE UPDATE ON auth_sessions FOR EACH ROW " +
		"SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced session update failure'"
	if err := db.Exec(statement).Error; err != nil {
		t.Fatalf("create test trigger: %v", err)
	}
}

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
func newUserHTTPEngine(t *testing.T) (*gin.Engine, *applicationaccount.SessionLifecycleService) {
	t.Helper()
	gdb := testutil.DB(t)
	sessionRepo := infraaccount.NewSessionRepository(gdb)
	sessions := applicationaccount.NewSessionLifecycle(sessionRepo, sessionRepo,
		infrajwt.RefreshTokenGenerator{}, infrajwt.RefreshTokenHasher{}, infrajwt.AccessTokenIssuer{})
	repo := infraaccount.NewRepository(gdb)
	accountHandler := interfaceshttpaccount.New(applicationaccount.New(infraaccount.NewReader(repo), nil))
	registrationHandler := interfaceshttpaccount.NewRegistration(applicationaccount.NewRegistration(
		infraaccount.NewCreator(repo), infraaccount.BcryptPasswordHasher{}))
	sessionHandler := interfaceshttpaccount.NewSessions(applicationaccount.NewSessions(
		infraaccount.NewCredentialReader(repo), infraaccount.NewReader(repo), infraaccount.BcryptPasswordVerifier{},
		sessions, infrajwt.AccessTokenIssuer{}))
	securityHandler := interfaceshttpaccount.NewAccountSecurity(applicationaccount.NewAccountSecurity(
		infraaccount.NewCredentialReader(repo), infraaccount.BcryptPasswordVerifier{}, infraaccount.BcryptPasswordHasher{},
		infraaccount.NewAccountSecurityWriter(gdb)))

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/user/register", registrationHandler.CreateUser)
	engine.POST("/api/user/login", sessionHandler.Login)
	engine.POST("/api/user/refresh", sessionHandler.UpdateRefreshToken)
	engine.GET("/api/user/:id", accountHandler.GetUser)

	protected := engine.Group("/api/user/auth", jwtmw.Auth(sessions))
	protected.POST("/logout", sessionHandler.UpdateSessionRevocation)
	protected.DELETE("", securityHandler.DeleteUser)
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
	if message := userErrorMessage(t, duplicate, http.StatusConflict); message != domainaccount.ErrUsernameTaken.Error() {
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
