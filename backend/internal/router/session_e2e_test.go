package router

import (
	"fmt"
	"net/http"
	"testing"
)

// refreshSession 提交 refresh token 并返回轮换后的会话信息
func refreshSession(t *testing.T, client *http.Client, base, refreshToken string, wantStatus int) authSession {
	t.Helper()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	if wantStatus == http.StatusOK {
		doJSON(t, client, http.MethodPost, base+"/api/user/refresh", "", map[string]string{
			"refresh_token": refreshToken,
		}, wantStatus, &out)
	} else {
		doJSON(t, client, http.MethodPost, base+"/api/user/refresh", "", map[string]string{
			"refresh_token": refreshToken,
		}, wantStatus, nil)
	}
	return authSession{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		UserID:       out.User.ID,
		Username:     out.User.Username,
	}
}

func TestSessionRefreshRotation(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "refresh_user", "refresh-password-123")
	sess := login(t, client, base, "refresh_user", "refresh-password-123")

	refreshed := refreshSession(t, client, base, sess.RefreshToken, http.StatusOK)
	if refreshed.RefreshToken == "" || refreshed.RefreshToken == sess.RefreshToken {
		t.Fatal("refresh 应返回新的 refresh token")
	}

	// 新 access 可用
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", refreshed.AccessToken, nil, http.StatusOK, nil)
	// 旧 refresh 重放 401
	refreshSession(t, client, base, sess.RefreshToken, http.StatusUnauthorized)
	// 旧 access 仍有效：refresh 只轮换 refresh token，会话 ID 不变
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusOK, nil)
	// 新 refresh 可继续轮换
	refreshSession(t, client, base, refreshed.RefreshToken, http.StatusOK)
}

func TestSessionLogoutIsolation(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "logout_user", "logout-password-123")
	a := login(t, client, base, "logout_user", "logout-password-123")
	b := login(t, client, base, "logout_user", "logout-password-123")

	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", a.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", a.AccessToken, nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusOK, nil)

	// 已撤销会话再次退出
	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", a.AccessToken, nil, http.StatusUnauthorized, nil)

	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", b.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusUnauthorized, nil)
}

func TestSessionPasswordChangeRevokesAll(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "pw_user", "old-password-123")
	a := login(t, client, base, "pw_user", "old-password-123")
	b := login(t, client, base, "pw_user", "old-password-123")

	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/password", b.AccessToken, map[string]string{
		"old_password": "old-password-123",
		"new_password": "new-password-456",
	}, http.StatusOK, nil)

	// 改密后两个会话的 access 全部失效
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", a.AccessToken, nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusUnauthorized, nil)

	// 旧密码登录 401，新密码登录成功
	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "pw_user",
		"password": "old-password-123",
	}, http.StatusUnauthorized, nil)
	login(t, client, base, "pw_user", "new-password-456")
}

func TestSessionDeleteUserRevokesAll(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "del_user", "del-password-123")
	sess := login(t, client, base, "del_user", "del-password-123")

	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusUnauthorized, nil)

	// 公开读取已删除用户返回 404（仓储查询先过滤软删行）
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d", base, sess.UserID), "", nil, http.StatusNotFound, nil)

	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "del_user",
		"password": "del-password-123",
	}, http.StatusUnauthorized, nil)
}

func TestUserAuthBoundaries(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	// 重复注册 409
	register(t, client, base, "dup_user", "dup-password-123")
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "dup_user",
		"password": "dup-password-123",
	}, http.StatusConflict, nil)

	// 用户名/密码长度不足 400
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "ab",
		"password": "short",
	}, http.StatusBadRequest, nil)

	// 错误密码登录 401
	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "dup_user",
		"password": "wrong-password-123",
	}, http.StatusUnauthorized, nil)

	// 改名撞已有用户名 409
	register(t, client, base, "another_user", "another-password-123")
	sess := login(t, client, base, "dup_user", "dup-password-123")
	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/name", sess.AccessToken, map[string]string{
		"new_username": "another_user",
	}, http.StatusConflict, nil)

	// 错误旧密码 403
	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/password", sess.AccessToken, map[string]string{
		"old_password": "wrong-password-123",
		"new_password": "new-password-456",
	}, http.StatusForbidden, nil)

	// 删号后原用户名仍被唯一索引占用，不能重新注册
	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "dup_user",
		"password": "dup-password-123",
	}, http.StatusConflict, nil)
}
