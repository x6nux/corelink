package config

import (
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// configConfidence 静态配置入口的置信度（0-100）。
// 配置由运维显式声明，可信度高。
const configConfidence uint32 = 95

// IngressConfig 静态配置的单个接入点。
type IngressConfig struct {
	// Kind 接入类型："ip"（直连 IP）或 "cdn"（经 CDN 边缘）。
	Kind string `json:"kind"`
	// Host 主机（IP 或域名）。
	Host string `json:"host"`
	// Port TCP/隧道端口。
	Port uint32 `json:"port,omitempty"`
	// Protocols 支持的隧道协议字符串："tcp"/"ws"/"tls"/"wss"/"grpc"。
	Protocols []string `json:"protocols,omitempty"`
	// UdpPort UDP 主路端口（CDN 入口通常无此项）。
	UdpPort uint32 `json:"udp_port,omitempty"`
	// Sni CDN 入口的 SNI（TLS 服务器名），kind=cdn 时保留。
	Sni string `json:"sni,omitempty"`
}

// protocolStringToEnum 将协议字符串映射为 genv1.TunnelProtocol；未知字符串返回 (UNSPECIFIED, false)。
func protocolStringToEnum(s string) (genv1.TunnelProtocol, bool) {
	switch s {
	case "tcp":
		return genv1.TunnelProtocol_TUNNEL_PROTOCOL_TCP, true
	case "ws":
		return genv1.TunnelProtocol_TUNNEL_PROTOCOL_WS, true
	case "tls":
		return genv1.TunnelProtocol_TUNNEL_PROTOCOL_TLS, true
	case "wss":
		return genv1.TunnelProtocol_TUNNEL_PROTOCOL_WSS, true
	case "grpc":
		return genv1.TunnelProtocol_TUNNEL_PROTOCOL_GRPC, true
	default:
		return genv1.TunnelProtocol_TUNNEL_PROTOCOL_UNSPECIFIED, false
	}
}

// isHTTPLikeProtocol 判定协议是否为 HTTP 类（可经 CDN 边缘转发）。
// CDN 仅支持 WebSocket 类承载（ws/wss）。
func isHTTPLikeProtocol(p genv1.TunnelProtocol) bool {
	switch p {
	case genv1.TunnelProtocol_TUNNEL_PROTOCOL_WS, genv1.TunnelProtocol_TUNNEL_PROTOCOL_WSS:
		return true
	default:
		return false
	}
}

// ConfigIngresses 将静态配置转换为候选 genv1.Ingress。
//
//   - kind="cdn" → INGRESS_KIND_CDN，保留 Sni，仅保留 HTTP 类协议（ws/wss）。
//   - kind="ip"（或其它）→ INGRESS_KIND_IP_DIRECT，保留全部已识别协议。
//
// 所有配置入口 source=CONFIG、confidence=95。未识别的协议字符串被跳过。
func ConfigIngresses(cfgs []IngressConfig) []*genv1.Ingress {
	if len(cfgs) == 0 {
		return nil
	}
	out := make([]*genv1.Ingress, 0, len(cfgs))
	for _, c := range cfgs {
		isCDN := c.Kind == "cdn"

		var protos []genv1.TunnelProtocol
		for _, ps := range c.Protocols {
			p, ok := protocolStringToEnum(ps)
			if !ok {
				continue
			}
			if isCDN && !isHTTPLikeProtocol(p) {
				continue // CDN 入口过滤非 HTTP 类协议。
			}
			protos = append(protos, p)
		}

		ing := &genv1.Ingress{
			Host:       c.Host,
			Port:       c.Port,
			Protocols:  protos,
			Source:     genv1.IngressSource_INGRESS_SOURCE_CONFIG,
			Confidence: configConfidence,
		}
		if isCDN {
			ing.Kind = genv1.IngressKind_INGRESS_KIND_CDN
			ing.Sni = c.Sni
		} else {
			ing.Kind = genv1.IngressKind_INGRESS_KIND_IP_DIRECT
			ing.UdpPort = c.UdpPort
		}
		out = append(out, ing)
	}
	return out
}
