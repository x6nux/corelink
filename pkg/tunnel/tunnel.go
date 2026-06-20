// Package tunnel 提供 CoreLink 数据面/控制面的多协议隧道抽象。
// 上层只依赖 Dialer/Listener 接口，具体协议(tcp/ws/tls/wss/grpc)与
// 上游代理/sing-box 出站均为可替换实现。
package tunnel

import (
	"context"
	"net"
)

// Protocol 隧道协议。
type Protocol string

const (
	TCP  Protocol = "tcp"
	WS   Protocol = "ws"
	TLS  Protocol = "tls"
	WSS  Protocol = "wss"
	GRPC Protocol = "grpc"
)

// Dialer 主动建立到目标的隧道连接，返回面向流的 net.Conn。
type Dialer interface {
	Dial(ctx context.Context, addr string) (net.Conn, error)
}

// DialCloser 扩展 Dialer，提供关闭子资源能力（如 sing-box 子进程清理）。
// 调用方应在不再需要 Dialer 时检查 io.Closer 或此接口并调 Close()。
type DialCloser interface {
	Dialer
	Close() error
}

// Listener 监听并接受隧道连接。
type Listener interface {
	Accept() (net.Conn, error)
	Addr() net.Addr
	Close() error
}

// ProxyOptions 上游代理 / sing-box 出站参数（Task 3.6 细化）。
type ProxyOptions struct {
	// SingBoxOutbound 为 sing-box 出站配置的 JSON 片段（outbound 对象）。
	// 不为空时：以"启动 sing-box 二进制子进程"方式提供出站——
	// 生成含本地 socks 入站 + 该出站的临时配置，拉起 sing-box 进程，
	// 再经其本地 socks 端口拨号（见 Task 3.6/3.6b）。不嵌入 sing-box 库。
	SingBoxOutbound string
	// SingBoxBinary 为 sing-box 可执行文件路径，缺省 "sing-box"（按 PATH 查找）。
	SingBoxBinary string
	URL           string // 形如 socks5://user:pass@host:port
}

// Config 构造 Dialer/Listener 的统一参数。
type Config struct {
	Protocol Protocol
	TLS      *TLSOptions
	Proxy    *ProxyOptions
	WSPath   string
}
