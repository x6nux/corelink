package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/x6nux/corelink/internal/nodecore/jointoken"
)

// validCAHash 是合法的 CA SPKI 哈希（sha256: + 64 hex），供 jointoken.Decode 通过。
const validCAHash = "sha256:" +
	"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func TestTokenConfigFnWritesConfig(t *testing.T) {
	tok, err := jointoken.Encode(jointoken.JoinToken{V: 1, H: "c.example.com", K: "ek123", C: validCAHash})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "node.json")
	fn := tokenConfigFn(tok)
	if err := fn([]string{"--output", out}); err != nil {
		t.Fatalf("tokenConfigFn: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["controller_enroll_addr"] != "c.example.com:7443" {
		t.Fatalf("enroll_addr=%v", m["controller_enroll_addr"])
	}
	if m["controller_ca_hash"] != validCAHash || m["enrollment_key"] != "ek123" {
		t.Fatalf("ca_hash/key 错误: %v", m)
	}
}

func TestTokenConfigFnRejectsBadToken(t *testing.T) {
	fn := tokenConfigFn("!!!not-base64!!!")
	if err := fn([]string{"--output", filepath.Join(t.TempDir(), "x.json")}); err == nil {
		t.Fatalf("非法 token 应报错")
	}
}
