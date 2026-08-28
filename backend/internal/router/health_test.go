package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"gofeed/internal/observability"
)

// 测试目标：验证存活检查不依赖数据库连接
// 预期效果：进程仍在运行时始终返回 200 存活状态
func TestLivenessRoute(t *testing.T) {
	engine := New(nil, false, Options{
		ReadinessCheck: func(context.Context) error {
			return errors.New("database should not be called")
		},
	})
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("存活检查状态错误 got=%d", response.Code)
	}
	var body struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("存活响应解析失败: %v", err)
	}
	if body.Name != "GoFeed" || body.Status != "ok" {
		t.Fatalf("存活响应内容错误 body=%+v", body)
	}
}

// 测试目标：验证就绪检查使用注入的依赖检查器并回传请求 ID
// 预期效果：测试和部署探针可以区分依赖已就绪与未就绪
func TestReadinessRoute(t *testing.T) {
	called := false
	engine := New(nil, false, Options{
		ReadinessCheck: func(ctx context.Context) error {
			called = true
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("就绪检查应使用带超时上下文")
			}
			return nil
		},
	})
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	request.Header.Set(observability.RequestIDHeader, "health-probe-1")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if !called {
		t.Fatal("就绪检查未被调用")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("就绪检查状态错误 got=%d", response.Code)
	}
	if response.Header().Get(observability.RequestIDHeader) != "health-probe-1" {
		t.Fatalf("就绪响应未回传请求 ID got=%q", response.Header().Get(observability.RequestIDHeader))
	}
}

// 测试目标：验证数据库故障会让就绪路由返回服务不可用
// 预期效果：编排系统可停止向未准备完成的 API 转发流量
func TestReadinessRouteRejectsUnavailableDependency(t *testing.T) {
	engine := New(nil, false, Options{
		ReadinessCheck: func(context.Context) error {
			return errors.New("database unavailable")
		},
	})
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("不可用依赖状态错误 got=%d", response.Code)
	}
	var body struct {
		Status       string `json:"status"`
		Dependencies struct {
			Database string `json:"database"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("不可用就绪响应解析失败: %v", err)
	}
	if body.Status != "not_ready" || body.Dependencies.Database != "unavailable" {
		t.Fatalf("不可用就绪响应内容错误 body=%+v", body)
	}
}
