// Package tlsext 封装 TLS 0-RTT 扩展层，将 CoreLink 现有 crypto/tls 配置
// 透明桥接到 gitlab.com/go-extension/tls（支持 TCP 0-RTT early data）。
//
// 功能由 featureflag.TLS0RTT 控制：
//   - 启用时：返回 *extls.Config（带 MaxEarlyData / AllowEarlyData）
//   - 关闭或 flags==nil 时：回退到标准 *stdtls.Config
//
// 调用方通过 ServerConfig / ClientConfig / NewListener 操作，无需关心底层
// 使用哪个 TLS 库——返回的配置类型为 any，由 NewListener 做类型分派。
package tlsext

import (
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"net"

	extls "gitlab.com/go-extension/tls"

	"github.com/x6nux/corelink/internal/featureflag"
)

// DefaultMaxEarlyData 是服务端允许接收的 0-RTT early data 最大字节数（16 KB）。
const DefaultMaxEarlyData = 16384

// ServerConfig 创建支持 0-RTT 的服务端 TLS 配置。
//
// flags 为 nil 或 TLS0RTT 未启用时，回退到标准 crypto/tls。
// 返回 *extls.Config 或 *stdtls.Config（根据 flag 状态），调用方传给 NewListener 使用。
func ServerConfig(certs []stdtls.Certificate, clientCAs *x509.CertPool, flags *featureflag.Flags) any {
	if flags != nil && flags.Enabled(featureflag.TLS0RTT) {
		return &extls.Config{
			Certificates:   convertCerts(certs),
			ClientAuth:     extls.RequireAndVerifyClientCert,
			ClientCAs:      clientCAs,
			MinVersion:     extls.VersionTLS13, // 0-RTT 仅 TLS 1.3
			NextProtos:     []string{"corelink", "h2", "http/1.1"},
			MaxEarlyData:   DefaultMaxEarlyData,
			AllowEarlyData: true,
		}
	}
	return &stdtls.Config{
		Certificates: certs,
		ClientAuth:   stdtls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   stdtls.VersionTLS12,
		NextProtos:   []string{"corelink", "h2", "http/1.1"},
	}
}

// ClientConfig 创建支持 0-RTT 的客户端 TLS 配置。
//
// flags 为 nil 或 TLS0RTT 未启用时，回退到标准 crypto/tls。
// cache 提供 session ticket 缓存（客户端恢复会话用），可为 nil（禁用缓存）。
func ClientConfig(certs []stdtls.Certificate, rootCAs *x509.CertPool, cache *SessionCache, flags *featureflag.Flags) any {
	if flags != nil && flags.Enabled(featureflag.TLS0RTT) {
		cfg := &extls.Config{
			Certificates:       convertCerts(certs),
			RootCAs:            rootCAs,
			MinVersion:         extls.VersionTLS13,
			NextProtos:         []string{"corelink", "h2", "http/1.1"},
			ServerName:         "localhost", // 调用方按需覆盖
			InsecureSkipVerify: false,
		}
		if cache != nil {
			cfg.ClientSessionCache = cache.ext
		}
		return cfg
	}
	cfg := &stdtls.Config{
		Certificates: certs,
		RootCAs:      rootCAs,
		MinVersion:   stdtls.VersionTLS12,
		NextProtos:   []string{"corelink", "h2", "http/1.1"},
		ServerName:   "localhost",
	}
	if cache != nil {
		cfg.ClientSessionCache = cache.std
	}
	return cfg
}

// NewListener 创建 TLS listener。
//
// cfg 可以是 *extls.Config 或 *stdtls.Config（由 ServerConfig 返回），
// 其它类型 panic（编程错误）。
func NewListener(inner net.Listener, cfg any) net.Listener {
	switch c := cfg.(type) {
	case *extls.Config:
		return extls.NewListener(inner, c)
	case *stdtls.Config:
		return stdtls.NewListener(inner, c)
	default:
		panic("tlsext: 不支持的 TLS 配置类型")
	}
}

// ConfirmHandshake 在服务端等待 TLS 握手完成（用于 0-RTT 防重放）。
//
// 对 *extls.Conn 调用 ConfirmHandshake()；对标准 *stdtls.Conn 调用 Handshake()
// （标准 TLS 无 0-RTT，Handshake 已在 Accept 时隐式完成，此处调用为幂等操作）。
// 对其它类型返回 nil（无需确认）。
func ConfirmHandshake(conn net.Conn) error {
	switch c := conn.(type) {
	case *extls.Conn:
		return c.ConfirmHandshake()
	case *stdtls.Conn:
		return c.Handshake()
	default:
		return nil
	}
}

// SessionCache 封装 session ticket 缓存，同时持有 extls 和 stdtls 两种缓存实例。
// 调用方无需关心底层 TLS 库类型——ClientConfig 根据 flag 选取对应的缓存。
type SessionCache struct {
	ext extls.ClientSessionCache
	std stdtls.ClientSessionCache
}

// NewSessionCache 创建 session ticket 缓存。
// capacity 指定 LRU 缓存容量（session 条目数）。
func NewSessionCache(capacity int) *SessionCache {
	return &SessionCache{
		ext: extls.NewLRUClientSessionCache(capacity),
		std: stdtls.NewLRUClientSessionCache(capacity),
	}
}

// SetNextProtos 在 any 类型的 TLS 配置上设置 ALPN 协议列表。
//
// cfg 必须是 *extls.Config 或 *stdtls.Config（由 ServerConfig/ClientConfig 返回），
// 其它类型 panic。
func SetNextProtos(cfg any, protos []string) {
	switch c := cfg.(type) {
	case *extls.Config:
		c.NextProtos = protos
	case *stdtls.Config:
		c.NextProtos = protos
	default:
		panic("tlsext: SetNextProtos: 不支持的 TLS 配置类型")
	}
}

