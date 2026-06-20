// orchestrate.go 收纳 corelink-node 的智能并网编排逻辑（可测纯函数 + 子系统装配器）。
//
// 设计目标：把 runNode 的关键决策点拆成可注入依赖的纯函数与装配函数，
// 便于单元测试在不接触真实网络 / 真实数据面 / 真 controller 的前提下断言：
//   - 角色判定（roleFromConfig）正确读取 topology.Role；
//   - baseline 路由转换（assignmentToBaseline）正确把 Route2.Hops → mesh.Hop 序列；
//   - 入口发现选项装配（buildIngressOptions）正确接线各 fn；
//   - 角色装配分支（setupLeaf / setupTransit）按 topology 正确选择子系统。
package main

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/x6nux/corelink/internal/featureflag"
	agentconfig "github.com/x6nux/corelink/internal/nodecore/config"
	"github.com/x6nux/corelink/internal/nodecore/ingress"
	"github.com/x6nux/corelink/internal/nodecore/multirelay"
	"github.com/x6nux/corelink/internal/relay/mesh"
	tfib "github.com/x6nux/corelink/internal/transport/fib"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─────────────────────── 角色判定 ───────────────────────

// roleFromConfig 从 NodeConfig 读取本节点被分配的拓扑角色。
//
// 无 topology（S5 兼容场景）或 role 未指定时返回 NODE_TOPO_ROLE_UNSPECIFIED，
// 调用方据此退化为基础 agent（单 relay 接入）。
func roleFromConfig(nc *genv1.NodeConfig) genv1.NodeTopoRole {
	if nc == nil {
		return genv1.NodeTopoRole_NODE_TOPO_ROLE_UNSPECIFIED
	}
	topo := nc.GetTopology()
	if topo == nil {
		return genv1.NodeTopoRole_NODE_TOPO_ROLE_UNSPECIFIED
	}
	return topo.GetRole()
}

// ─────────────────────── baseline 路由转换 ───────────────────────

// assignmentToBaseline 把 TopologyAssignment.BaselineRoutes 转换为
// 「按目的节点分组的 K 条 mesh.Hop 路径」。
//
// SessionRouter 是 per-dst 的（每个目的中转 dstRelay 对应一组 K 基准路由），
// 故输出 map[dstNode][][]mesh.Hop：每个 dst 下挂若干条路径，每条路径是 Hop 序列。
//
// Route2.Hops（[]*genv1.Hop{NodeId,IngressId}）→ []mesh.Hop{Node,Ingress}。
// 同一 DstNode 的多个 Route2 视为该 dst 的多条基准路由（K 路）。
// nil / 空输入返回空 map（非 nil，调用方无需判空）。
func assignmentToBaseline(asg *genv1.TopologyAssignment) map[string][][]mesh.Hop {
	out := make(map[string][][]mesh.Hop)
	if asg == nil {
		return out
	}
	for _, r := range asg.GetBaselineRoutes() {
		if r == nil {
			continue
		}
		dst := r.GetDstNode()
		hops := make([]mesh.Hop, 0, len(r.GetHops()))
		for _, h := range r.GetHops() {
			if h == nil {
				continue
			}
			hops = append(hops, mesh.Hop{
				Node:    h.GetNodeId(),
				Ingress: h.GetIngressId(),
			})
		}
		out[dst] = append(out[dst], hops)
	}
	return out
}

// ─────────────────────── FIB 路由应用（VIP 模式）───────────────────────

// applyFIBToRoute 将 FIBTable 应用到 FIBRoute，用于 VIP 路由模式。
//
// 遍历 FIBTable.Entries，把每条 (prefix, nextHops) 转换为 FIBRoute.UpdateFIB 调用。
// 无效前缀静默跳过（日志在上层处理）。nil fib 或 nil fr 安全返回 nil。
func applyFIBToRoute(fr *mesh.FIBRoute, fib *genv1.FIBTable) error {
	if fib == nil || fr == nil {
		return nil
	}
	for _, entry := range fib.GetEntries() {
		if entry == nil {
			continue
		}
		prefix, err := netip.ParsePrefix(entry.GetPrefix())
		if err != nil {
			continue // 跳过无效前缀
		}
		nhs := make([]tfib.NextHop, 0, len(entry.GetNextHops()))
		for _, nh := range entry.GetNextHops() {
			if nh == nil {
				continue
			}
			nhs = append(nhs, tfib.NextHop{
				PeerID:    nh.GetPeerId(),
				Weight:    nh.GetWeight(),
				IngressID: nh.GetIngressId(),
			})
		}
		fr.UpdateFIB(prefix, nhs)
	}
	return nil
}

// ─────────────────────── 入口发现选项装配 ───────────────────────

