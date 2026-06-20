package wizard

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/x6nux/corelink/internal/nodecore/jointoken"
)

func TestPreviewTokenRendersSummary(t *testing.T) {
	tok, _ := jointoken.Encode(jointoken.JoinToken{
		V: 1, H: "ctl.example.com", K: "k",
		C: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	})
	s, err := PreviewToken(tok)
	if err != nil {
		t.Fatalf("PreviewToken: %v", err)
	}
	if !strings.Contains(s, "ctl.example.com") {
		t.Fatalf("预览应含 controller host: %q", s)
	}
	if !strings.Contains(s, "sha256:") {
		t.Fatalf("预览应含 CA 哈希: %q", s)
	}
}

func TestPreviewTokenRejectsBad(t *testing.T) {
	if _, err := PreviewToken("garbage"); err == nil {
		t.Fatalf("非法 token 应报错")
	}
}

func TestNodeWizardStepsFirstStepIsToken(t *testing.T) {
	steps := NodeWizardSteps()
	if len(steps[0].Fields) != 1 || steps[0].Fields[0].Key != "join_token" {
		t.Fatalf("第一步应为单 join_token 字段: %+v", steps[0].Fields)
	}
	if steps[0].Fields[0].CharLimit != 512 {
		t.Fatalf("token 框 CharLimit 应为 512")
	}
	for _, s := range steps {
		for _, f := range s.Fields {
			if f.Key == "server_fingerprint" {
				t.Fatalf("不应再有 server_fingerprint 字段")
			}
		}
	}
}

func TestNodeConfigJSONFromToken(t *testing.T) {
	tok, _ := jointoken.Encode(jointoken.JoinToken{
		V: 1, H: "h.example.com", K: "ek",
		C: "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	})
	data, err := NodeConfigJSON(map[string]string{
		"join_token": tok, "role": "node", "hostname": "n1",
	})
	if err != nil {
		t.Fatalf("NodeConfigJSON: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if m["controller_enroll_addr"] != "h.example.com:7443" {
		t.Fatalf("enroll_addr=%v", m["controller_enroll_addr"])
	}
	if m["controller_ca_hash"] != "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" || m["enrollment_key"] != "ek" {
		t.Fatalf("ca_hash/key 错误: %v", m)
	}
}

func TestTokenToConfigJSONIPv6UsesJoinHostPort(t *testing.T) {
	jt := jointoken.JoinToken{V: 1, H: "fd00::1", K: "mykey", C: "sha256:abcd"}
	data, err := TokenToConfigJSON(jt, "node", "node1")
	if err != nil {
		t.Fatalf("TokenToConfigJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["controller_enroll_addr"] != "[fd00::1]:7443" {
		t.Fatalf("enroll_addr=%v, want [fd00::1]:7443", m["controller_enroll_addr"])
	}
	if m["controller_mtls_addr"] != "[fd00::1]:7444" {
		t.Fatalf("mtls_addr=%v", m["controller_mtls_addr"])
	}
	if m["controller_http_addr"] != "[fd00::1]:8080" {
		t.Fatalf("http_addr=%v", m["controller_http_addr"])
	}
	if m["enrollment_key"] != "mykey" || m["controller_ca_hash"] != "sha256:abcd" {
		t.Fatalf("key/ca_hash 错误: %v", m)
	}
	if m["role"] != "node" || m["hostname"] != "node1" {
		t.Fatalf("role/hostname 错误: %v", m)
	}
}

func TestTokenToConfigJSONIPv4(t *testing.T) {
	jt := jointoken.JoinToken{V: 1, H: "1.2.3.4", K: "k", C: "sha256:ab"}
	data, _ := TokenToConfigJSON(jt, "node", "")
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if m["controller_enroll_addr"] != "1.2.3.4:7443" {
		t.Fatalf("enroll_addr=%v", m["controller_enroll_addr"])
	}
	// hostname 空时回落 os.Hostname()，非空即可。
	if m["hostname"] == "" {
		t.Fatalf("hostname 不应为空")
	}
}
