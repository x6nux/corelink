package tunnel

import (
	"testing"
)

// TestProtocol常量 验证协议常量值与预期一致。
func TestProtocol常量(t *testing.T) {
	tests := []struct {
		proto Protocol
		want  string
	}{
		{TCP, "tcp"},
		{WS, "ws"},
		{TLS, "tls"},
		{WSS, "wss"},
		{GRPC, "grpc"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.proto) != tt.want {
				t.Errorf("Protocol %q != %q", tt.proto, tt.want)
			}
		})
	}
}

// TestConfig_零值可用 验证零值 Config 不触发 panic。
func TestConfig_零值可用(t *testing.T) {
	cfg := Config{}
	// 零值的 Protocol、TLS、Proxy 应为零值/nil
	if cfg.Protocol != "" {
		t.Errorf("零值 Protocol = %q, 期望空", cfg.Protocol)
	}
	if cfg.TLS != nil {
		t.Error("零值 TLS 应为 nil")
	}
	if cfg.Proxy != nil {
		t.Error("零值 Proxy 应为 nil")
	}
}

// TestProxyOptions_字段 验证 ProxyOptions 各字段可设置。
func TestProxyOptions_字段(t *testing.T) {
	p := ProxyOptions{
		URL:             "socks5://127.0.0.1:1080",
		SingBoxOutbound: `{"type":"direct"}`,
		SingBoxBinary:   "/usr/bin/sing-box",
	}
	if p.URL != "socks5://127.0.0.1:1080" {
		t.Errorf("URL = %q", p.URL)
	}
	if p.SingBoxOutbound == "" {
		t.Error("SingBoxOutbound 不应为空")
	}
	if p.SingBoxBinary != "/usr/bin/sing-box" {
		t.Errorf("SingBoxBinary = %q", p.SingBoxBinary)
	}
}

// TestTLSOptions_字段 验证 TLSOptions 各字段可设置。
func TestTLSOptions_字段(t *testing.T) {
	o := TLSOptions{
		Mode:         TLSModePinned,
		ServerName:   "example.com",
		PinnedCAHash: "sha256:abc123",
		ACMEDomains:  []string{"a.com", "b.com"},
		ACMECacheDir: "/tmp/acme",
	}
	if o.Mode != TLSModePinned {
		t.Errorf("Mode = %q", o.Mode)
	}
	if o.ServerName != "example.com" {
		t.Errorf("ServerName = %q", o.ServerName)
	}
	if len(o.ACMEDomains) != 2 {
		t.Errorf("ACMEDomains 长度 = %d", len(o.ACMEDomains))
	}
}