// buildIngressOptions 装配 ingress.Discover 所需的 DiscoverOptions。
//
// 各路探测 fn 由调用方注入（生产传 ingress.StunProbe / EnumInterfaces / QueryPublicIP
// 的闭包；测试传确定性 fake）。observed 是 controller 观察到的源地址（可空）。
// portmapFn 是端口映射入口发现函数（UPnP/NAT-PMP/PCP），nil 时跳过该路。
func buildIngressOptions(
	cfg *agentconfig.Config,
	nodeID string,
	observed *genv1.Endpoint,
	stunFn func(ctx context.Context) (string, uint32, genv1.NatType, error),
	netifFn func() []*genv1.Ingress,
	urlFn func(ctx context.Context) (string, error),
	portmapFn func(ctx context.Context) ([]*genv1.Ingress, error),
) ingress.DiscoverOptions {
	var cfgIngresses []*genv1.Ingress
	if cfg != nil {
		cfgIngresses = agentconfig.ConfigIngresses(cfg.Ingresses)
	}
	return ingress.DiscoverOptions{
		NodeID:          nodeID,
		ConfigIngresses: cfgIngresses,
		Observed:        observed,
		StunFn:          stunFn,
		NetifFn:         netifFn,
		UrlFn:           urlFn,
		PortmapFn:       portmapFn,
	}
}

// ─────────────────────── probe 目标展开 ───────────────────────

// probeTargetsFromConfig 把 TopologyAssignment.ProbeTargets 展开为
// 一组 probe.ProbeTarget（每个 (NodeId, IngressId) 一条）。
//
// 用于 L1 探测后台 goroutine 对下发的探测目标逐个探测 + 喂给 Reporter。
func probeTargetsFromConfig(asg *genv1.TopologyAssignment) []probeTarget {
	var out []probeTarget
	if asg == nil {
		return out
	}
	for _, pt := range asg.GetProbeTargets() {
		if pt == nil {
			continue
		}
		for _, ing := range pt.GetIngressIds() {
			out = append(out, probeTarget{NodeID: pt.GetNodeId(), IngressID: ing})
		}
	}
	return out
}

// probeTarget 是 probe.ProbeTarget 的本地镜像，避免编排层直接依赖 probe 包类型签名
// 用于测试断言（转换在 driveProbeLoop 处用 probe.ProbeTarget 完成）。
type probeTarget struct {
	NodeID    string
	IngressID string
}

// ─────────────────────── multirelay 入口解析 ───────────────────────

// ingressResolverFromAssignment 构造 multirelay.IngressResolver：把目标 relayID
// 解析为 TopologyAssignment.Neighbors 中该 relay 的选定入口拨号参数。
//
// 返回 nil 表示无 topology / 无邻居信息（调用方据此退回默认 RelayEndpoint 接入，兼容）。
func ingressResolverFromAssignment(asg *genv1.TopologyAssignment) multirelay.IngressResolver {
	if asg == nil || len(asg.GetNeighbors()) == 0 {
		return nil
	}
	// 预构造 relayID → 选定入口（取邻居的首个入口作为选定入口；多入口取首项确定性）。
	table := make(map[string]*multirelay.IngressEndpoint)
	for _, nb := range asg.GetNeighbors() {
		if nb == nil {
			continue
		}
		ings := nb.GetIngresses()
		if len(ings) == 0 {
			continue
		}
		ing := ings[0]
		isCDN := ing.GetKind() == genv1.IngressKind_INGRESS_KIND_CDN
		ep := &multirelay.IngressEndpoint{
			IngressID: ing.GetId(),
			Addr:      addrOf(ing.GetHost(), ing.GetPort()),
			IsCDN:     isCDN,
		}
		if isCDN {
			ep.SNI = ing.GetSni()
		}
		table[nb.GetNodeId()] = ep
	}
	if len(table) == 0 {
		return nil
	}
	return func(relayID string) *multirelay.IngressEndpoint {
		return table[relayID]
	}
}

// ─────────────────────── interconnect 邻居入口地址表 ───────────────────────

