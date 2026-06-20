// Package ctrlmethods implements Controller-side JSON-RPC methods for the TUI.
package ctrlmethods

import (
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/rpc"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// StoreIface is the store dependency surface for RPC methods.
type StoreIface interface {
	ListNodes() ([]store.Node, error)
	GetNode(id string) (*store.Node, error)
	DeleteNode(id string) error
	GetLeasesByNode(nodeID string) ([]store.Lease, error)

	ListEnrollKeys() ([]store.EnrollKey, error)
	CreateEnrollKey(ek *store.EnrollKey) error
	RevokeEnrollKey(key string) error

	ListCerts() ([]store.Cert, error)

	GetLatestACLPolicy() (*store.ACLPolicy, error)
	ListACLPolicies() ([]store.ACLPolicy, error)
	SaveACLPolicy(doc, author string) (*store.ACLPolicy, error)

	ListRelayInfo() ([]store.RelayInfo, error)
	ListRelayLinks() ([]store.RelayLink, error)
}

// CAIface is the CA dependency surface for RPC methods.
type CAIface interface {
	CACertPEM() ([]byte, error)
	CAPublicKeyHash() (string, error)
	Revoke(serial string) error
}

// OnlineIface reports node online status.
type OnlineIface interface {
	IsOnline(nodeID string) bool
}

// NotifyIface triggers config recomputation and push.
type NotifyIface interface {
	RecomputeAndNotify(nodeIDs ...string)
}

// TopoStatus holds topology summary information.
type TopoStatus struct {
	Version       uint64    `json:"version"`
	TransitCount  int       `json:"transit_count"`
	LeafCount     int       `json:"leaf_count"`
	LastRecompute time.Time `json:"last_recompute"`
}

// TopoIface is the topology dependency surface for RPC methods.
type TopoIface interface {
	AssignmentForNode(nodeID string) (*genv1.TopologyAssignment, bool)
	Status() TopoStatus
}

// IngressIface is the ingress dependency surface for RPC methods.
type IngressIface interface {
	GetIngressSet(nodeID string) (*genv1.IngressSet, bool)
	AllIngressSets() []*genv1.IngressSet
}

// ConfigSummary controller 运行配置摘要（脱敏后用于 TUI 展示）。
type ConfigSummary struct {
	DBDSN       string `json:"db_dsn"`
	ListenAddr  string `json:"listen_addr"`
	AdminAddr   string `json:"admin_addr"`
	VirtualCIDR string `json:"virtual_cidr"`
	TLSMode     string `json:"tls_mode"`
	CASubject   string `json:"ca_subject"`
	CAHash      string `json:"ca_hash"`
}

// Deps aggregates all dependencies for Controller RPC methods.
type Deps struct {
	Store     StoreIface
	CA        CAIface
	Online    OnlineIface
	Notify    NotifyIface
	Topo      TopoIface
	Ingress   IngressIface
	StartTime time.Time
	Version   string
	Config    *ConfigSummary // 可选，nil 时 config.status 返回空
	LogBuffer *rpc.LogBuffer // 可选，nil 时 system.logs 返回空
}

// RegisterAll registers all Controller RPC methods on the given server.
func RegisterAll(s *rpc.Server, deps Deps) {
	registerSystemMethods(s, deps)
	registerNodesMethods(s, deps)
	registerKeysMethods(s, deps)
	registerCertsMethods(s, deps)
	registerACLMethods(s, deps)
	registerTopoMethods(s, deps)
}
