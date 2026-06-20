package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func newTestAuth(t *testing.T) *Authenticator {
	t.Helper()
	a, err := NewAuthenticatorFromPassword("admin", "s3cret", []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticatorFromPassword: %v", err)
	}
	return a
}

func TestLoginSuccess(t *testing.T) {
	a := newTestAuth(t)
	tok, err := a.Login("admin", "s3cret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	user, err := a.VerifyToken(tok)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if user != "admin" {
		t.Fatalf("user = %q, want admin", user)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	a := newTestAuth(t)
	if _, err := a.Login("admin", "wrong"); err == nil {
		t.Fatal("want error for wrong password")
	}
}

func TestLoginUnknownUser(t *testing.T) {
	a := newTestAuth(t)
	if _, err := a.Login("nobody", "s3cret"); err == nil {
		t.Fatal("want error for unknown user")
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	a := newTestAuth(t)
	// 用过去的过期时刻签发。
	tok := a.signToken("admin", time.Now().Add(-time.Minute))
	if _, err := a.VerifyToken(tok); err == nil {
		t.Fatal("want error for expired token")
	}
}

func TestVerifyTokenTamperedSignature(t *testing.T) {
	a := newTestAuth(t)
	tok, err := a.Login("admin", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		t.Fatalf("token format: %q", tok)
	}
	// 篡改签名段。
	tampered := parts[0] + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := a.VerifyToken(tampered); err == nil {
		t.Fatal("want error for tampered signature")
	}
}

func TestVerifyTokenTamperedPayload(t *testing.T) {
	a := newTestAuth(t)
	tok, err := a.Login("admin", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	// 替换 payload 为另一个 user，签名不再匹配。
	forged := a.signToken("attacker", time.Now().Add(time.Hour))
	forgedPayload := strings.Split(forged, ".")[0]
	mixed := forgedPayload + "." + parts[1]
	if _, err := a.VerifyToken(mixed); err == nil {
		t.Fatal("want error: payload swapped but old signature")
	}
}

func TestVerifyTokenGarbage(t *testing.T) {
	a := newTestAuth(t)
	for _, bad := range []string{"", "abc", "a.b.c", "no-dot", "!!!.@@@"} {
		if _, err := a.VerifyToken(bad); err == nil {
			t.Fatalf("want error for garbage token %q", bad)
		}
	}
}

func TestRequireAuthRejectsMissing(t *testing.T) {
	a := newTestAuth(t)
	called := false
	h := a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/nodes", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("next handler should not be called")
	}
}

func TestRequireAuthRejectsBadToken(t *testing.T) {
	a := newTestAuth(t)
	h := a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/nodes", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAuthAllowsValid(t *testing.T) {
	a := newTestAuth(t)
	tok, _ := a.Login("admin", "s3cret")
	var gotUser string
	h := a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotUser != "admin" {
		t.Fatalf("ctx user = %q, want admin", gotUser)
	}
}

func TestNewAuthenticatorFromHash(t *testing.T) {
	// 用已有 bcrypt 哈希构造（模拟配置注入 AdminPassHash）。
	base := newTestAuth(t)
	hash := base.PasswordHash()
	a, err := NewAuthenticator("admin", hash, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	if _, err := a.Login("admin", "s3cret"); err != nil {
		t.Fatalf("Login with injected hash: %v", err)
	}
}

func TestNewAuthenticatorEmptyKey(t *testing.T) {
	if _, err := NewAuthenticatorFromPassword("admin", "s3cret", nil, time.Hour); err == nil {
		t.Fatal("want error for empty token key")
	}
}

func TestNewAuthenticatorRejectsInvalidHash(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	// 长度 1-7 的非法 hash（模拟用户在 AdminPassHash 里填了垃圾值），
	// 旧实现仅校验非空，会放过它，导致后续 PasswordHash()[:8] 越界 panic。
	for _, bad := range []string{"x", "abc", "1234567"} {
		if _, err := NewAuthenticator("admin", []byte(bad), key, time.Hour); err == nil {
			t.Fatalf("NewAuthenticator 应拒绝非法 bcrypt hash %q，但返回了 nil error", bad)
		}
	}
	// 合法 bcrypt hash 仍须被接受。
	good, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("生成测试 hash 失败: %v", err)
	}
	if _, err := NewAuthenticator("admin", good, key, time.Hour); err != nil {
		t.Fatalf("NewAuthenticator 应接受合法 bcrypt hash，但返回错误: %v", err)
	}
}

func TestPassHashPrefix(t *testing.T) {
	// 极短 hash：朴素的 passHash[:8] 会越界 panic，PassHashPrefix 必须安全。
	short := &Authenticator{passHash: []byte{0xab, 0xcd}}
	if got := short.PassHashPrefix(8); got != "abcd" {
		t.Fatalf("短 hash 前缀 = %q，want %q", got, "abcd")
	}
	// 空 hash 不应 panic，返回空串。
	empty := &Authenticator{passHash: nil}
	if got := empty.PassHashPrefix(8); got != "" {
		t.Fatalf("空 hash 前缀 = %q，want 空串", got)
	}
	// 正常 bcrypt hash：取前 8 字节 = 16 个 hex 字符。
	good, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("生成测试 hash 失败: %v", err)
	}
	a := &Authenticator{passHash: good}
	if got := a.PassHashPrefix(8); len(got) != 16 {
		t.Fatalf("正常 hash 前 8 字节应为 16 hex 字符，got %d 个: %q", len(got), got)
	}
}
