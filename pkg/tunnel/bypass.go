package tunnel

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
)

// BypassTransport 返回已注入 BindControl 的 http.Transport。
// 分流模式下，通过此函数创建的 HTTP 客户端会绑定物理网卡 + fwmark 绕过 TUN 策略路由。
func BypassTransport(tlsCfg *tls.Config) *http.Transport {
	return &http.Transport{
		TLSClientConfig: tlsCfg,
		DialContext:     (&net.Dialer{Control: BindControl}).DialContext,
	}
}

// BypassGRPCDialer 返回用于 grpc.WithContextDialer 的拨号函数，
// 使 gRPC 连接绑定物理网卡 + fwmark 绕过 TUN 策略路由。
func BypassGRPCDialer() func(ctx context.Context, addr string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{Control: BindControl}).DialContext(ctx, "tcp", addr)
	}
}
