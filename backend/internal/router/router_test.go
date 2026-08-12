package router

import "testing"

func TestUserRouteGroups(t *testing.T) {
	routes := New(nil, false, Options{}).Routes()
	actual := make(map[string]bool, len(routes))
	for _, route := range routes {
		actual[route.Method+" "+route.Path] = true
	}

	expected := []string{
		"POST /api/user/register",
		"POST /api/user/login",
		"POST /api/user/refresh",
		"GET /api/user",
		"GET /api/user/:id",
		"GET /api/user/:id/profile",
		"POST /api/user/auth/logout",
		"PATCH /api/user/auth/name",
		"PATCH /api/user/auth/password",
		"PATCH /api/user/auth/profile",
		"DELETE /api/user/auth",
		"GET /api/video",
		"GET /api/video/:id",
		"POST /api/video/auth/upload/video",
		"POST /api/video/auth/upload/cover",
		"POST /api/video/auth/publish",
		"GET /api/video/auth/mine",
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
