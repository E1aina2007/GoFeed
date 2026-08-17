package router

import (
	"fmt"
	"net/http"
	"testing"
)

// 测试目标：提交刷新令牌并读取轮换后的会话信息
// 预期效果：按指定状态返回新的凭据或空结果
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

// 测试目标：验证刷新令牌轮换不会使同一会话的访问令牌提前失效
// 预期效果：旧刷新令牌不能重放，新刷新令牌可继续轮换，轮换前后访问令牌均可使用
func TestSessionRefreshRotation(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "refresh_user", "refresh-password-123")
	sess := login(t, client, base, "refresh_user", "refresh-password-123")

	refreshed := refreshSession(t, client, base, sess.RefreshToken, http.StatusOK)
	if refreshed.RefreshToken == "" || refreshed.RefreshToken == sess.RefreshToken {
		t.Fatal("refresh 应返回新的 refresh token")
	}

	// 新访问令牌可用
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", refreshed.AccessToken, nil, http.StatusOK, nil)
	// 旧刷新令牌重放返回未认证状态
	refreshSession(t, client, base, sess.RefreshToken, http.StatusUnauthorized)
	// 旧访问令牌仍有效，预期刷新仅轮换刷新令牌且会话标识不变
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusOK, nil)
	// 新刷新令牌可继续轮换
	refreshSession(t, client, base, refreshed.RefreshToken, http.StatusOK)
}

// 测试目标：验证退出登录仅撤销当前会话而不会影响同用户其他会话
// 预期效果：已退出会话不可访问，另一会话保持可用，重复退出被拒绝
func TestSessionLogoutIsolation(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "logout_user", "logout-password-123")
	a := login(t, client, base, "logout_user", "logout-password-123")
	b := login(t, client, base, "logout_user", "logout-password-123")

	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", a.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", a.AccessToken, nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusOK, nil)

	// 已撤销会话再次退出，预期返回未认证状态
	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", a.AccessToken, nil, http.StatusUnauthorized, nil)

	doJSON(t, client, http.MethodPost, base+"/api/user/auth/logout", b.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusUnauthorized, nil)
}

// 测试目标：验证修改密码会撤销该用户的全部现有会话
// 预期效果：两个访问令牌均失效，旧密码不能登录而新密码可以登录
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

	// 修改密码后两个会话的访问令牌全部失效
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", a.AccessToken, nil, http.StatusUnauthorized, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", b.AccessToken, nil, http.StatusUnauthorized, nil)

	// 旧密码登录返回未认证状态，新密码登录成功
	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "pw_user",
		"password": "old-password-123",
	}, http.StatusUnauthorized, nil)
	login(t, client, base, "pw_user", "new-password-456")
}

// 测试目标：验证注销账号会撤销会话并禁止后续身份访问
// 预期效果：原访问令牌和密码登录均失效，公开读取已删除用户返回未找到状态
func TestSessionDeleteUserRevokesAll(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	register(t, client, base, "del_user", "del-password-123")
	sess := login(t, client, base, "del_user", "del-password-123")

	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusUnauthorized, nil)

	// 公开读取已删除用户，预期仓储过滤软删除记录后返回未找到状态
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/user/%d", base, sess.UserID), "", nil, http.StatusNotFound, nil)

	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "del_user",
		"password": "del-password-123",
	}, http.StatusUnauthorized, nil)
}

// 测试目标：验证用户认证接口对重复数据、非法输入和冲突操作的边界处理
// 预期效果：各场景返回冲突、请求无效或禁止状态，软删除用户名仍不可重新注册
func TestUserAuthBoundaries(t *testing.T) {
	srv, client := newTestServer(t)
	base := srv.URL

	// 重复注册，预期返回冲突状态
	register(t, client, base, "dup_user", "dup-password-123")
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "dup_user",
		"password": "dup-password-123",
	}, http.StatusConflict, nil)

	// 用户名或密码长度不足，预期返回请求无效状态
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "ab",
		"password": "short",
	}, http.StatusBadRequest, nil)

	// 使用错误密码登录，预期返回未认证状态
	doJSON(t, client, http.MethodPost, base+"/api/user/login", "", map[string]string{
		"username": "dup_user",
		"password": "wrong-password-123",
	}, http.StatusUnauthorized, nil)

	// 改名碰撞已有用户名，预期返回冲突状态
	register(t, client, base, "another_user", "another-password-123")
	sess := login(t, client, base, "dup_user", "dup-password-123")
	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/name", sess.AccessToken, map[string]string{
		"new_username": "another_user",
	}, http.StatusConflict, nil)

	// 提交错误旧密码，预期返回禁止状态
	doJSON(t, client, http.MethodPatch, base+"/api/user/auth/password", sess.AccessToken, map[string]string{
		"old_password": "wrong-password-123",
		"new_password": "new-password-456",
	}, http.StatusForbidden, nil)

	// 注销后原用户名仍被唯一约束占用，预期不能重新注册
	doJSON(t, client, http.MethodDelete, base+"/api/user/auth", sess.AccessToken, nil, http.StatusNoContent, nil)
	doJSON(t, client, http.MethodPost, base+"/api/user/register", "", map[string]string{
		"username": "dup_user",
		"password": "dup-password-123",
	}, http.StatusConflict, nil)
}