// peerIngressAddrsFromAssignment 从 neighbors 构造 interconnect 的
// PeerIngressAddrs 表（neighbor → ingressID → addr）+ PeerIngressSNI 表。
//
// 供 TRANSIT 角色装配 interconnect 时按入口建互联链路。
func peerIngressAddrsFromAssignment(asg *genv1.TopologyAssignment, nc *genv1.NodeConfig) (addrs, sni map[string]map[string]string) {
	addrs = make(map[string]map[string]string)
	sni = make(map[string]map[string]string)
	if asg == nil {
		return addrs, sni
	}

	// 构建 relayID → 互联端点映射（来自 NodeConfig.Relays）。
	// 优先用 UDP endpoint（实际存的是 interconnect 监听端口，帧格式匹配 mesh 互联），
	// 退而用 tunnel endpoint（relay stream 端口，仅作兜底）。
	relayEP := make(map[string]string) // relayID → "host:port"
	for _, r := range nc.GetRelays() {
		// 优先 UDP endpoint（= interconnect 端口）
		if u := r.GetUdp(); u != nil && u.GetHost() != "" && u.GetPort() > 0 {
			relayEP[r.GetRelayId()] = addrOf(u.GetHost(), u.GetPort())
			continue
		}
		// 退而 tunnel endpoint（= relay stream 端口）
		if t := r.GetTunnel(); t != nil && t.GetHost() != "" && t.GetPort() > 0 {
			relayEP[r.GetRelayId()] = addrOf(t.GetHost(), t.GetPort())
		}
	}

	for _, nb := range asg.GetNeighbors() {
		if nb == nil {
			continue
		}
		nbID := nb.GetNodeId()
		added := false
		for _, ing := range nb.GetIngresses() {
			if ing == nil || ing.GetId() == "" {
				continue
			}
			addr := addrOf(ing.GetHost(), ing.GetPort())
			if ing.GetHost() != "" && ing.GetPort() == 0 {
				// ingress 有 IP 但没端口（如 NETIF 的 LAN/公网 IP）：
				// 保留 ingress 自身 Host，从 relay endpoint 借端口。
				// 确保同内网 LAN IP 入口不被公网 relay 地址覆盖。
				if ep, ok := relayEP[nbID]; ok {
					if _, port, err := net.SplitHostPort(ep); err == nil && port != "" {
						addr = net.JoinHostPort(ing.GetHost(), port)
					} else {
						addr = ep
					}
				}
			} else if addr == "" {
				if ep, ok := relayEP[nbID]; ok {
					addr = ep
				}
			}
			if addr == "" {
				continue
			}
			if addrs[nbID] == nil {
				addrs[nbID] = make(map[string]string)
			}
			addrs[nbID][ing.GetId()] = addr
			added = true
			if ing.GetKind() == genv1.IngressKind_INGRESS_KIND_CDN && ing.GetSni() != "" {
				if sni[nbID] == nil {
					sni[nbID] = make(map[string]string)
				}
				sni[nbID][ing.GetId()] = ing.GetSni()
			}
		}
		// 如果 ingress 全都没有可用地址，直接用 relay endpoint 作为默认入口。
		if !added {
			if ep, ok := relayEP[nbID]; ok && ep != "" {
				if addrs[nbID] == nil {
					addrs[nbID] = make(map[string]string)
				}
				addrs[nbID]["_relay_default"] = ep
			}
		}
	}
	return addrs, sni
}

// ─────────────────────── 角色装配分支 ───────────────────────

// subsystemAssembler 抽象「按角色装配子系统」的装配动作，便于单测注入 fake 验证
// 分支选择，而不必真起数据面 / relay server / 互联监听。
//
// 生产实现（realAssembler）在 main.go 中接通真实子系统（node-core + relay server +
// interconnect + multirelay）。测试实现记录被调用的分支与参数。
type subsystemAssembler interface {
	// SetupLeaf 装配 LEAF 角色：node-core（DataPlane+TUN）+ multirelay 按选定入口接入。
	SetupLeaf(ctx context.Context, p leafParams) error
	// SetupTransit 装配 TRANSIT 角色：relay server + interconnect + 内嵌 node-core。
	SetupTransit(ctx context.Context, p transitParams) error
	// SetupBasicAgent 装配退化基础 agent（无 topology，S5 单 relay 接入）。
	SetupBasicAgent(ctx context.Context, p basicParams) error
}

// leafParams 是 LEAF 角色装配参数。
type leafParams struct {
	NodeID          string
	Candidates      []*genv1.RelayEndpoint
	IngressResolver multirelay.IngressResolver
	FirstConfig     *genv1.NodeConfig
}

// transitParams 是 TRANSIT 角色装配参数。
type transitParams struct {
	NodeID string
	// Baseline 是按 dst 分组的 K 基准路由（SetBaseline 用）。
	Baseline map[string][][]mesh.Hop
	// FIB 是 VIP 路由模式下的转发信息库（与 Baseline 互斥使用）。
	FIB *genv1.FIBTable
	// Flags 是 feature flag 集合（VIP 路由判定用）。
	Flags *featureflag.Flags
	// PeerIngressAddrs / PeerIngressSNI 是互联按入口建链表。
	PeerIngressAddrs map[string]map[string]string
	PeerIngressSNI   map[string]map[string]string
	// NeighborIDs 是确定性排序的邻居 ID 列表（建链顺序）。
	NeighborIDs []string
	// PeerFingerprints 是 nodeID→证书指纹映射（A6），供 mesh.InterconnectConfig 注入 pin（A7）。
	PeerFingerprints map[string]string
	FirstConfig      *genv1.NodeConfig
}