// SetInsecureSkipVerify 在 any 类型的 TLS 配置上设置 InsecureSkipVerify。
//
// 用于互联拨号场景：配合自定义 VerifyConnection 做 CN + 指纹校验。
func SetInsecureSkipVerify(cfg any, skip bool) {
	switch c := cfg.(type) {
	case *extls.Config:
		c.InsecureSkipVerify = skip
	case *stdtls.Config:
		c.InsecureSkipVerify = skip
	default:
		panic("tlsext: SetInsecureSkipVerify: 不支持的 TLS 配置类型")
	}
}

// SetServerName 在 any 类型的 TLS 配置上设置 ServerName（SNI）。
func SetServerName(cfg any, name string) {
	switch c := cfg.(type) {
	case *extls.Config:
		c.ServerName = name
	case *stdtls.Config:
		c.ServerName = name
	default:
		panic("tlsext: SetServerName: 不支持的 TLS 配置类型")
	}
}

// SetVerifyConnection 在 any 类型的 TLS 配置上设置 VerifyConnection 回调。
//
// verify 接收 *x509.Certificate 切片（对端证书链），由调用方执行链校验 + CN + 指纹 pin。
// 本函数内部适配 extls.ConnectionState / stdtls.ConnectionState 的类型差异。
func SetVerifyConnection(cfg any, verify func(peerCerts []*x509.Certificate) error) {
	switch c := cfg.(type) {
	case *extls.Config:
		c.VerifyConnection = func(cs extls.ConnectionState) error {
			return verify(cs.PeerCertificates)
		}
	case *stdtls.Config:
		c.VerifyConnection = func(cs stdtls.ConnectionState) error {
			return verify(cs.PeerCertificates)
		}
	default:
		panic("tlsext: SetVerifyConnection: 不支持的 TLS 配置类型")
	}
}

// ClientHandshake 在 raw 连接上完成 TLS 客户端握手并返回 TLS 连接。
//
// cfg 可以是 *extls.Config 或 *stdtls.Config，其它类型 panic。
func ClientHandshake(ctx context.Context, raw net.Conn, cfg any) (net.Conn, error) {
	switch c := cfg.(type) {
	case *extls.Config:
		tc := extls.Client(raw, c)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		return tc, nil
	case *stdtls.Config:
		tc := stdtls.Client(raw, c)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		return tc, nil
	default:
		panic("tlsext: ClientHandshake: 不支持的 TLS 配置类型")
	}
}

// ConnState 从 TLS 连接中提取对端证书和协商协议。
//
// 支持 *extls.Conn 和 *stdtls.Conn。
// 返回 (peerCerts, negotiatedProtocol, ok)。
func ConnState(c net.Conn) (peerCerts []*x509.Certificate, proto string, ok bool) {
	switch tc := c.(type) {
	case *extls.Conn:
		st := tc.ConnectionState()
		return st.PeerCertificates, st.NegotiatedProtocol, true
	case *stdtls.Conn:
		st := tc.ConnectionState()
		return st.PeerCertificates, st.NegotiatedProtocol, true
	default:
		return nil, "", false
	}
}

// Handshake 对 TLS 连接执行握手（兼容 extls / stdtls）。
func Handshake(c net.Conn) error {
	switch tc := c.(type) {
	case *extls.Conn:
		return tc.Handshake()
	case *stdtls.Conn:
		return tc.Handshake()
	default:
		return nil
	}
}

// IsTLSConn 判断连接是否为 TLS 连接（extls 或 stdtls）。
func IsTLSConn(c net.Conn) bool {
	switch c.(type) {
	case *extls.Conn, *stdtls.Conn:
		return true
	default:
		return false
	}
}

// UnwrapTCP 从 TLS 连接中提取底层 TCP 连接。
//
// 支持 *extls.Conn（通过 Compatible 转换取 NetConn）和 *stdtls.Conn。
func UnwrapTCP(c net.Conn) (*net.TCPConn, bool) {
	switch tc := c.(type) {
	case *stdtls.Conn:
		tcp, ok := tc.NetConn().(*net.TCPConn)
		return tcp, ok
	case *extls.Conn:
		// extls.Conn 不直接暴露 NetConn()——通过 ConnectionState().Compatible()
		// 无法取底层连接。改用类型断言链：extls.Conn 包装的底层连接应在 Accept 时已设好 NoDelay。
		return nil, false
	default:
		tcp, ok := c.(*net.TCPConn)
		return tcp, ok
	}
}

// convertCerts 将 stdtls.Certificate 切片转换为 extls.Certificate 切片。
//
// 两者结构兼容（Certificate [][]byte + PrivateKey + Leaf 等），
// 但属于不同包的不同类型，需逐字段复制。
func convertCerts(stdCerts []stdtls.Certificate) []extls.Certificate {
	out := make([]extls.Certificate, len(stdCerts))
	for i, sc := range stdCerts {
		out[i] = extls.Certificate{
			Certificate: sc.Certificate,
			PrivateKey:  sc.PrivateKey,
			OCSPStaple:  sc.OCSPStaple,
			Leaf:        sc.Leaf,
		}
		// SignedCertificateTimestamps: 逐项复制
		if len(sc.SignedCertificateTimestamps) > 0 {
			out[i].SignedCertificateTimestamps = make([][]byte, len(sc.SignedCertificateTimestamps))
			copy(out[i].SignedCertificateTimestamps, sc.SignedCertificateTimestamps)
		}
	}
	return out
}
