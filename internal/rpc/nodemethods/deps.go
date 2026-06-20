// Package nodemethods implements Node-side JSON-RPC methods for the TUI.
//
// Dependencies are injected via function closures (Deps struct) rather than
// interfaces, because Node-side data comes from various local variables in
// runNode; closures are the most flexible injection mechanism.
package nodemethods

import (
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// IngressInfo describes a discovered ingress endpoint.
type IngressInfo struct {
	Host       string `json:"host"`
	Port       uint32 `json:"port"`
	Source     string `json:"source"`
	Confidence uint32 `json:"confidence"`
	NATType    string `json:"nat_type"`
}

// MappingInfo describes a single port-mapping entry (UPnP/NAT-PMP/PCP).
type MappingInfo struct {
	Protocol     string `json:"protocol"`
	ExternalIP   string `json:"external_ip"`
	ExternalPort uint16 `json:"external_port"`
	InternalPort uint16 `json:"internal_port"`
	Transport    string `json:"transport"`
	TTL          string `json:"ttl"`
	RenewIn      string `json:"renew_in"`
}

// PortmapStatusInfo summarises the portmap subsystem state.
type PortmapStatusInfo struct {
	Active       bool `json:"active"`
	ManagedCount int  `json:"managed_count"`
}

// ConnectionInfo describes a peer connection.
type ConnectionInfo struct {
	PeerID     string `json:"peer_id"`
	VIP        string `json:"vip"`
	PeerIP     string `json:"peer_ip"`
	InternalIP string `json:"internal_ip"`
	LinkType   string `json:"link_type"`
	RTTms      uint32 `json:"rtt_ms"`
	RTTValid   bool   `json:"rtt_valid"`
	Loss       uint32 `json:"loss_permille"`
	LossValid  bool   `json:"loss_valid"`
	State      string `json:"state"`
}

// RouteInfo describes a route to a destination.
type RouteInfo struct {
	Hops      []HopInfo `json:"hops"`
	TotalHops int       `json:"total_hops"`
	Active    bool      `json:"active"`
}

// HopInfo describes a single hop in a route.
type HopInfo struct {
	NodeID    string `json:"node_id"`
	IngressID string `json:"ingress_id"`
	Host      string `json:"host"`
	Port      uint32 `json:"port"`
}

// PeerInfo describes a known peer node.
type PeerInfo struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	VIP      string `json:"vip"`
}

// Deps aggregates all dependencies for Node RPC methods.
type Deps struct {
	NodeID        string
	VIP           string
	Role          func() string
	TopoVer       func() uint64
	TopoUpdatedAt func() time.Time
	Uptime        func() time.Duration
	Connected     func() bool
	Config        func() any
	LogBuffer     *rpc.LogBuffer // 可选，nil 时 system.logs 返回空

	Ingresses       func() []IngressInfo
	PortmapMappings func() []MappingInfo
	PortmapStatus   func() PortmapStatusInfo
	Connections     func() []ConnectionInfo
	Routes          func(dst string) []RouteInfo
	Peers           func() []PeerInfo

	// 调试接口（可选，nil 时 debug.* 方法不可用）。
	DebugBlockPeer   func(peerID string)
	DebugUnblockPeer func(peerID string)
	DebugListBlocked func() []string
	DebugMTR         func(target string, count int, via []string, replyMode string) (*MTRResult, error)
	DebugMTREnum     func(target string) (*MTREnumResult, error)
	DebugMTREnumAll  func() (*MTREnumAllResult, error)
}

// RegisterAll registers all Node RPC methods on the given server.
func RegisterAll(s *rpc.Server, deps Deps) {
	registerSystemMethods(s, deps)
	registerIngressMethods(s, deps)
	registerConnectionsMethods(s, deps)
	registerPortmapMethods(s, deps)
	registerRouteMethods(s, deps)
	registerConfigMethods(s, deps)
	registerDebugMethods(s, deps)
	registerMTRMethods(s, deps)
}
