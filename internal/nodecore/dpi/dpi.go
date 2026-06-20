package dpi

import (
	"bytes"

	"github.com/x6nux/corelink/internal/nodecore/metadata"
)

// httpMethods 列出常见 HTTP 请求方法前缀，用于快速匹配。
var httpMethods = [][]byte{
	[]byte("GET "),
	[]byte("POST "),
	[]byte("PUT "),
	[]byte("DELETE "),
	[]byte("HEAD "),
	[]byte("OPTIONS "),
	[]byte("PATCH "),
	[]byte("CONNECT "),
}

// Result 保存 Inspect 函数的检测结果。
type Result struct {
	// Protocol 已识别协议（dns/tls/http/unknown）
	Protocol string
	// Domain 嗅探到的目标域名，可能为空
	Domain string
	// Done 表示检测已完成（无论识别成功与否）
	Done bool
}

// Inspect 检测 payload 的应用层协议，返回 Result。
// payload 为空时 Done=false（需要更多数据）。
// 检测优先级：TLS > HTTP > DNS > Unknown。
func Inspect(payload []byte) Result {
	if len(payload) == 0 {
		return Result{}
	}

	// TLS：第一个字节固定为 0x16（Handshake）
	if payload[0] == 0x16 {
		sni, ok := parseTLSClientHello(payload)
		if !ok {
			// 数据足够长（>=11 字节可容纳 TLS record header + handshake type + length）
			// 但解析仍失败，说明不是合法 TLS ClientHello，标记为 Unknown 终止检测。
			if len(payload) >= 11 {
				return Result{Protocol: metadata.ProtocolUnknown, Done: true}
			}
			return Result{}
		}
		return Result{Protocol: metadata.ProtocolTLS, Domain: sni, Done: true}
	}

	// HTTP：以已知请求方法开头
	for _, method := range httpMethods {
		if bytes.HasPrefix(payload, method) {
			host, _ := parseHTTPHost(payload)
			return Result{Protocol: metadata.ProtocolHTTP, Domain: host, Done: true}
		}
	}

	// DNS：最后匹配，避免与 TLS/HTTP 误判
	if SniffDNS(payload) {
		return Result{Protocol: metadata.ProtocolDNS, Done: true}
	}

	return Result{Protocol: metadata.ProtocolUnknown, Done: true}
}

// InspectCtx 检测 payload 协议，结果直接写入 InboundContext。
// 返回 true 表示检测完成；false 表示 payload 为空，需要更多数据。
// 检测优先级：TLS > HTTP > DNS > Unknown。
func InspectCtx(payload []byte, ctx *metadata.InboundContext) (done bool) {
	if len(payload) == 0 {
		return false
	}

	// TLS：第一个字节固定为 0x16（Handshake）
	if payload[0] == 0x16 {
		sni, ok := parseTLSClientHello(payload)
		if !ok {
			// 数据足够长但解析失败，不是合法 TLS，标记 Unknown 终止检测
			if len(payload) >= 11 {
				ctx.Protocol = metadata.ProtocolUnknown
				return true
			}
			return false
		}
		ctx.Protocol = metadata.ProtocolTLS
		ctx.Domain = sni
		return true
	}

	// HTTP：以已知请求方法开头
	for _, method := range httpMethods {
		if bytes.HasPrefix(payload, method) {
			host, _ := parseHTTPHost(payload)
			ctx.Protocol = metadata.ProtocolHTTP
			ctx.Domain = host
			return true
		}
	}

	// DNS：最后匹配，避免与 TLS/HTTP 误判
	if SniffDNS(payload) {
		ctx.Protocol = metadata.ProtocolDNS
		return true
	}

	ctx.Protocol = metadata.ProtocolUnknown
	return true
}
