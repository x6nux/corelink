package ingress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMyIPHandlerReturnsRemoteIP(t *testing.T) {
	h := NewMyIPHandler()

	req := httptest.NewRequest(http.MethodGet, "/v1/myip", nil)
	req.RemoteAddr = "198.51.100.42:60123"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if got := string(body); got != "198.51.100.42" {
		t.Fatalf("body = %q, want 198.51.100.42", got)
	}
}

func TestMyIPHandlerRejectsNonGet(t *testing.T) {
	h := NewMyIPHandler()

	req := httptest.NewRequest(http.MethodPost, "/v1/myip", nil)
	req.RemoteAddr = "198.51.100.42:60123"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestMyIPHandlerRemoteAddrWithoutPort covers a RemoteAddr that has no port
// (SplitHostPort fails): the handler falls back to the raw RemoteAddr.
func TestMyIPHandlerRemoteAddrWithoutPort(t *testing.T) {
	h := NewMyIPHandler()

	req := httptest.NewRequest(http.MethodGet, "/v1/myip", nil)
	req.RemoteAddr = "203.0.113.9"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)
	if got := string(body); got != "203.0.113.9" {
		t.Fatalf("body = %q, want 203.0.113.9", got)
	}
}
