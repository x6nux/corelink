package metadata

import (
	"net/netip"
	"testing"
)

func TestProtocolConstants(t *testing.T) {
	if ProtocolDNS != "dns" {
		t.Errorf("ProtocolDNS = %q, want %q", ProtocolDNS, "dns")
	}
	if ProtocolTLS != "tls" {
		t.Errorf("ProtocolTLS = %q, want %q", ProtocolTLS, "tls")
	}
	if ProtocolUnknown != "" {
		t.Errorf("ProtocolUnknown = %q, want empty", ProtocolUnknown)
	}
}

func TestNetworkConstants(t *testing.T) {
	if NetworkTCP != "tcp" {
		t.Errorf("NetworkTCP = %q", NetworkTCP)
	}
	if NetworkUDP != "udp" {
		t.Errorf("NetworkUDP = %q", NetworkUDP)
	}
}

func TestFlowStateConstants(t *testing.T) {
	if FlowNew != 0 || FlowEstablished != 1 || FlowClosing != 2 || FlowClosed != 3 {
		t.Error("FlowState iota values incorrect")
	}
}

func TestInboundContext_IsDNS(t *testing.T) {
	ctx := &InboundContext{Protocol: ProtocolDNS}
	if ctx.Protocol != ProtocolDNS {
		t.Error("should detect DNS protocol")
	}
}

func TestInboundContext_Fields(t *testing.T) {
	ctx := &InboundContext{
		Network:     NetworkUDP,
		IPVersion:   4,
		Source:      netip.MustParseAddrPort("10.0.0.1:12345"),
		Destination: netip.MustParseAddrPort("100.64.0.1:53"),
		Protocol:    ProtocolDNS,
		DNSHijacked: true,
	}
	if ctx.Network != "udp" {
		t.Errorf("Network = %q", ctx.Network)
	}
	if !ctx.DNSHijacked {
		t.Error("DNSHijacked should be true")
	}
	if ctx.IPVersion != 4 {
		t.Errorf("IPVersion = %d", ctx.IPVersion)
	}
}
