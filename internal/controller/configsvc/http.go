package configsvc

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/x6nux/corelink/internal/controller/acl"
	"github.com/x6nux/corelink/internal/controller/routepolicy"
	"github.com/x6nux/corelink/internal/controller/store"
	version "github.com/x6nux/corelink/internal/version"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// configStoreIface 是 HTTP handler 需要的 store 接口。
type configStoreIface interface {
	ListNodes() ([]store.Node, error)
	GetLatestACLPolicy() (*store.ACLPolicy, error)
	ListRelayInfo() ([]store.RelayInfo, error)
	GetNode(id string) (*store.Node, error)
	ListNodeAliases() ([]store.NodeAlias, error)
	ListPublishedRoutes() ([]store.PublishedRoute, error)
	GetDNSSettings() (*store.DNSSettings, error)
	ListFreshDiscoveredMappings(now time.Time) ([]store.DiscoveredMapping, error)
	ListSplitRules() ([]store.SplitRuleRow, error)
	GetLatestGeoIPMeta() (*store.GeoIPMeta, error)
	ListActiveCertFingerprints() (map[string]string, error)
}

// crlProvider 提供当前 CRL DER 字节。
type crlProvider interface {
	CurrentCRL(validFor time.Duration) ([]byte, error)
}

// nodeRelayMapper 提供 nodeID → relayID 映射（来自 relayroster，可为 nil）。
type nodeRelayMapper interface {
	NodeRelay() map[string]string
}

// ConfigHTTP 提供 GET /v1/config 接口，返回完整 NodeConfig（protojson 编码）。
//
// mTLS 由外层 http.Server.TLSConfig 实现；nodeID 从 r.TLS PeerCertificates[0] CN 取。
// 支持 If-None-Match / generation 语义：客户端带本地 generation，相等返回 304。
type ConfigHTTP struct {
	st          configStoreIface
	crl         crlProvider
	nodeRelayFn func() map[string]string // 可为 nil

	// epoch 指向 Services.epoch（共享原子值），读取当前 控制平面纪元。
	// nil 时（旧构造路径）退化为恒 0，保持向后兼容。
	epoch *atomic.Uint64

	// assignmentFn 可选注入：按 nodeID 返回该节点的拓扑分配（智能并网，P4）。
	// nil（或返回 nil）时构造全连接 fallback（让节点先互联再探测质量）。
	assignmentFn func(nodeID string) *genv1.TopologyAssignment

	// ingressFn 可选注入：返回指定节点的入口集（用于全连接 fallback 构造邻居列表）。
	ingressFn func(nodeID string) (*genv1.IngressSet, bool)

	// geoLookupFn 可选注入：返回国家代码的 CIDR 列表（用于 geoip 规则展开）。
	geoLookupFn func(code string) []string
}

// NewConfigHTTP 构造 ConfigHTTP handler。
// nodeRelayFn 可选：返回 nodeID→relayID 映射；传 nil 则 route 中 ViaRelayId 为空。
//
// 拓扑注入（assignmentFn）通过 SetAssignmentFn 可选挂载，保持本构造签名兼容（既有
// 调用方与测试无需改动）。
// epoch 字段为 nil（退化恒 0，向后兼容），如需动态 epoch 请用 newConfigHTTPWithEpoch。
func NewConfigHTTP(st configStoreIface, crl crlProvider, nodeRelayFn func() map[string]string) *ConfigHTTP {
	return &ConfigHTTP{st: st, crl: crl, nodeRelayFn: nodeRelayFn}
}

// newConfigHTTPWithEpoch 是 New() 使用的内部构造函数，注入共享 epoch 指针。
func newConfigHTTPWithEpoch(st configStoreIface, crl crlProvider, nodeRelayFn func() map[string]string, epoch *atomic.Uint64) *ConfigHTTP {
	return &ConfigHTTP{st: st, crl: crl, nodeRelayFn: nodeRelayFn, epoch: epoch}
}

// loadEpoch 安全读取 epoch 值；epoch 指针为 nil 时返回 0（向后兼容）。
func (h *ConfigHTTP) loadEpoch() uint64 {
	if h.epoch == nil {
		return 0
	}
	return h.epoch.Load()
}

// SetAssignmentFn 注入可选的拓扑分配函数（P4 装配时由 TopoService.AssignmentForNode
// 提供）。传 nil 等价于不注入。非并发安全：应在起服务前一次性设置。
func (h *ConfigHTTP) SetAssignmentFn(fn func(nodeID string) *genv1.TopologyAssignment) {
	h.assignmentFn = fn
}

