package acl

import (
	"fmt"
	"net"
	"sort"
	"strings"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── 快照输入类型 ─────────────────────────────────────────────────────────────

// Snapshot 是配置生成所需的全部输入（DB 状态快照）。
// Generate 是纯函数：相同 Snapshot → 相同输出。
type Snapshot struct {
	Policy            *Policy             // 当前生效策略；nil 等价于空策略（无任何 ACL）
	Nodes             []NodeView          // 所有节点（agent + relay）
	Relays            []RelayView         // relay 端点列表（用于生成每节点的 relay 列表）
	NodeRelay         map[string]string   // nodeID → 该节点当前接入的 relayID（来自 relayroster）
	PublishedPrefixes map[string][]string // ownerNodeID → []CIDR（该节点发布的可达前缀）
}

// NodeView 是节点的 ACL 视图（从 store.Node + 上层注入的 Tags 构造）。
type NodeView struct {
	ID        string
	User      string   // store.Node.User（enroll 时由 key.Tag 设置，参与 group 匹配）
	Tags      []string // 节点所属 tag（如 ["tag:server"]；store.Node 暂无 tag 列，由调用方注入）
	WGPubKey  string
	VirtualIP string // e.g. "100.64.1.2/32"
}

// RelayView 是 relay 端点的 ACL 视图（从 store.RelayInfo 构造）。
type RelayView struct {
	ID       string
	Endpoint *genv1.Endpoint // tunnel 端点（TLS/TCP 侧）
	UDP      *genv1.Endpoint // UDP 端点
	Tunnels  []genv1.TunnelProtocol
	Priority uint
}

// ─── 端口约束 ─────────────────────────────────────────────────────────────────

// portSet 表示端口约束。nil 表示所有端口（无限制）。
type portSet []uint16

func (ps portSet) isAll() bool { return ps == nil }

// mergePortSets 合并两个端口集（union），任一为 nil（全端口）则结果为 nil。
func mergePortSets(a, b portSet) portSet {
	if a.isAll() || b.isAll() {
		return nil
	}
	m := make(map[uint16]struct{}, len(a)+len(b))
	for _, p := range a {
		m[p] = struct{}{}
	}
	for _, p := range b {
		m[p] = struct{}{}
	}
	result := make(portSet, 0, len(m))
	for p := range m {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// ─── 主函数 ───────────────────────────────────────────────────────────────────

// Generate 是纯函数：接受 Snapshot，返回每节点的 NodeConfig。
// map 键为 NodeView.ID（node_id）。
//
// 算法（spec §6.2）：
//  1. 建立 user→groups、tag→members 索引（每节点归类到其所属 group/tag 集合）。
//  2. 对每条 ACL：展开 src/dst 到节点 ID 集合 + 端口约束，建立有向边：src→dst。
//  3. 每节点 N 的 peer 列表 = N→其他（出边目标）∪ 其他→N（入边来源）（双向可见性求并）。
//  4. 每 peer P 对应 AllowedIPs = [P.VirtualIP/32]（端口粒度到 IP，S2 不做子网裁剪）。
//  5. routes = 所有 peer 的 VirtualIP/32 → ViaRelayId（来自 NodeRelay）。
//  6. relays = 所有可用 relay，按 Priority 升序排列。
func Generate(snap Snapshot) map[string]*genv1.NodeConfig {
	result := make(map[string]*genv1.NodeConfig, len(snap.Nodes))

	// 空节点集直接返回
	if len(snap.Nodes) == 0 {
		return result
	}

	// 构建节点索引
	nodeByID := make(map[string]NodeView, len(snap.Nodes))
	for _, n := range snap.Nodes {
		nodeByID[n.ID] = n
	}

	// 构建全局 relay 列表（按 Priority 升序，确定性排序）
	sortedRelays := sortRelays(snap.Relays)
	relayEndpoints := buildRelayEndpoints(sortedRelays)

	// 空策略或无 ACL → 默认全 mesh 互通（所有节点互为 peer）
	if snap.Policy == nil || len(snap.Policy.ACLs) == 0 {
		for _, n := range snap.Nodes {
			var peers []*genv1.Peer
			for _, other := range snap.Nodes {
				if other.ID == n.ID {
					continue
				}
				allowedIPs := []string{stripMask(other.VirtualIP) + "/32"}
				if snap.PublishedPrefixes != nil {
					for _, prefix := range snap.PublishedPrefixes[other.ID] {
						allowedIPs = append(allowedIPs, prefix)
					}
				}
				peers = append(peers, &genv1.Peer{
					NodeId:      other.ID,
					WgPublicKey: other.WGPubKey,
					AllowedIps:  allowedIPs,
				})
			}
			result[n.ID] = &genv1.NodeConfig{
				VirtualIp: stripMask(n.VirtualIP),
				Peers:     peers,
				Relays:    relayEndpoints,
			}
		}
		return result
	}

	// ─── 步骤 1：建立 user→groups、tag→{nodeID} 索引 ─────────────────────────

	// userToGroups: user → []"group:x"（用于展开 group: 选择器）
	userToGroups := buildUserToGroups(snap.Policy.Groups)

	// tagToNodes: "tag:x" → set of nodeID（用于展开 tag: 选择器）
	tagToNodes := buildTagToNodes(snap.Nodes)

	// ─── 步骤 2：对每条 ACL 建立 srcNodeIDs×dstNodeIDs 有向边 ─────────────────

	// edges: srcNodeID → dstNodeID → portSet（nil=全端口）
	type edgeKey struct{ src, dst string }
	edges := make(map[edgeKey]portSet)

	addEdge := func(srcID, dstID string, ports portSet) {
		k := edgeKey{srcID, dstID}
		if existing, ok := edges[k]; ok {
			edges[k] = mergePortSets(existing, ports)
		} else {
			edges[k] = ports
		}
	}

	for _, rule := range snap.Policy.ACLs {
		if rule.Action != "accept" {
			continue
		}
		// 展开 src 到 nodeID 集
		srcIDs := expandSelector(rule.Src, snap.Nodes, userToGroups, tagToNodes, snap.Policy.Groups)
		// 展开 dst 到 nodeID + 端口约束
		dstPorts := expandDstWithPorts(rule.Dst, snap.Nodes, userToGroups, tagToNodes, snap.Policy.Groups)

		for srcID := range srcIDs {
			for dstID, ports := range dstPorts {
				if srcID == dstID {
					continue // 节点不需要将自己加入 peer 列表
				}
				addEdge(srcID, dstID, ports)
			}
		}
	}

	// ─── 步骤 3：双向可见性求并 ───────────────────────────────────────────────

	// peerPorts[nodeID][peerID] = 合并端口集
	peerPorts := make(map[string]map[string]portSet, len(snap.Nodes))
	for _, n := range snap.Nodes {
		peerPorts[n.ID] = make(map[string]portSet)
	}

	for e, ports := range edges {
		// e.src 可以看到 e.dst
		peerPorts[e.src][e.dst] = mergePortSets(peerPorts[e.src][e.dst], ports)
		// 双向：e.dst 也能看到 e.src（对称可见性）
		peerPorts[e.dst][e.src] = mergePortSets(peerPorts[e.dst][e.src], ports)
	}

	// ─── 步骤 4-6：为每节点生成 NodeConfig ────────────────────────────────────

	for _, n := range snap.Nodes {
		peers := buildPeers(n.ID, peerPorts[n.ID], nodeByID, snap.PublishedPrefixes)
		routes := buildRoutes(n.ID, peerPorts[n.ID], nodeByID, snap.NodeRelay)
		result[n.ID] = &genv1.NodeConfig{
			VirtualIp: stripMask(n.VirtualIP),
			Peers:     peers,
			Routes:    routes,
			Relays:    relayEndpoints,
		}
	}

	return result
}

// ─── 工具函数：索引构建 ───────────────────────────────────────────────────────

// buildUserToGroups 构建 user → []"group:x" 的倒排索引。
func buildUserToGroups(groups map[string][]string) map[string][]string {
	m := make(map[string][]string)
	for groupName, members := range groups {
		for _, member := range members {
			m[member] = append(m[member], groupName)
		}
	}
	return m
}

// buildTagToNodes 构建 "tag:x" → set{nodeID} 索引（来自 NodeView.Tags）。
func buildTagToNodes(nodes []NodeView) map[string]map[string]struct{} {
	m := make(map[string]map[string]struct{})
	for _, n := range nodes {
		for _, tag := range n.Tags {
			if m[tag] == nil {
				m[tag] = make(map[string]struct{})
			}
			m[tag][n.ID] = struct{}{}
		}
	}
	return m
}

// ─── 工具函数：选择器展开 ─────────────────────────────────────────────────────

// expandSelector 把 src 选择器列表展开为 nodeID 集合。
// 支持：user、"group:x"、"tag:x"、"*"。
func expandSelector(
	selectors []string,
	nodes []NodeView,
	userToGroups map[string][]string,
	tagToNodes map[string]map[string]struct{},
	groups map[string][]string,
) map[string]struct{} {
	result := make(map[string]struct{})
	for _, sel := range selectors {
		addNodesForSelector(sel, nodes, userToGroups, tagToNodes, groups, result)
	}
	return result
}

// expandDstWithPorts 把 dst 选择器列表展开为 nodeID → portSet。
func expandDstWithPorts(
	dsts []string,
	nodes []NodeView,
	userToGroups map[string][]string,
	tagToNodes map[string]map[string]struct{},
	groups map[string][]string,
) map[string]portSet {
	result := make(map[string]portSet)
	for _, d := range dsts {
		if d == "*:*" {
			// 所有节点，全端口
			for _, n := range nodes {
				result[n.ID] = mergePortSets(result[n.ID], nil)
			}
			continue
		}
		sel, rawPorts, _ := parseDstSpec(d)
		ports := parsePortSet(rawPorts)
		nodeSet := make(map[string]struct{})
		addNodesForSelector(sel, nodes, userToGroups, tagToNodes, groups, nodeSet)
		for nid := range nodeSet {
			result[nid] = mergePortSets(result[nid], ports)
		}
	}
	return result
}

// addNodesForSelector 把单个选择器匹配的节点加入 out。
func addNodesForSelector(
	sel string,
	nodes []NodeView,
	userToGroups map[string][]string,
	tagToNodes map[string]map[string]struct{},
	groups map[string][]string,
	out map[string]struct{},
) {
	switch {
	case sel == "*":
		for _, n := range nodes {
			out[n.ID] = struct{}{}
		}
	case strings.HasPrefix(sel, "group:"):
		members := groups[sel]
		memberSet := make(map[string]struct{}, len(members))
		for _, m := range members {
			memberSet[m] = struct{}{}
		}
		for _, n := range nodes {
			if _, ok := memberSet[n.User]; ok {
				out[n.ID] = struct{}{}
			}
		}
	case strings.HasPrefix(sel, "tag:"):
		for nid := range tagToNodes[sel] {
			out[nid] = struct{}{}
		}
	default:
		// user 名
		for _, n := range nodes {
			if n.User == sel {
				out[n.ID] = struct{}{}
			}
		}
	}
	_ = userToGroups // 备用：当前通过 groups 正向展开，不需要倒排
}

// parsePortSet 把 []string{"22","443"} 转为 portSet，nil 输入返回 nil（全端口）。
func parsePortSet(rawPorts []string) portSet {
	if len(rawPorts) == 0 {
		return nil // 无端口限制 = 全端口
	}
	ps := make(portSet, 0, len(rawPorts))
	for _, s := range rawPorts {
		s = strings.TrimSpace(s)
		if s == "" || s == "*" {
			return nil // 通配符 = 全端口
		}
		n, err := fmt.Sscan(s, new(int))
		if err != nil || n == 0 {
			continue
		}
		var port int
		fmt.Sscan(s, &port)
		if port >= 0 && port <= 65535 {
			ps = append(ps, uint16(port))
		}
	}
	if len(ps) == 0 {
		return nil
	}
	return ps
}

// ─── 工具函数：NodeConfig 字段构建 ────────────────────────────────────────────

// buildPeers 根据 peerMap 为节点 nodeID 构建 Peer 列表（确定性排序）。
// publishedPrefixes 可选：如果对端有已发布前缀，追加到 AllowedIPs。
func buildPeers(
	nodeID string,
	peerMap map[string]portSet,
	nodeByID map[string]NodeView,
	publishedPrefixes ...map[string][]string,
) []*genv1.Peer {
	if len(peerMap) == 0 {
		return nil
	}
	peerIDs := make([]string, 0, len(peerMap))
	for pid := range peerMap {
		peerIDs = append(peerIDs, pid)
	}
	sort.Strings(peerIDs)

	var pp map[string][]string
	if len(publishedPrefixes) > 0 {
		pp = publishedPrefixes[0]
	}

	peers := make([]*genv1.Peer, 0, len(peerIDs))
	for _, pid := range peerIDs {
		peerNode, ok := nodeByID[pid]
		if !ok {
			continue
		}
		allowedIP := normalizeToSlash32(peerNode.VirtualIP)
		allowed := []string{allowedIP}
		if pp != nil {
			for _, prefix := range pp[pid] {
				allowed = append(allowed, prefix)
			}
		}
		peers = append(peers, &genv1.Peer{
			NodeId:      pid,
			WgPublicKey: peerNode.WGPubKey,
			AllowedIps:  allowed,
		})
	}
	return peers
}

func dedupSort(s []string) {
	sort.Strings(s)
	j := 0
	for i := range s {
		if i == 0 || s[i] != s[i-1] {
			s[j] = s[i]
			j++
		}
	}
}

// buildRoutes 为节点 nodeID 构建路由表（peer VirtualIP/32 → ViaRelayId）。
func buildRoutes(
	nodeID string,
	peerMap map[string]portSet,
	nodeByID map[string]NodeView,
	nodeRelay map[string]string,
) []*genv1.Route {
	if len(peerMap) == 0 {
		return nil
	}
	peerIDs := make([]string, 0, len(peerMap))
	for pid := range peerMap {
		peerIDs = append(peerIDs, pid)
	}
	sort.Strings(peerIDs)

	routes := make([]*genv1.Route, 0, len(peerIDs))
	for _, pid := range peerIDs {
		peerNode, ok := nodeByID[pid]
		if !ok {
			continue
		}
		destCIDR := normalizeToSlash32(peerNode.VirtualIP)
		relayID := nodeRelay[pid] // 对端接入的 relay（可能为空）
		routes = append(routes, &genv1.Route{
			DestCidr:   destCIDR,
			ViaRelayId: relayID,
		})
	}
	return routes
}

// sortRelays 按 Priority 升序（小=优先）+ ID 字典序（保证确定性）排序。
func sortRelays(relays []RelayView) []RelayView {
	sorted := make([]RelayView, len(relays))
	copy(sorted, relays)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

// buildRelayEndpoints 把 RelayView 切片转为 proto RelayEndpoint 切片。
func buildRelayEndpoints(relays []RelayView) []*genv1.RelayEndpoint {
	if len(relays) == 0 {
		return nil
	}
	out := make([]*genv1.RelayEndpoint, 0, len(relays))
	for _, r := range relays {
		re := &genv1.RelayEndpoint{
			RelayId:  r.ID,
			Priority: uint32(r.Priority),
			Tunnels:  r.Tunnels,
		}
		if r.Endpoint != nil {
			re.Tunnel = r.Endpoint
		}
		if r.UDP != nil {
			re.Udp = r.UDP
		}
		out = append(out, re)
	}
	return out
}

// ─── IP 工具 ──────────────────────────────────────────────────────────────────

// normalizeToSlash32 确保返回 "x.x.x.x/32" 格式（输入可能是 "x.x.x.x/32" 或 "x.x.x.x"）。
func normalizeToSlash32(ip string) string {
	if strings.Contains(ip, "/") {
		// 验证并规范化
		_, ipNet, err := net.ParseCIDR(ip)
		if err == nil {
			ones, _ := ipNet.Mask.Size()
			if ones == 32 {
				return ip
			}
		}
		return ip
	}
	return ip + "/32"
}

// stripMask 去掉 "/32" 后缀，返回纯 IP 字符串（用于 NodeConfig.VirtualIp）。
func stripMask(ip string) string {
	if idx := strings.Index(ip, "/"); idx >= 0 {
		return ip[:idx]
	}
	return ip
}
