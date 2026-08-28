package router

import "testing"

// 测试目标：验证路由装配包含全部用户、视频和互动接口且不保留旧路径
// 预期效果：每个对外接口均已注册，废弃的复数用户路径不存在
func TestUserRouteGroups(t *testing.T) {
	routes := New(nil, false, Options{}).Routes()
	// 测试目标：收集已注册路由
	// 预期效果：用于逐项判断必需接口是否存在
	actual := make(map[string]bool, len(routes))
	for _, route := range routes {
		actual[route.Method+" "+route.Path] = true
	}

	// 测试目标：列出产品对外提供的路由
	// 预期效果：全部出现在已注册路由集合中
	expected := []string{
		"GET /health",
		"GET /ready",
		"POST /api/user/register",
		"POST /api/user/login",
		"POST /api/user/refresh",
		"GET /api/user",
		"GET /api/user/:id",
		"GET /api/user/:id/profile",
		"GET /api/user/:id/followers",
		"GET /api/user/:id/following",
		"POST /api/user/auth/logout",
		"PATCH /api/user/auth/name",
		"PATCH /api/user/auth/password",
		"POST /api/user/auth/avatar",
		"PATCH /api/user/auth/profile",
		"GET /api/user/auth/:id/follow",
		"PUT /api/user/auth/:id/follow",
		"DELETE /api/user/auth/:id/follow",
		"DELETE /api/user/auth",
		"GET /api/video",
		"GET /api/video/:id",
		"GET /api/video/:id/comments",
		"POST /api/video/auth/drafts",
		"GET /api/video/auth/drafts/:id",
		"POST /api/video/auth/drafts/:id/play",
		"POST /api/video/auth/drafts/:id/cover",
		"POST /api/video/auth/drafts/:id/publish",
		"DELETE /api/video/auth/drafts/:id",
		"GET /api/video/auth/mine",
		"GET /api/video/auth/:id/like",
		"PUT /api/video/auth/:id/like",
		"DELETE /api/video/auth/:id/like",
		"POST /api/video/auth/:id/comments",
		"DELETE /api/video/auth/:id/comments/:commentID",
		"DELETE /api/video/auth/:id",
	}
	for _, route := range expected {
		if !actual[route] {
			t.Errorf("missing route %s", route)
		}
	}
	if actual["POST /api/users"] {
		t.Error("legacy /api/users route must not remain registered")
	}
}
