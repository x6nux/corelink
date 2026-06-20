package ingress

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/portmap"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// MappingToIngress 把 portmap.Mapping 转换为 genv1.Ingress（source=UPNP, confidence=upnpConfidence）。
// nil Mapping 返回 nil（不 panic）。
func MappingToIngress(m *portmap.Mapping) *genv1.Ingress {
	if m == nil {
		return nil
	}

	proto := "tcp"
	if m.TransportUDP {
		proto = "udp"
	}

	ing := &genv1.Ingress{
		Id:         fmt.Sprintf("upnp-%s-%d-%s", m.ExternalIP, m.ExternalPort, proto),
		Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
		Host:       m.ExternalIP,
		Port:       uint32(m.ExternalPort),
		Source:     genv1.IngressSource_INGRESS_SOURCE_UPNP,
		Confidence: upnpConfidence,
	}

	if m.TransportUDP {
		ing.UdpPort = uint32(m.ExternalPort)
	}

	return ing
}

// PortmapDiscover 是 PortmapFn 的默认实现：调 mapper.Map 获取 UDP+TCP 映射 → 转 Ingress 列表。
//
// 任一 Map 失败不影响另一个（err → 跳过该路，不 panic）。两都失败 → 返回 nil, err。
func PortmapDiscover(ctx context.Context, mapper portmap.Mapper, internalUDPPort, internalTCPPort uint16) ([]*genv1.Ingress, error) {
	const ttl = 7200 * time.Second

	var result []*genv1.Ingress
	var errs []error

	// UDP 映射。
	udpMapping, err := mapper.Map(ctx, internalUDPPort, true, ttl)
	if err != nil {
		errs = append(errs, fmt.Errorf("udp: %w", err))
	} else {
		result = append(result, MappingToIngress(udpMapping))
	}

	// TCP 映射。
	tcpMapping, err := mapper.Map(ctx, internalTCPPort, false, ttl)
	if err != nil {
		errs = append(errs, fmt.Errorf("tcp: %w", err))
	} else {
		result = append(result, MappingToIngress(tcpMapping))
	}

	if len(result) == 0 {
		return nil, errors.Join(errs...)
	}
	return result, nil
}
