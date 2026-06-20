package configsvc

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ListNodeAliases 实现 configStoreIface。
func (a *StoreAdapter) ListNodeAliases() ([]store.NodeAlias, error) {
	return a.st.ListNodeAliases()
}

// ListPublishedRoutes 实现 configStoreIface。
func (a *StoreAdapter) ListPublishedRoutes() ([]store.PublishedRoute, error) {
	return a.st.ListPublishedRoutes()
}

// GetDNSSettings 实现 configStoreIface。
func (a *StoreAdapter) GetDNSSettings() (*store.DNSSettings, error) {
	return a.st.GetDNSSettings()
}

// ListFreshDiscoveredMappings 实现 configStoreIface。
func (a *StoreAdapter) ListFreshDiscoveredMappings(now time.Time) ([]store.DiscoveredMapping, error) {
	return a.st.ListFreshDiscoveredMappings(now)
}

// ListSplitRules 实现 configStoreIface。
func (a *StoreAdapter) ListSplitRules() ([]store.SplitRuleRow, error) {
	return a.st.ListSplitRules()
}

// GetLatestGeoIPMeta 实现 configStoreIface。
func (a *StoreAdapter) GetLatestGeoIPMeta() (*store.GeoIPMeta, error) {
	return a.st.GetLatestGeoIPMeta()
}

// ListActiveCertFingerprints 实现 configStoreIface。
func (a *StoreAdapter) ListActiveCertFingerprints() (map[string]string, error) {
	return a.st.ListActiveCertFingerprints()
}

// StoreAdapter 将 *store.Store 适配为 configsvc 所需的各个接口。
// 这样 configsvc 包不直接依赖 store 包的具体类型（除 http.go 所需的 store 数据类型外）。
type StoreAdapter struct {
	st *store.Store
}

// NewStoreAdapter 构造 StoreAdapter。
func NewStoreAdapter(st *store.Store) *StoreAdapter {
	return &StoreAdapter{st: st}
}

// BumpGeneration 实现 storeBumper 接口（供 Notify 使用）。
func (a *StoreAdapter) BumpGeneration(nodeID string) (uint64, error) {
	return a.st.BumpGeneration(nodeID)
}

// GetNodeInfo 实现 NodeInfoGetter 接口（供 ConfigGRPC/ConfigWS 使用）。
func (a *StoreAdapter) GetNodeInfo(nodeID string) (*NodeInfo, error) {
	n, err := a.st.GetNode(nodeID)
	if err != nil {
		return nil, err
	}
	return &NodeInfo{Generation: n.Generation}, nil
}

// ListNodes 实现 configStoreIface（供 ConfigHTTP 使用）。
func (a *StoreAdapter) ListNodes() ([]store.Node, error) {
	return a.st.ListNodes()
}

// GetLatestACLPolicy 实现 configStoreIface。
func (a *StoreAdapter) GetLatestACLPolicy() (*store.ACLPolicy, error) {
	return a.st.GetLatestACLPolicy()
}

// ListRelayInfo 实现 configStoreIface。
func (a *StoreAdapter) ListRelayInfo() ([]store.RelayInfo, error) {
	return a.st.ListRelayInfo()
}

// GetNode 实现 configStoreIface。
func (a *StoreAdapter) GetNode(id string) (*store.Node, error) {
	return a.st.GetNode(id)
}

// ─── 聚合服务 ─────────────────────────────────────────────────────────────────

// Services 聚合 configsvc 所有组件，方便 main 组装。
type Services struct {
	Notify     *Notify
	ConfigGRPC *ConfigGRPC
	ConfigWS   *ConfigWS
	ConfigHTTP *ConfigHTTP

	// epoch 是当前控制平面纪元，各组件通过共享指针原子读取。
	// 零值（未调 SetEpoch）时保持恒 0，向后兼容。
	epoch atomic.Uint64
}

// CRLProviderFunc 是 crlProvider 接口的函数适配器（用于测试或简单注入）。
type CRLProviderFunc func(validFor time.Duration) ([]byte, error)

func (f CRLProviderFunc) CurrentCRL(validFor time.Duration) ([]byte, error) {
	return f(validFor)
}

// New 聚合构造所有 configsvc 组件。
//
// 参数：
//   - st：*store.Store（通过 StoreAdapter 适配）
//   - crl：提供 CurrentCRL 的接口（通常是 *ca.Manager）
//   - nodeRelayFn：nodeID→relayID 映射函数（可为 nil）
func New(st *store.Store, crl crlProvider, nodeRelayFn func() map[string]string) *Services {
	svc := &Services{}
	adapter := NewStoreAdapter(st)
	notify := NewNotify(adapter)
	grpcSvc := newConfigGRPCWithEpoch(notify, adapter, &svc.epoch)
	wsSvc := newConfigWSWithEpoch(notify, adapter, &svc.epoch)
	httpSvc := newConfigHTTPWithEpoch(adapter, crl, nodeRelayFn, &svc.epoch)
	svc.Notify = notify
	svc.ConfigGRPC = grpcSvc
	svc.ConfigWS = wsSvc
	svc.ConfigHTTP = httpSvc
	return svc
}

// SetEpoch 动态更新当前控制平面纪元（并发安全）。
// 未调用时 epoch 为 0，与阶段1行为完全兼容。
func (s *Services) SetEpoch(epoch uint64) {
	s.epoch.Store(epoch)
}

// Epoch 返回当前 epoch 值（并发安全）。
func (s *Services) Epoch() uint64 {
	return s.epoch.Load()
}

// SetAssignmentFn 在聚合服务上注入可选的拓扑分配函数（转发给 ConfigHTTP）。
func (s *Services) SetAssignmentFn(fn func(nodeID string) *genv1.TopologyAssignment) {
	s.ConfigHTTP.SetAssignmentFn(fn)
}

// SetIngressFn 在聚合服务上注入入口查询函数（全连接 fallback 用）。
func (s *Services) SetIngressFn(fn func(nodeID string) (*genv1.IngressSet, bool)) {
	s.ConfigHTTP.SetIngressFn(fn)
}

// SetGeoLookupFn 在聚合服务上注入 GeoIP 查询函数。
func (s *Services) SetGeoLookupFn(fn func(code string) []string) {
	s.ConfigHTTP.SetGeoLookupFn(fn)
}

// HTTPHandler 返回挂载到 /v1/config 的 HTTP handler。
func (s *Services) HTTPHandler() http.Handler {
	return s.ConfigHTTP
}

// WSHandler 返回挂载到 /v1/watch 的 WebSocket handler。
func (s *Services) WSHandler() http.Handler {
	return s.ConfigWS
}

// GeoIPHandler 返回挂载到 /v1/geoip 的 HTTP handler（下发 geoip.dat 文件）。
func (s *Services) GeoIPHandler() http.HandlerFunc {
	return s.ConfigHTTP.handleServeGeoIP
}
