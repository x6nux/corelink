package tunnel

import (
	"testing"
)

// TestWrapWithProxy_NilProxy 验证 proxy 为 nil 时直接返回 base dialer。
func TestWrapWithProxy_NilProxy(t *testing.T) {
	base := &tcpDialer{}
	d, err := wrapWithProxy(base, nil)
	if err != nil {
		t.Fatalf("wrapWithProxy(nil): %v", err)
	}
	// 应返回原始 base
	if d != base {
		t.Fatal("proxy 为 nil 时应返回原始 base dialer")
	}
}

// TestWrapWithProxy_EmptyURL 验证 URL 为空且无 SingBoxOutbound 时返回 base dialer。
func TestWrapWithProxy_EmptyURL(t *testing.T) {
	base := &tcpDialer{}
	d, err := wrapWithProxy(base, &ProxyOptions{})
	if err != nil {
		t.Fatalf("wrapWithProxy(empty): %v", err)
	}
	if d != base {
		t.Fatal("URL 为空时应返回原始 base dialer")
	}
}

// TestWrapWithProxy_非法URL 验证非法 URL 返回错误。
func TestWrapWithProxy_非法URL(t *testing.T) {
	base := &tcpDialer{}
	_, err := wrapWithProxy(base, &ProxyOptions{URL: "://bad"})
	if err == nil {
		t.Fatal("非法 URL 应返回错误")
	}
}

// TestWrapWithProxy_不支持的协议 验证非 socks5 协议返回错误。
func TestWrapWithProxy_不支持的协议(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"http", "http://127.0.0.1:1080"},
		{"https", "https://127.0.0.1:1080"},
		{"socks4", "socks4://127.0.0.1:1080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &tcpDialer{}
			_, err := wrapWithProxy(base, &ProxyOptions{URL: tt.url})
			if err == nil {
				t.Fatalf("代理协议 %q 应返回错误", tt.url)
			}
		})
	}
}

// TestWrapWithProxy_SOCKS5构造成功 验证合法 socks5 URL 构造成功。
func TestWrapWithProxy_SOCKS5构造成功(t *testing.T) {
	base := &tcpDialer{}
	d, err := wrapWithProxy(base, &ProxyOptions{URL: "socks5://127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("wrapWithProxy(socks5): %v", err)
	}
	if d == nil {
		t.Fatal("不应返回 nil")
	}
	// 不应返回 base（应该被包装了）
	if d == base {
		t.Fatal("SOCKS5 应返回包装后的 dialer，不是原始 base")
	}
}

// TestWrapWithProxy_SOCKS5带认证 验证带用户名密码的 socks5 URL 构造成功。
func TestWrapWithProxy_SOCKS5带认证(t *testing.T) {
	base := &tcpDialer{}
	d, err := wrapWithProxy(base, &ProxyOptions{URL: "socks5://user:pass@127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("wrapWithProxy(socks5+auth): %v", err)
	}
	if d == nil {
		t.Fatal("不应返回 nil")
	}
}

// TestProxyDialer_接口满足 验证 proxyDialer 满足 Dialer 接口。
func TestProxyDialer_接口满足(t *testing.T) {
	var _ Dialer = (*proxyDialer)(nil)
}

// TestNewDialer_WithProxy_TCP 验证 NewDialer 可以构造带 proxy 的 TCP dialer。
func TestNewDialer_WithProxy_TCP(t *testing.T) {
	d, err := NewDialer(&Config{
		Protocol: TCP,
		Proxy:    &ProxyOptions{URL: "socks5://127.0.0.1:1080"},
	})
	if err != nil {
		t.Fatalf("NewDialer(TCP+proxy): %v", err)
	}
	if d == nil {
		t.Fatal("不应返回 nil")
	}
}

// TestNewDialer_WithNilProxy 验证 NewDialer proxy 为 nil 时正常构造。
func TestNewDialer_WithNilProxy(t *testing.T) {
	d, err := NewDialer(&Config{Protocol: TCP, Proxy: nil})
	if err != nil {
		t.Fatalf("NewDialer(TCP, no proxy): %v", err)
	}
	if d == nil {
		t.Fatal("不应返回 nil")
	}
}

// TestNewBaseDialer_覆盖所有分支 表驱动验证 newBaseDialer 各协议分支。
func TestNewBaseDialer_覆盖所有分支(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{"TCP", &Config{Protocol: TCP}, false},
		{"TLS_nil_opts", &Config{Protocol: TLS}, true}, // TLS 协议必须提供 TLSOptions
		{"WSS_nil_opts", &Config{Protocol: WSS}, true}, // WSS 协议必须提供 TLSOptions
		{"WS", &Config{Protocol: WS, WSPath: "/ws"}, false},
		{"GRPC", &Config{Protocol: GRPC}, false},
		{"unknown", &Config{Protocol: "badproto"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := newBaseDialer(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("应返回错误")
				}
				return
			}
			if err != nil {
				t.Fatalf("newBaseDialer: %v", err)
			}
			if d == nil {
				t.Fatal("不应返回 nil")
			}
		})
	}
}