// basicParams 是退化基础 agent 装配参数。
type basicParams struct {
	NodeID      string
	FirstConfig *genv1.NodeConfig
}

// assembleByRole 按 NodeConfig 中的拓扑角色选择装配分支并调用对应装配器方法。
//
// 这是「角色 → 子系统」的纯决策逻辑（无副作用，副作用全在注入的 assembler 中），
// 单测据此断言：TRANSIT → SetupTransit；LEAF → SetupLeaf；UNSPECIFIED/无 topology
// → SetupBasicAgent。
func assembleByRole(ctx context.Context, a subsystemAssembler, nodeID string, nc *genv1.NodeConfig, flags ...*featureflag.Flags) error {
	role := roleFromConfig(nc)
	asg := nc.GetTopology()
	var ff *featureflag.Flags
	if len(flags) > 0 {
		ff = flags[0]
	}
	switch role {
	case genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT:
		addrs, sni := peerIngressAddrsFromAssignment(asg, nc)
		return a.SetupTransit(ctx, transitParams{
			NodeID:           nodeID,
			Baseline:         assignmentToBaseline(asg),
			FIB:              asg.GetFib(),
			Flags:            ff,
			PeerIngressAddrs: addrs,
			PeerIngressSNI:   sni,
			NeighborIDs:      sortedNeighborIDs(asg),
			PeerFingerprints: peerFingerprintsFromAssignment(asg),
			FirstConfig:      nc,
		})
	case genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF:
		return a.SetupLeaf(ctx, leafParams{
			NodeID:          nodeID,
			Candidates:      nc.GetRelays(),
			IngressResolver: ingressResolverFromAssignment(asg),
			FirstConfig:     nc,
		})
	default:
		return a.SetupBasicAgent(ctx, basicParams{
			NodeID:      nodeID,
			FirstConfig: nc,
		})
	}
}

// ─────────────────────── 辅助 ───────────────────────

// addrOf 把 host + port 拼成 "host:port"（port 为 0 时仅返回 host）。
// 使用 net.JoinHostPort 确保 IPv6 地址正确加括号（如 [::1]:7447）。
func addrOf(host string, port uint32) string {
	if port == 0 {
		return host
	}
	return net.JoinHostPort(host, uitoa(port))
}

// uitoa 把 uint32 转十进制字符串（无 fmt 依赖，热路径友好）。
func uitoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// sortedNeighborIDs 返回 assignment 中邻居 ID 的确定性排序列表（装配 interconnect 时
// 用于稳定遍历建链顺序）。
func sortedNeighborIDs(asg *genv1.TopologyAssignment) []string {
	if asg == nil {
		return nil
	}
	ids := make([]string, 0, len(asg.GetNeighbors()))
	for _, nb := range asg.GetNeighbors() {
		if nb != nil && nb.GetNodeId() != "" {
			ids = append(ids, nb.GetNodeId())
		}
	}
	sort.Strings(ids)
	return ids
}

// ─────────────────────── 邻居指纹提取（A6）───────────────────────

// peerFingerprintsFromAssignment 从 TopologyAssignment 的邻居列表提取
// nodeID→证书指纹映射（A6）；空指纹跳过（A4 下发端对未签发节点留空）。
//
// 返回空 map（非 nil），调用方无需判空。
func peerFingerprintsFromAssignment(asg *genv1.TopologyAssignment) map[string]string {
	out := make(map[string]string)
	if asg == nil {
		return out
	}
	for _, nb := range asg.GetNeighbors() {
		if nb == nil {
			continue
		}
		if fp := nb.GetFingerprint(); fp != "" {
			out[nb.GetNodeId()] = fp
		}
	}
	return out
}

// ─────────────────────── 重报节流（reportGate）───────────────────────

// reportGate 提供 time-based 最小间隔节流，用于 OnMappingLost 触发重报时
// 防止路由器抖动引发上报风暴。
//
// 线程安全。Allow 返回 true 表示本次通过（距上次 >= minInterval），false 表示被合并/忽略。
type reportGate struct {
	mu          sync.Mutex
	minInterval time.Duration
	clock       func() time.Time
	lastAllow   time.Time
}

// newReportGate 创建 reportGate。interval 为最小重报间隔，clock 为时间源（默认 time.Now）。
func newReportGate(interval time.Duration, clock func() time.Time) *reportGate {
	if clock == nil {
		clock = time.Now
	}
	return &reportGate{
		minInterval: interval,
		clock:       clock,
	}
}

// Allow 判断当前是否允许通过：距上次 Allow=true 的时刻 >= minInterval 时返回 true
// 并记录本次时刻；否则返回 false（被节流）。
func (g *reportGate) Allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if g.lastAllow.IsZero() || now.Sub(g.lastAllow) >= g.minInterval {
		g.lastAllow = now
		return true
	}
	return false
}
