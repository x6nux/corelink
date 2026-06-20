package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/url"

	xproxy "golang.org/x/net/proxy"
)

// wrapWithProxy 把基础 Dialer 包一层上游代理/sing-box 出站。
func wrapWithProxy(base Dialer, p *ProxyOptions) (Dialer, error) {
	if p == nil {
		return base, nil
	}
	if p.SingBoxOutbound != "" {
		return newSingBoxDialer(p) // 启动 sing-box 二进制子进程，经其本地 socks 拨号
	}
	if p.URL == "" {
		return base, nil
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, fmt.Errorf("tunnel: 代理 URL 非法: %w", err)
	}
	switch u.Scheme {
	case "socks5":
		var auth *xproxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: pw}
		}
		pd, err := xproxy.SOCKS5("tcp", u.Host, auth, &bypassForwarder{})
		if err != nil {
			return nil, err
		}
		return &proxyDialer{pd: pd}, nil
	default:
		return nil, fmt.Errorf("tunnel: 暂不支持代理协议 %q", u.Scheme)
	}
}

// bypassForwarder 使 SOCKS5 底层连接带 BindControl 绕过 TUN 策略路由。
type bypassForwarder struct{}

func (bypassForwarder) Dial(network, addr string) (net.Conn, error) {
	return (&net.Dialer{Control: BindControl}).Dial(network, addr)
}

func (bypassForwarder) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{Control: BindControl}).DialContext(ctx, network, addr)
}

type proxyDialer struct{ pd xproxy.Dialer }

func (d *proxyDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	if cd, ok := d.pd.(xproxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", addr)
	}
	return d.pd.Dial("tcp", addr)
}