// SetIngressFn 注入入口查询函数（用于全连接 fallback）。
func (h *ConfigHTTP) SetIngressFn(fn func(nodeID string) (*genv1.IngressSet, bool)) {
	h.ingressFn = fn
}

// SetGeoLookupFn 注入 GeoIP 查询函数（用于 geoip 规则展开为 CIDR）。
func (h *ConfigHTTP) SetGeoLookupFn(fn func(code string) []string) {
	h.geoLookupFn = fn
}

// ServeHTTP 处理 GET /v1/config。
func (h *ConfigHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. 从 mTLS 证书提取 nodeID
	nodeID, err := NodeIDFromTLSCerts(r.TLS)
	if err != nil {
		slog.Warn("config http: mTLS 认证失败", "err", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. 构造快照并生成配置
	nodeConfig, err := h.buildNodeConfig(nodeID)
	if err != nil {
		slog.Error("config http: 构造配置失败", "node_id", nodeID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 3. 304 检测：用 (epoch, generation) 双维度比较
	// query 参数优先；若无 epoch query 参数则回退到 If-None-Match（仅 generation）。
	clientVer := version.ConfigVersion{
		Epoch:      extractClientUint(r, "epoch"),
		Generation: extractClientUint(r, "generation"),
	}
	serverVer := version.ConfigVersion{
		Epoch:      nodeConfig.Epoch,
		Generation: nodeConfig.Generation,
	}
	// clientVer.IsZero() 等价旧逻辑：generation=0 且无 epoch 时不触发 304
	if !clientVer.IsZero() && clientVer.Compare(serverVer) == 0 {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	// 向下兼容：客户端仅提供 If-None-Match（generation only，epoch 默认 0）
	if clientVer.IsZero() {
		clientGen := extractClientGeneration(r)
		if clientGen > 0 && clientGen == nodeConfig.Generation {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	// 4. protojson 编码响应
	marshaler := protojson.MarshalOptions{EmitUnpopulated: false}
	body, err := marshaler.Marshal(nodeConfig)
	if err != nil {
		slog.Error("config http: 序列化失败", "node_id", nodeID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", fmt.Sprintf("%d", nodeConfig.Generation))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// buildNodeConfig 构造指定节点的完整 NodeConfig。
func (h *ConfigHTTP) buildNodeConfig(nodeID string) (*genv1.NodeConfig, error) {
	// 查询当前节点（获取 generation、virtualIP 等）
	node, err := h.st.GetNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("GetNode 失败: %w", err)
	}

	// 列出所有节点
	nodes, err := h.st.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("ListNodes 失败: %w", err)
	}

	// 获取最新 ACL 策略
	policy, err := h.st.GetLatestACLPolicy()
	if err != nil {
		return nil, fmt.Errorf("GetLatestACLPolicy 失败: %w", err)
	}

	// 获取 relay 信息
	relayInfos, err := h.st.ListRelayInfo()
	if err != nil {
		return nil, fmt.Errorf("ListRelayInfo 失败: %w", err)
	}

	// 加载 alias/route/DNS/discovery 数据
	aliases, _ := h.st.ListNodeAliases()
	routes, _ := h.st.ListPublishedRoutes()
	dnsSettings, _ := h.st.GetDNSSettings()
	discovered, _ := h.st.ListFreshDiscoveredMappings(time.Now())

	// 调用 routepolicy resolver
	rpOut := routepolicy.Resolve(routepolicy.ResolveInput{
		Aliases:     aliases,
		Routes:      routes,
		Discovered:  discovered,
		DNSSettings: dnsSettings,
		Now:         time.Now(),
	})

	// 构造 acl.Snapshot（含 published prefixes）
	snap := buildSnapshot(nodes, policy, relayInfos, h.nodeRelayFn)
	snap.PublishedPrefixes = rpOut.PublishedPrefixes

	// 生成所有节点配置（纯函数）
	allConfigs := acl.Generate(snap)

	// 取当前节点的配置
	cfg, ok := allConfigs[nodeID]
	if !ok {
		cfg = &genv1.NodeConfig{
			VirtualIp: stripIPMask(node.VirtualIP),
		}
	}

	// 填充 generation 和 epoch
	cfg.Generation = node.Generation
	cfg.Epoch = h.loadEpoch()
	cfg.VirtualIp = stripIPMask(node.VirtualIP)

	// 填充 CRL
	crlDER, err := h.crl.CurrentCRL(24 * time.Hour)
	if err != nil {
		return nil, fmt.Errorf("CurrentCRL 失败: %w", err)
	}
	cfg.CrlDer = crlDER

	// 注入拓扑分配；若拓扑优化器尚未产出结果则构造全连接 fallback
	var asg *genv1.TopologyAssignment
	if h.assignmentFn != nil {
		asg = h.assignmentFn(nodeID)
	}
	if asg == nil {
		// 全连接 fallback：所有其他节点都是邻居，让节点自行建链探测
		asg = h.buildFullMeshFallback(nodeID)
	}
	if asg != nil {
		cfg.Topology = asg
	}

	// 全量指纹表：所有活跃节点的当前证书指纹（安全层，与拓扑解耦）
	if fps, err := h.st.ListActiveCertFingerprints(); err == nil && len(fps) > 0 {
		cfg.NodeFingerprints = fps
	}

	// 附加 routepolicy 解析结果
	if rpOut.DNSConfig != nil {
		cfg.Dns = rpOut.DNSConfig
	}
	// 只下发当前节点可见的 published prefixes
	for ownerID, prefixes := range rpOut.PublishedPrefixes {
		for _, prefix := range prefixes {
			cfg.PublishedPrefixes = append(cfg.PublishedPrefixes, &genv1.PublishedPrefix{
				Prefix:      prefix,
				OwnerNodeId: ownerID,
			})
		}
	}
	// 出口规则仅下发给出口 node
	if rules, ok := rpOut.EgressRules[nodeID]; ok {
		cfg.EgressRules = rules
	}
	// 发现配置仅下发给出口 node
	if configs, ok := rpOut.DiscoveryConfigs[nodeID]; ok {
		cfg.DiscoveryConfigs = configs
	}

	// 分流策略（仅当该节点有匹配的规则时才下发）
	// geoip:xx 规则在 controller 侧展开为 CIDR 列表直接下发，节点不需要 GeoIP 数据库。
	splitRules, _ := h.st.ListSplitRules()
	if len(splitRules) > 0 {
		var matchedRules []*genv1.SplitRule
		defaultAction := "direct"
		defaultExitNode := ""
		for _, r := range splitRules {
			if !r.Enabled {
				continue
			}
			if r.NodeID != "" && r.NodeID != nodeID {
				continue
			}
			// geoip:xx → 展开为 CIDR 列表
			if strings.HasPrefix(r.Match, "geoip:") && h.geoLookupFn != nil {
				code := strings.TrimPrefix(r.Match, "geoip:")
				negate := strings.HasPrefix(code, "!")
				if negate {
					code = code[1:]
				}
				cidrs := h.geoLookupFn(code)
				if negate {
					// geoip:!cn → cn CIDR 走 direct，默认 proxy
					for _, cidr := range cidrs {
						matchedRules = append(matchedRules, &genv1.SplitRule{
							Match:  "cidr:" + cidr,
							Action: "direct",
						})
					}
					defaultAction = "proxy"
					defaultExitNode = r.ExitNodeID
				} else {
					for _, cidr := range cidrs {
						matchedRules = append(matchedRules, &genv1.SplitRule{
							Match:      "cidr:" + cidr,
							Action:     r.Action,
							ExitNodeId: r.ExitNodeID,
						})
					}
				}
			} else {
				matchedRules = append(matchedRules, &genv1.SplitRule{
					Match:      r.Match,
					Action:     r.Action,
					ExitNodeId: r.ExitNodeID,
				})
			}
		}
		if len(matchedRules) > 0 {
			policy := &genv1.SplitTunnelPolicy{
				Enabled:           true,
				DefaultAction:     defaultAction,
				DefaultExitNodeId: defaultExitNode,
				Rules:             matchedRules,
			}
			cfg.SplitTunnel = policy
			slog.Info("configsvc: 分流策略已构建", "nodeID", nodeID, "rules", len(matchedRules), "default", defaultAction)
		}
	}

	return cfg, nil
}

// buildSnapshot 把 store 数据组装成 acl.Snapshot。
func buildSnapshot(
	nodes []store.Node,
	policy *store.ACLPolicy,
	relayInfos []store.RelayInfo,
	nodeRelayFn func() map[string]string,
) acl.Snapshot {
	// 解析 ACL 策略
	var aclPolicy *acl.Policy
	if policy != nil && policy.Document != "" {
		p, err := acl.ParsePolicy([]byte(policy.Document))
		if err == nil {
			aclPolicy = p
		}
	}

	// 构造 NodeView 列表
	nodeViews := make([]acl.NodeView, 0, len(nodes))
	for _, n := range nodes {
		nodeViews = append(nodeViews, acl.NodeView{
			ID:        n.ID,
			User:      n.User,
			Tags:      nil, // store.Node 暂无 tag 列，留空
			WGPubKey:  n.WGPubKey,
			VirtualIP: n.VirtualIP,
		})
	}

	// 构造 RelayView 列表——解析 store.RelayInfo 的端点字符串为 proto Endpoint。
	relayViews := make([]acl.RelayView, 0, len(relayInfos))
	for _, ri := range relayInfos {
		rv := acl.RelayView{
			ID:       ri.NodeID,
			Priority: ri.Priority,
		}
		if ri.TunnelEndpoint != "" {
			rv.Endpoint = parseEndpoint(ri.TunnelEndpoint)
		}
		if ri.UDPEndpoint != "" {
			rv.UDP = parseEndpoint(ri.UDPEndpoint)
		}
		if ri.Protocols != "" {
			for _, s := range strings.Split(ri.Protocols, ",") {
				if v, ok := genv1.TunnelProtocol_value[strings.TrimSpace(s)]; ok {
					rv.Tunnels = append(rv.Tunnels, genv1.TunnelProtocol(v))
				}
			}
		}
		relayViews = append(relayViews, rv)
	}

	// 构造 nodeRelay 映射
	var nodeRelay map[string]string
	if nodeRelayFn != nil {
		nodeRelay = nodeRelayFn()
	}

	return acl.Snapshot{
		Policy:    aclPolicy,
		Nodes:     nodeViews,
		Relays:    relayViews,
		NodeRelay: nodeRelay,
	}
}

// extractClientUint 从请求 query 参数中按 name 提取 uint64 值。
// 返回 0 表示未提供或无效。
func extractClientUint(r *http.Request, name string) uint64 {
	if s := r.URL.Query().Get(name); s != "" {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			return v
		}
	}
	return 0
}

// extractClientGeneration 从请求 query 参数 "generation" 或 header "If-None-Match" 提取客户端已知 generation。
// 返回 0 表示未提供或无效。query 优先于 If-None-Match。
func extractClientGeneration(r *http.Request) uint64 {
	// 优先从 query 参数取
	if v := extractClientUint(r, "generation"); v != 0 {
		return v
	}
	// 回退到 If-None-Match header（ETag 格式为纯数字）
	if etag := r.Header.Get("If-None-Match"); etag != "" {
		if v, err := strconv.ParseUint(etag, 10, 64); err == nil {
			return v
		}
	}
	return 0
}

// stripIPMask 去掉 "/32" 后缀，返回纯 IP 字符串。
func stripIPMask(ip string) string {
	for i := len(ip) - 1; i >= 0; i-- {
		if ip[i] == '/' {
			return ip[:i]
		}
	}
	return ip
}

// parseEndpoint 把 "host:port" 字符串解析为 genv1.Endpoint。
func parseEndpoint(s string) *genv1.Endpoint {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return nil
	}
	port, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return nil
	}
	return &genv1.Endpoint{Host: host, Port: uint32(port)}
}

// handleServeGeoIP 处理 GET /v1/geoip，下发 geoip.dat 文件给节点。
func (h *ConfigHTTP) handleServeGeoIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	meta, err := h.st.GetLatestGeoIPMeta()
	if err != nil || meta == nil {
		http.Error(w, "GeoIP 数据不可用", http.StatusNotFound)
		return
	}
	// 支持 If-None-Match：客户端带已有 SHA256，匹配则 304
	if etag := r.Header.Get("If-None-Match"); etag == meta.SHA256 {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", meta.SHA256)
	http.ServeFile(w, r, meta.FilePath)
}

// buildFullMeshFallback 在无拓扑分配时构造全连接拓扑——
// 所有其他节点都是邻居，附带各自已上报的入口地址。
// 让节点先互联、探测质量、上报数据后，拓扑优化器再接管。
func (h *ConfigHTTP) buildFullMeshFallback(selfID string) *genv1.TopologyAssignment {
	nodes, err := h.st.ListNodes()
	if err != nil || len(nodes) <= 1 {
		return nil
	}

	var neighbors []*genv1.NeighborRef
	for _, n := range nodes {
		if n.ID == selfID {
			continue
		}
		nb := &genv1.NeighborRef{NodeId: n.ID}
		// 从已上报的 ingress 集取入口地址
		if h.ingressFn != nil {
			if set, ok := h.ingressFn(n.ID); ok && set != nil {
				nb.Ingresses = set.GetIngresses()
			}
		}
		neighbors = append(neighbors, nb)
	}

	if len(neighbors) == 0 {
		return nil
	}

	return &genv1.TopologyAssignment{
		Role:      genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF,
		Neighbors: neighbors,
	}
}
