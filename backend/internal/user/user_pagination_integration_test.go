package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gofeed/internal/testutil"

	"github.com/gin-gonic/gin"
)

// 测试目标：验证无参数全量读取与显式 keyset 分页可以兼容共存
// 预期效果：无参数没有 next_cursor，分页按 ID 升序并可用游标续页
func TestUserListPaginationCompatibility(t *testing.T) {
	db := testutil.DB(t)
	repo := NewRepository(db)
	for _, username := range []string{"page-alice", "page-bob", "page-cora"} {
		if err := repo.Create(t.Context(), &User{Username: username, Password: "test-hash"}); err != nil {
			t.Fatalf("创建用户 %s: %v", username, err)
		}
	}

	engine := gin.New()
	controller := NewController(NewService(repo, nil), nil)
	engine.GET("/api/user", controller.GetUserList)

	legacy := httptest.NewRecorder()
	engine.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/api/user", nil))
	if legacy.Code != http.StatusOK {
		t.Fatalf("无参数列表状态错误 got=%d", legacy.Code)
	}
	var legacyBody userListHTTPResponse
	if err := json.Unmarshal(legacy.Body.Bytes(), &legacyBody); err != nil {
		t.Fatalf("解析无参数列表响应: %v", err)
	}
	if len(legacyBody.Users) != 3 || legacyBody.NextCursor != "" {
		t.Fatalf("无参数读取不应分页 got=%+v", legacyBody)
	}

	first := httptest.NewRecorder()
	engine.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/user?limit=2", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("分页首页状态错误 got=%d", first.Code)
	}
	var firstBody userListHTTPResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("解析分页首页响应: %v", err)
	}
	if len(firstBody.Users) != 2 || firstBody.NextCursor == "" || firstBody.Users[0].ID >= firstBody.Users[1].ID {
		t.Fatalf("分页首页内容错误 got=%+v", firstBody)
	}

	second := httptest.NewRecorder()
	engine.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/user?cursor="+firstBody.NextCursor, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("分页续页状态错误 got=%d", second.Code)
	}
	var secondBody userListHTTPResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("解析分页续页响应: %v", err)
	}
	if len(secondBody.Users) != 1 || secondBody.NextCursor != "" || secondBody.Users[0].ID <= firstBody.Users[1].ID {
		t.Fatalf("分页续页内容错误 got=%+v", secondBody)
	}
}

// 测试目标：验证用户列表拒绝无效分页大小和不透明游标
// 预期效果：两类参数错误均返回 400，不执行全量读取回退
func TestUserListPaginationRejectsInvalidParameters(t *testing.T) {
	db := testutil.DB(t)
	engine := gin.New()
	controller := NewController(NewService(NewRepository(db), nil), nil)
	engine.GET("/api/user", controller.GetUserList)

	for _, target := range []string{"/api/user?limit=0", "/api/user?limit=51", "/api/user?cursor=invalid"} {
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("参数错误状态不正确 got=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
