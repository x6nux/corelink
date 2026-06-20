package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 测试 handleLogin 请求体边界情况。

func TestHandleLoginEmptyBody(t *testing.T) {
	// 空请求体（body 长度 0，非 nil）应返回 400。
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(""))
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 body 状态码 = %d, 期望 400", rec.Code)
	}
}

func TestHandleLoginOversizeBody(t *testing.T) {
	// 超大请求体应被 MaxBytesReader 限制，返回 400。
	h := newHarness(t)
	// 构造超过 64KB 的 JSON
	big := `{"user":"admin","password":"` + strings.Repeat("x", 100*1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(big))
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大 body 状态码 = %d, 期望 400", rec.Code)
	}
}

func TestHandleLoginResponseContainsUser(t *testing.T) {
	// 登录成功时 response 应包含 user 字段。
	h := newHarness(t)
	rec := h.doToken(http.MethodPost, "/admin/api/login",
		loginRequest{User: "admin", Password: "s3cret"}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rec.Code)
	}
	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if resp.User != "admin" {
		t.Errorf("User = %q, 期望 admin", resp.User)
	}
	if resp.Token == "" {
		t.Error("Token 不应为空")
	}
}
