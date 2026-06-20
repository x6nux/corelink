package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 测试 spaHandler 对不同路径的处理行为。

func TestSpaHandler_IndexHTMLContent(t *testing.T) {
	// 验证 fallback 到 index.html 时返回 HTML 内容。
	handler := spaHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/settings", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "<!doctype html>") && !strings.Contains(strings.ToLower(body), "<html") {
		t.Error("fallback 应返回 HTML 内容")
	}
}

func TestSpaHandler_RootPath(t *testing.T) {
	// 根路径 / 也应返回 index.html。
	handler := spaHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ 状态码 = %d, 期望 200", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("/ 返回空 body")
	}
}

func TestSpaHandler_DeepNestedPath(t *testing.T) {
	// 深层嵌套路径应 fallback 到 index.html。
	handler := spaHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/a/b/c/d/e", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("深层路径 状态码 = %d, 期望 200", rec.Code)
	}
}
