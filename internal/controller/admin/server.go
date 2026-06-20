package admin

import (
	"net/http"

	"github.com/x6nux/corelink/internal/controller/store"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── 依赖接口（便于测试注入 mock）─────────────────────────────────────────────

// StoreIface 是管理面对 store 的依赖。实际传 *store.Store 即满足。
type StoreIface interface {
	ListNodes() ([]store.Node, error)
	GetNode(id string) (*store.Node, error)
	GetNodeByName(name string) (*store.Node, error)
	ResolveNode(hint string) (*store.Node, error)
	UpdateNodeMeta(id, name, remark string) error
	DeleteNode(id string) error
	GetLeasesByNode(nodeID string) ([]store.Lease, error)

	ListEnrollKeys() ([]store.EnrollKey, error)
	CreateEnrollKey(ek *store.EnrollKey) error
	GetEnrollKey(key string) (*store.EnrollKey, error)
	RevokeEnrollKey(key string) error

	ListCerts() ([]store.Cert, error)
	ListCertsByNode(nodeID string) ([]store.Cert, error)

	GetLatestACLPolicy() (*store.ACLPolicy, error)
	ListACLPolicies() ([]store.ACLPolicy, error)
	SaveACLPolicy(doc, author string) (*store.ACLPolicy, error)

	ListRelayInfo() ([]store.RelayInfo, error)
	ListRelayLinks() ([]store.RelayLink, error)
	SetRelayLinks(relayID string, neighborIDs []string) error

	// Node alias / route / DNS
	CreateNodeAlias(a *store.NodeAlias) error
	ListNodeAliases() ([]store.NodeAlias, error)
	DeleteNodeAlias(id uint) error
	CreatePublishedRoute(r *store.PublishedRoute) error
	ListAllPublishedRoutes() ([]store.PublishedRoute, error)
	SetPublishedRouteEnabled(id uint, enabled bool) error
	DeletePublishedRoute(id uint) error
	UpsertDNSSettings(d *store.DNSSettings) error
	GetDNSSettings() (*store.DNSSettings, error)

	// Split tunnel
	CreateSplitRule(r *store.SplitRuleRow) error
	ListSplitRules() ([]store.SplitRuleRow, error)
	DeleteSplitRule(id uint) error
	SetSplitRuleEnabled(id uint, enabled bool) error
	ReorderSplitRule(id uint, newOrder uint32) error
	UpsertGeoIPMeta(m *store.GeoIPMeta) error
	GetLatestGeoIPMeta() (*store.GeoIPMeta, error)
}

// CAIface 是管理面对 CA 管理器的依赖。
type CAIface interface {
	Revoke(serial string) error
	CACertPEM() ([]byte, error)
	CAPublicKeyHash() (string, error)
}

// IPAMIface 是管理面对 IPAM 的依赖。
type IPAMIface interface {
	Release(ip string) error
}

// OnlineIface 报告节点在线状态（通常是 *configsvc.Notify）。
type OnlineIface interface {
	IsOnline(nodeID string) bool
}

// NotifyIface 触发配置重算下发（通常是 *configsvc.Notify）。
type NotifyIface interface {
	RecomputeAndNotify(nodeIDs ...string)
}

// RosterIface 是管理面对 relayroster 的依赖。
type RosterIface interface {
	Topology() (map[string][]string, error)
}

// TopologyIface 提供路由/定位只读视图 + 手动修正（通常由 *ingress.Receiver 实现）。
type TopologyIface interface {
	AllNodeGeo() []*genv1.NodeGeo
	AllRouteReports() []*genv1.RouteReport
	SetNodeGeo(*genv1.NodeGeo)
}

// Deps 聚合管理面 server 的全部依赖。
type Deps struct {
	Auth     *Authenticator
	Store    StoreIface
	CA       CAIface
	IPAM     IPAMIface
	Online   OnlineIface
	Notify   NotifyIface
	Roster   RosterIface
	Topology TopologyIface
}

// Server 是管理面 HTTP 服务（API + 认证中间件；SPA 静态留 P4）。
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// NewAdminServer 装配管理 HTTP 路由：/admin/api/login 公开，其余资源经 RequireAuth。
func NewAdminServer(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ServeHTTP 实现 http.Handler。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// routes 注册所有路由。
func (s *Server) routes() {
	// 公开端点：登录。
	s.mux.HandleFunc("POST /admin/api/login", s.handleLogin)
	s.mux.HandleFunc("POST /admin/api/logout", s.handleLogout)

	// 受保护资源：用 RequireAuth 包裹。
	protected := http.NewServeMux()

	// nodes
	protected.HandleFunc("GET /admin/api/nodes", s.handleListNodes)
	protected.HandleFunc("GET /admin/api/nodes/{id}", s.handleGetNode)
	protected.HandleFunc("PATCH /admin/api/nodes/{id}", s.handlePatchNode)
	protected.HandleFunc("DELETE /admin/api/nodes/{id}", s.handleDeleteNode)

	// acl
	protected.HandleFunc("GET /admin/api/acl", s.handleGetACL)
	protected.HandleFunc("PUT /admin/api/acl", s.handlePutACL)
	protected.HandleFunc("GET /admin/api/acl/history", s.handleACLHistory)
	protected.HandleFunc("POST /admin/api/acl/preview", s.handleACLPreview)

	// keys
	protected.HandleFunc("GET /admin/api/keys", s.handleListKeys)
	protected.HandleFunc("POST /admin/api/keys", s.handleCreateKey)
	protected.HandleFunc("DELETE /admin/api/keys/{key}", s.handleRevokeKey)

	// relays
	protected.HandleFunc("GET /admin/api/relays", s.handleListRelays)
	protected.HandleFunc("PUT /admin/api/relays/topology", s.handleSetTopology)

	// certs / ca
	protected.HandleFunc("GET /admin/api/certs", s.handleListCerts)
	protected.HandleFunc("POST /admin/api/certs/{serial}/revoke", s.handleRevokeCert)
	protected.HandleFunc("GET /admin/api/ca", s.handleGetCA)

	// node aliases
	protected.HandleFunc("GET /admin/api/node-aliases", s.handleListAliases)
	protected.HandleFunc("POST /admin/api/node-aliases", s.handleCreateAlias)
	protected.HandleFunc("DELETE /admin/api/node-aliases/{id}", s.handleDeleteAlias)

	// routes
	protected.HandleFunc("GET /admin/api/routes", s.handleListRoutes)
	protected.HandleFunc("POST /admin/api/routes", s.handleCreateRoute)
	protected.HandleFunc("PATCH /admin/api/routes/{id}", s.handlePatchRoute)
	protected.HandleFunc("DELETE /admin/api/routes/{id}", s.handleDeleteRoute)

	// dns
	protected.HandleFunc("GET /admin/api/dns", s.handleGetDNS)
	protected.HandleFunc("PUT /admin/api/dns", s.handlePutDNS)

	// split-tunnel
	protected.HandleFunc("GET /admin/api/split-tunnel/rules", s.handleListSplitRules)
	protected.HandleFunc("POST /admin/api/split-tunnel/rules", s.handleCreateSplitRule)
	protected.HandleFunc("DELETE /admin/api/split-tunnel/rules/{id}", s.handleDeleteSplitRule)
	protected.HandleFunc("PUT /admin/api/split-tunnel/rules/{id}/reorder", s.handleReorderSplitRule)
	protected.HandleFunc("GET /admin/api/split-tunnel/status", s.handleSplitTunnelStatus)
	protected.HandleFunc("GET /admin/api/geoip/status", s.handleGeoIPStatus)

	// topology（节点定位 + 路由选路聚合视图）
	protected.HandleFunc("GET /admin/api/topology", s.handleGetTopology)
	protected.HandleFunc("PUT /admin/api/nodes/{id}/geo", s.handleSetNodeGeo)

	s.mux.Handle("/admin/api/", s.deps.Auth.RequireAuth(protected))

	// SPA 兜底：非 /admin/api 路径先尝试静态文件，未命中则返回 index.html。
	s.mux.Handle("/", spaHandler())
}

// loginRequest 是登录请求体。
type loginRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// loginResponse 是登录响应体。
type loginResponse struct {
	Token string `json:"token"`
	User  string `json:"user"`
}
