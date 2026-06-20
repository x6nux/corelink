package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
)

// newBaseDialer 构造不含代理装饰的基础 Dialer。
func newBaseDialer(cfg *Config) (Dialer, error) {
	switch cfg.Protocol {
	case TCP:
		return &tcpDialer{}, nil
	case TLS:
		if cfg.TLS == nil {
			return nil, fmt.Errorf("tunnel: Protocol=%s 必须提供 TLSOptions", cfg.Protocol)
		}
		tc, err := ClientTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		return &tcpDialer{tlsConf: tc}, nil
	case WS, WSS:
		if cfg.Protocol == WSS && cfg.TLS == nil {
			return nil, fmt.Errorf("tunnel: Protocol=%s 必须提供 TLSOptions", cfg.Protocol)
		}
		return newWSDialer(cfg)
	case GRPC:
		return newGRPCDialer(cfg)
	default:
		return nil, fmt.Errorf("tunnel: NewDialer 暂不支持 %q", cfg.Protocol)
	}
}

// NewDialer 按 Config 构造 Dialer（tcp/tls；ws/wss 见 ws.go；grpc 见 grpc.go）。
// 若 cfg.Proxy 非空则自动包装代理/sing-box 出站装饰器。
func NewDialer(cfg *Config) (Dialer, error) {
	base, err := newBaseDialer(cfg)
	if err != nil {
		return nil, err
	}
	return wrapWithProxy(base, cfg.Proxy)
}

// Listen 按 Config 构造 Listener。
func Listen(cfg *Config, addr string) (Listener, error) {
	switch cfg.Protocol {
	case TCP:
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		return &tcpListener{Listener: l}, nil
	case TLS:
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		tl := &tlsListener{inner: l}
		// 默认（cfg.TLS 为 nil）或 pinned 模式：在 listener 层自管证书。
		// pinned 模式自动生成自签证书；测试可用 setServerCert 覆盖注入已知证书以拿到其指纹。
		if cfg.TLS == nil || cfg.TLS.Mode == TLSModePinned {
			name := ""
			if cfg.TLS != nil {
				name = cfg.TLS.ServerName
			}
			if name == "" {
				name = "127.0.0.1"
			}
			srvCert, _, err := GenerateSelfSigned(name)
			if err != nil {
				l.Close()
				return nil, err
			}
			tl.srvCert = srvCert
			return tl, nil
		}
		// 其它模式（acme/未知）复用 ServerTLSConfig：
		// acme 返回含 GetCertificate 钩子的 config；未知模式直接 fail-fast 返回 error，
		// 不再产出用零值证书握手、必然失败的 listener。
		tc, err := ServerTLSConfig(cfg.TLS)
		if err != nil {
			l.Close()
			return nil, err
		}
		tl.tlsConf = tc
		return tl, nil
	case WS, WSS:
		if cfg.Protocol == WSS && cfg.TLS == nil {
			return nil, fmt.Errorf("tunnel: Protocol=%s 必须提供 TLSOptions", cfg.Protocol)
		}
		return newWSListener(cfg, addr)
	case GRPC:
		return newGRPCListener(addr)
	default:
		return nil, fmt.Errorf("tunnel: Listen 暂不支持 %q", cfg.Protocol)
	}
}

type tcpDialer struct{ tlsConf *tls.Config }

func (d *tcpDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	nd := net.Dialer{Control: BindControl}
	c, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if d.tlsConf != nil {
		tc := tls.Client(c, d.tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, err
		}
		return tc, nil
	}
	return c, nil
}

type tcpListener struct{ net.Listener }

type tlsListener struct {
	inner   net.Listener
	srvCert tls.Certificate // pinned/默认模式：listener 自管的自签证书
	tlsConf *tls.Config     // acme 模式：来自 ServerTLSConfig（含 GetCertificate）
}

// setServerCert 用于 pinned/默认模式下覆盖注入已知证书（测试拿其指纹用）。
func (l *tlsListener) setServerCert(c tls.Certificate) { l.srvCert = c }

func (l *tlsListener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	// acme 模式用 ServerTLSConfig 的 config（GetCertificate 惰性取证书）；
	// pinned/默认模式用 listener 自管的 srvCert。
	conf := l.tlsConf
	if conf == nil {
		conf = &tls.Config{Certificates: []tls.Certificate{l.srvCert}, MinVersion: tls.VersionTLS12}
	}
	tc := tls.Server(c, conf)
	return tc, nil
}
func (l *tlsListener) Addr() net.Addr { return l.inner.Addr() }
func (l *tlsListener) Close() error   { return l.inner.Close() }
