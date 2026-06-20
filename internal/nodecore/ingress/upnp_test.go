package ingress

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/portmap"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func TestMappingToIngressUDP(t *testing.T) {
	m := &portmap.Mapping{
		Protocol:     portmap.ProtocolUPnP,
		ExternalIP:   "203.0.113.10",
		ExternalPort: 51820,
		InternalPort: 51820,
		TransportUDP: true,
		TTL:          7200 * time.Second,
		Gateway:      "http://192.168.1.1:5000/ctl",
	}

	ing := MappingToIngress(m)
	if ing == nil {
		t.Fatal("MappingToIngress returned nil for valid Mapping")
	}
	if ing.GetSource() != genv1.IngressSource_INGRESS_SOURCE_UPNP {
		t.Errorf("source: want UPNP, got %v", ing.GetSource())
	}
	if ing.GetConfidence() != upnpConfidence {
		t.Errorf("confidence: want %d, got %d", upnpConfidence, ing.GetConfidence())
	}
	if ing.GetKind() != genv1.IngressKind_INGRESS_KIND_IP_DIRECT {
		t.Errorf("kind: want IP_DIRECT, got %v", ing.GetKind())
	}
	if ing.GetHost() != "203.0.113.10" {
		t.Errorf("host: want 203.0.113.10, got %q", ing.GetHost())
	}
	if ing.GetPort() != 51820 {
		t.Errorf("port: want 51820, got %d", ing.GetPort())
	}
	if ing.GetUdpPort() != 51820 {
		t.Errorf("udp_port: want 51820 for UDP mapping, got %d", ing.GetUdpPort())
	}
	if ing.GetId() != "upnp-203.0.113.10-51820-udp" {
		t.Errorf("id: want upnp-203.0.113.10-51820-udp, got %q", ing.GetId())
	}
}

func TestMappingToIngressTCP(t *testing.T) {
	m := &portmap.Mapping{
		Protocol:     portmap.ProtocolNATPMP,
		ExternalIP:   "198.51.100.20",
		ExternalPort: 443,
		InternalPort: 443,
		TransportUDP: false,
		TTL:          3600 * time.Second,
		Gateway:      "192.168.1.1",
	}

	ing := MappingToIngress(m)
	if ing == nil {
		t.Fatal("MappingToIngress returned nil for valid Mapping")
	}
	if ing.GetSource() != genv1.IngressSource_INGRESS_SOURCE_UPNP {
		t.Errorf("source: want UPNP, got %v", ing.GetSource())
	}
	if ing.GetConfidence() != upnpConfidence {
		t.Errorf("confidence: want %d, got %d", upnpConfidence, ing.GetConfidence())
	}
	if ing.GetUdpPort() != 0 {
		t.Errorf("udp_port: want 0 for TCP mapping, got %d", ing.GetUdpPort())
	}
	if ing.GetId() != "upnp-198.51.100.20-443-tcp" {
		t.Errorf("id: want upnp-198.51.100.20-443-tcp, got %q", ing.GetId())
	}
}

func TestMappingToIngressNil(t *testing.T) {
	ing := MappingToIngress(nil)
	if ing != nil {
		t.Errorf("MappingToIngress(nil) should return nil, got %+v", ing)
	}
}

// mockMapper 是测试用的 portmap.Mapper 实现。
type mockMapper struct {
	udpMapping *portmap.Mapping
	udpErr     error
	tcpMapping *portmap.Mapping
	tcpErr     error
}

func (m *mockMapper) Map(_ context.Context, _ uint16, udp bool, _ time.Duration) (*portmap.Mapping, error) {
	if udp {
		return m.udpMapping, m.udpErr
	}
	return m.tcpMapping, m.tcpErr
}

func (m *mockMapper) Refresh(context.Context, *portmap.Mapping) error { return nil }
func (m *mockMapper) Unmap(context.Context, *portmap.Mapping) error   { return nil }

func TestPortmapDiscoverBoth(t *testing.T) {
	mapper := &mockMapper{
		udpMapping: &portmap.Mapping{
			ExternalIP:   "203.0.113.1",
			ExternalPort: 51820,
			TransportUDP: true,
		},
		tcpMapping: &portmap.Mapping{
			ExternalIP:   "203.0.113.1",
			ExternalPort: 443,
			TransportUDP: false,
		},
	}

	result, err := PortmapDiscover(context.Background(), mapper, 51820, 443)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("want 2 ingresses, got %d", len(result))
	}

	// 第一个应是 UDP。
	if result[0].GetUdpPort() != 51820 {
		t.Errorf("first ingress should be UDP with udp_port=51820, got %d", result[0].GetUdpPort())
	}
	// 第二个应是 TCP。
	if result[1].GetUdpPort() != 0 {
		t.Errorf("second ingress should be TCP with udp_port=0, got %d", result[1].GetUdpPort())
	}
}

func TestPortmapDiscoverPartial(t *testing.T) {
	mapper := &mockMapper{
		udpMapping: &portmap.Mapping{
			ExternalIP:   "203.0.113.1",
			ExternalPort: 51820,
			TransportUDP: true,
		},
		tcpErr: errors.New("tcp map failed"),
	}

	result, err := PortmapDiscover(context.Background(), mapper, 51820, 443)
	if err != nil {
		t.Fatalf("partial success should not return error, got: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("want 1 ingress (UDP only), got %d", len(result))
	}
	if result[0].GetUdpPort() != 51820 {
		t.Errorf("surviving ingress should be UDP, got udp_port=%d", result[0].GetUdpPort())
	}
}

func TestPortmapDiscoverAllFail(t *testing.T) {
	mapper := &mockMapper{
		udpErr: errors.New("udp map failed"),
		tcpErr: errors.New("tcp map failed"),
	}

	result, err := PortmapDiscover(context.Background(), mapper, 51820, 443)
	if result != nil {
		t.Errorf("all-fail should return nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("all-fail should return error")
	}
}
