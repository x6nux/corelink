package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/acme/autocert"

	"github.com/coder/websocket"
)

// wsListener 实现 Listener 接口，基于 http.Server + chan 结构接收 WebSocket 连接。
//
// WSS 模式下 TLS 终结放在 listener 层（tls.NewListener），http.Server.Serve
// 收到的已是握手完成的明文连接——与 tcp.go 的 tlsListener「Accept 时握手」模式
// 一致，从根本上消除 http.Server 运行期对 TLSConfig 的并发读写竞争。
// 服务端证书通过 tls.Config.GetCertificate 回调在握手时读取，回调里用
// atomic.Pointer 原子读 srvCert，setServerCert 用原子写注入，二者无数据竞争。
type wsListener struct {
	ln        net.Listener
	srv       *http.Server
	accept    chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once                       // 保证 close(closed) 至多执行一次，消除并发 Close 竞态
	srvCert   atomic.Pointer[tls.Certificate] // wss 服务端证书（GetCertificate 回调读、setServerCert 写）
}

// setServerCert 注入已知证书（供 wss 钉扎测试拿到确定指纹）。
// 原子写，与 GetCertificate 回调的原子读无竞争；只需在第一次 TLS 握手前调用。
func (l *wsListener) setServerCert(c tls.Certificate) {
	l.srvCert.Store(&c)
}

func newWSListener(cfg *Config, addr string) (Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	path := cfg.WSPath
	if path == "" {
		path = "/"
	}
	wl := &wsListener{
		ln:     ln,
		accept: make(chan net.Conn, 8),
		closed: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
		select {
		case wl.accept <- nc:
		case <-wl.closed:
			nc.Close()
		case <-r.Context().Done():
			nc.Close()
		}
	})
	wl.srv = &http.Server{Handler: mux}

	serveLn := ln
	// wss：在 listener 层终结 TLS，与 tcp.go Listen 的处理对称。
	if cfg.Protocol == WSS && cfg.TLS != nil {
		switch cfg.TLS.Mode {
		case TLSModePinned:
			// pinned 模式：自动生成自签证书，测试可用 setServerCert 覆盖。
			name := cfg.TLS.ServerName
			if name == "" {
				name = "127.0.0.1"
			}
			srvCert, _, err := GenerateSelfSigned(name)
			if err != nil {
				ln.Close()
				return nil, err
			}
			wl.srvCert.Store(&srvCert)
			tlsConf := &tls.Config{
				MinVersion: tls.VersionTLS12,
				// GetCertificate 在握手时回调，原子读当前注入的证书；
				// 保证 server 实际发送的证书 == setServerCert 注入的那一张。
				GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
					return wl.srvCert.Load(), nil
				},
			}
			serveLn = tls.NewListener(ln, tlsConf)
		case TLSModeACME:
			// acme 模式：通过 autocert.Manager 自动申请/续签公网可信证书，
			// 与 tcp.go Listen 中 ServerTLSConfig 的处理对称。
			cacheDir := cfg.TLS.ACMECacheDir
			if cacheDir == "" {
				if d, err := os.UserCacheDir(); err == nil {
					cacheDir = d + "/corelink/acme"
				} else {
					cacheDir = "/var/cache/corelink/acme"
				}
			}
			m := &autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				HostPolicy: autocert.HostWhitelist(cfg.TLS.ACMEDomains...),
				Cache:      autocert.DirCache(cacheDir),
			}
			serveLn = tls.NewListener(ln, m.TLSConfig())
		default:
			ln.Close()
			return nil, fmt.Errorf("tunnel: wss 不支持的 TLS 模式 %q", cfg.TLS.Mode)
		}
	}
	go wl.srv.Serve(serveLn)
	return wl, nil
}

func (l *wsListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accept:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *wsListener) Addr() net.Addr { return l.ln.Addr() }

func (l *wsListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return l.srv.Close()
}

// wsDialer 实现 Dialer 接口，使用 coder/websocket 拨号。
type wsDialer struct {
	scheme  string // "ws" | "wss"
	path    string
	tlsConf *tls.Config // 字段名用 tlsConf，避免与 tls 包名冲突
}

func newWSDialer(cfg *Config) (Dialer, error) {
	d := &wsDialer{scheme: "ws", path: cfg.WSPath}
	if d.path == "" {
		d.path = "/"
	}
	if cfg.Protocol == WSS {
		d.scheme = "wss"
		tc, err := ClientTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		// WebSocket 走 HTTP/1.1 Upgrade，ALPN 必须包含 "http/1.1"
		// 才能被服务端（M1 多协议 listener）正确分流到 WebSocket 路径。
		if !slices.Contains(tc.NextProtos, "http/1.1") {
			tc.NextProtos = append(tc.NextProtos, "http/1.1")
		}
		d.tlsConf = tc
	}
	return d, nil
}

func (d *wsDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	url := d.scheme + "://" + addr + d.path
	opts := &websocket.DialOptions{
		// 非 TLS 路径也设置 BindControl，使 WS 连接绑定物理网卡绕过 TUN。
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Control: BindControl}).DialContext,
			},
		},
	}
	if d.tlsConf != nil {
		// 使用 DialTLSContext 而非 TLSClientConfig：Go 的 http.Transport
		// 在设置 TLSClientConfig 后不保证传递 NextProtos（ALPN），
		// 直接控制 TLS 握手以确保 ALPN 协商为 "http/1.1"，
		// 与 M1 多协议 listener（AccessListener）的分流逻辑一致。
		tlsConf := d.tlsConf
		opts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				DialTLSContext: func(dialCtx context.Context, network, dialAddr string) (net.Conn, error) {
					td := &tls.Dialer{Config: tlsConf, NetDialer: &net.Dialer{Control: BindControl}}
					return td.DialContext(dialCtx, network, dialAddr)
				},
			},
		}
	}
	c, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}
