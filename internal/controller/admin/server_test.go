package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 测试 Server 实现 http.Handler 接口与 loginRequest/loginResponse 序列化。

func TestServerImplementsHTTPHandler(t *testing.T) {
	// 验证 *Server 满足 http.Handler 接口。
	h := newHarness(t)
	var handler http.Handler = h.srv
	if handler == nil {
		t.Fatal("Server 应实现 http.Handler 接口")
	}
}

func TestLoginRequestJSONRoundTrip(t *testing.T) {
	// 验证 loginRequest JSON 序列化 -> 反序列化一致性。
	orig := loginRequest{User: "admin", Password: "s3cret!@#"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var decoded loginRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.User != orig.User || decoded.Password != orig.Password {
		t.Errorf("round-trip 失败: got %+v", decoded)
	}
}

func TestLoginResponseJSONTags(t *testing.T) {
	// 验证 loginResponse JSON 键名。
	resp := loginResponse{Token: "tok123", User: "admin"}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["token"] != "tok123" {
		t.Errorf("token = %v", m["token"])
	}
	if m["user"] != "admin" {
		t.Errorf("user = %v", m["user"])
	}
}

func TestNewAdminServerReturnsNonNil(t *testing.T) {
	// 即使只传最小依赖也不 panic。
	h := newHarness(t)
	deps := h.srv.deps
	srv := NewAdminServer(deps)
	if srv == nil {
		t.Fatal("NewAdminServer 不应返回 nil")
	}
}

func TestUnknownAPIPathReturns404OrSPA(t *testing.T) {
	// /admin/api/ 下不存在的路径，由 RequireAuth 拦截（无 token -> 401）。
	h := newHarness(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/nonexistent", nil)
	h.srv.ServeHTTP(rec, req)
	// 未认证时受保护路径应返回 401。
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("不存在的 API 路径状态码 = %d, 期望 401", rec.Code)
	}
}
