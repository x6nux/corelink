package config

import (
	"encoding/json"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func TestConfigIngresses_CDN(t *testing.T) {
	out := ConfigIngresses([]IngressConfig{{
		Kind:      "cdn",
		Host:      "edge.example.com",
		Port:      443,
		Protocols: []string{"wss", "tls", "tcp"}, // tcp 非 HTTP 类，CDN 应过滤掉
		Sni:       "edge.example.com",
	}})
	if len(out) != 1 {
		t.Fatalf("期望 1 个 ingress，got %d", len(out))
	}
	ing := out[0]
	if ing.Kind != genv1.IngressKind_INGRESS_KIND_CDN {
		t.Errorf("kind = %v, 期望 CDN", ing.Kind)
	}
	if ing.Sni != "edge.example.com" {
		t.Errorf("sni = %q 应保留", ing.Sni)
	}
	if ing.Source != genv1.IngressSource_INGRESS_SOURCE_CONFIG {
		t.Errorf("source = %v, 期望 CONFIG", ing.Source)
	}
	if ing.Confidence != configConfidence {
		t.Errorf("confidence = %d, 期望 %d", ing.Confidence, configConfidence)
	}
	// CDN 仅保留 HTTP 类协议 (ws/wss)，过滤 tcp/tls/grpc。
	for _, p := range ing.Protocols {
		if p == genv1.TunnelProtocol_TUNNEL_PROTOCOL_TCP || p == genv1.TunnelProtocol_TUNNEL_PROTOCOL_GRPC {
			t.Errorf("CDN 不应包含非 HTTP 协议 %v", p)
		}
	}
	if !hasProto(ing.Protocols, genv1.TunnelProtocol_TUNNEL_PROTOCOL_WSS) {
		t.Errorf("CDN 应保留 wss, got %v", ing.Protocols)
	}
}

func TestConfigIngresses_IPDirect(t *testing.T) {
	out := ConfigIngresses([]IngressConfig{{
		Kind:      "ip",
		Host:      "203.0.113.10",
		Port:      8443,
		Protocols: []string{"tcp", "grpc", "ws"},
		UdpPort:   51820,
	}})
	if len(out) != 1 {
		t.Fatalf("期望 1 个 ingress，got %d", len(out))
	}
	ing := out[0]
	if ing.Kind != genv1.IngressKind_INGRESS_KIND_IP_DIRECT {
		t.Errorf("kind = %v, 期望 IP_DIRECT", ing.Kind)
	}
	if ing.Host != "203.0.113.10" || ing.Port != 8443 || ing.UdpPort != 51820 {
		t.Errorf("字段透传错误: %+v", ing)
	}
	if !hasProto(ing.Protocols, genv1.TunnelProtocol_TUNNEL_PROTOCOL_TCP) ||
		!hasProto(ing.Protocols, genv1.TunnelProtocol_TUNNEL_PROTOCOL_GRPC) {
		t.Errorf("IP_DIRECT 应保留全部协议, got %v", ing.Protocols)
	}
}

func TestConfigIngresses_UnknownProtocolSkipped(t *testing.T) {
	out := ConfigIngresses([]IngressConfig{{
		Kind:      "ip",
		Host:      "1.2.3.4",
		Protocols: []string{"tcp", "bogus", ""},
	}})
	if len(out[0].Protocols) != 1 {
		t.Errorf("未知协议应跳过, got %v", out[0].Protocols)
	}
}

func TestConfigIngresses_Empty(t *testing.T) {
	if got := ConfigIngresses(nil); len(got) != 0 {
		t.Errorf("空输入应返回空, got %d", len(got))
	}
}

func hasProto(ps []genv1.TunnelProtocol, want genv1.TunnelProtocol) bool {
	for _, p := range ps {
		if p == want {
			return true
		}
	}
	return false
}

func TestConfig_IngressesJSONCompat(t *testing.T) {
	// 既有配置（无 ingresses 字段）应正常反序列化，Validate 不报错。
	const legacy = `{"controller_enroll_addr":"c:7443","controller_mtls_addr":"c:7444","controller_http_addr":"c:8080","enrollment_key":"k","controller_ca_hash":"sha256:aabb","role":"node"}`
	var c Config
	if err := json.Unmarshal([]byte(legacy), &c); err != nil {
		t.Fatalf("legacy 配置反序列化失败: %v", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		t.Errorf("空 Ingresses 不应导致 Validate 失败: %v", err)
	}
	if len(c.Ingresses) != 0 {
		t.Errorf("legacy 配置 Ingresses 应为空")
	}

	// 带 ingresses 字段的配置。
	const withIng = `{"controller_enroll_addr":"c:7443","enrollment_key":"k","role":"node","ingresses":[{"kind":"cdn","host":"e.com","port":443,"sni":"e.com","protocols":["wss"]}]}`
	var c2 Config
	if err := json.Unmarshal([]byte(withIng), &c2); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if len(c2.Ingresses) != 1 || c2.Ingresses[0].Kind != "cdn" {
		t.Errorf("Ingresses 解析错误: %+v", c2.Ingresses)
	}
}
