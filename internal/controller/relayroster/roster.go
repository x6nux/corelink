// Package relayroster 实现 relay 注册/拓扑/节点位置注册表，以及
// genv1.RelayControlServiceServer（§7.2.2/§7.2.3）。
package relayroster

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/grpc/peer"
)

// storeIface 是 Roster 对 store 的最小依赖接口。
type storeIface interface {
	UpsertRelayInfo(info *store.RelayInfo) error
	ListRelayInfo() ([]store.RelayInfo, error)
	ListRelayLinks() ([]store.RelayLink, error)
}

// notifyIface 是 Roster 触发重算通知的接口（通常是 *configsvc.Notify）。
type notifyIface interface {
	RecomputeAndNotify(nodeIDs ...string)
}

// AffectedNodesFunc 返回因 changedNode 位置变更而需一并重算的其他节点 ID
// （spec §7.2.3：会合 relay 变更影响与之有路由关系的对端）。
//
// 例如：某节点接入 relay 变了，则与它互通的对端节点的「就近会合 relay」选择
// 可能随之改变，需重算并下发新配置。返回 nil 表示无额外受影响节点。
type AffectedNodesFunc func(changedNode string) []string

// Roster 管理 relay 注册/拓扑及「节点→relay」位置注册表。
//
// 并发安全：mu 保护 nodeRelay 映射；version 通过 atomic 保证单调递增。
type Roster struct {
	genv1.UnimplementedRelayControlServiceServer

	st     storeIface
	notify notifyIface

	mu        sync.RWMutex
	nodeRelay map[string]string // nodeID → relayID（当前接入）
	affected  AffectedNodesFunc // 可选：位置变更时扩展受影响节点集合

	version atomic.Uint64 // 每次位置变更递增，返回给 ReportAck.Version
}

// New 构造 Roster。
//
//   - st：store 接口（实际传 *store.Store 即可）
//   - notify：配置变更通知接口（传 *configsvc.Notify 的 RecomputeAndNotify）
func New(st storeIface, notify notifyIface) *Roster {
	return &Roster{
		st:        st,
		notify:    notify,
		nodeRelay: make(map[string]string),
	}
}

// ReportNodeLocation 处理 relay 上报节点接入/离开事件（§7.2.3）。
//
//   - attached=true：更新「nodeID→relayID」映射。
//   - attached=false：从映射中删除该节点。
//
// 变更后：
//  1. version 递增（单调）。
//  2. 触发受影响节点的 configsvc.RecomputeAndNotify：该节点自身 + AffectedNodes
//     扩展的对端（会合 relay 因迁移而变化）。
func (r *Roster) ReportNodeLocation(ctx context.Context, loc *genv1.NodeLocation) (*genv1.ReportAck, error) {
	r.mu.Lock()
	old, existed := r.nodeRelay[loc.NodeId]
	if loc.Attached {
		r.nodeRelay[loc.NodeId] = loc.RelayId
	} else {
		delete(r.nodeRelay, loc.NodeId)
	}
	affected := r.affected
	r.mu.Unlock()

	// TRANSIT 自身即 relay：nodeId == relayId 时自动注册 relay 端点信息。
	// 从 gRPC peer 提取源 IP，结合上报的端口，构造公网端点。
	// 过滤回环地址（127.x / ::1）——通过 localhost 连 controller 的节点源 IP 无外部可达性。
	if loc.Attached && loc.NodeId == loc.RelayId {
		srcHost := peerHost(ctx)
		if isLoopback(srcHost) {
			srcHost = "" // 回环地址不可路由，不注册为 relay 端点
		}
		var tunnelEP, udpEP string
		if srcHost != "" && loc.StreamPort > 0 {
			tunnelEP = fmt.Sprintf("%s:%d", srcHost, loc.StreamPort)
		}
		if srcHost != "" && loc.UdpPort > 0 {
			udpEP = fmt.Sprintf("%s:%d", srcHost, loc.UdpPort)
		}
		var protos []string
		for _, p := range loc.Protocols {
			protos = append(protos, p.String())
		}
		if err := r.st.UpsertRelayInfo(&store.RelayInfo{
			NodeID:         loc.RelayId,
			TunnelEndpoint: tunnelEP,
			UDPEndpoint:    udpEP,
			Protocols:      strings.Join(protos, ","),
			Priority:       100,
		}); err != nil {
			slog.Warn("relayroster: relay 端点持久化失败", "relay", loc.RelayId, "err", err)
		}
	}

	ver := r.version.Add(1)

	// 位置是否实质变化：新接入、迁移到不同 relay、或离开。
	changed := (loc.Attached && (!existed || old != loc.RelayId)) || (!loc.Attached && existed)

	// 触发受影响节点的重算：该节点自身 + AffectedNodes 扩展的对端。
	// TRANSIT 自身即 relay 时通知所有节点（它们需要拿到新 relay 端点）。
	if r.notify != nil && loc.NodeId != "" {
		nodes := []string{loc.NodeId}
		if changed {
			if affected != nil {
				nodes = append(nodes, affected(loc.NodeId)...)
			}
			// 新 relay 注册时通知全网——所有节点的 configsvc relay 列表需更新
			if loc.Attached && loc.NodeId == loc.RelayId {
				if all, err := r.st.ListRelayInfo(); err == nil {
					for _, ri := range all {
						if ri.NodeID != loc.NodeId {
							nodes = append(nodes, ri.NodeID)
						}
					}
				}
			}
		}
		r.notify.RecomputeAndNotify(nodes...)
	}

	return &genv1.ReportAck{Version: ver}, nil
}

// ResolveNodeLocation 返回节点当前接入的 relay ID（spec §7.2.3 位置权威查询）。
//
// 供 relay 回源查询节点位置：
//   - relay 的 locationcache miss 时回源解析 dst_node 当前 relay；
//   - relay 的 hand-off 迁移转交窗口内回源解析切走节点的新位置。
//
// 返回 (relayID, true) 表示节点当前接入 relayID；("", false) 表示无记录（未接入）。
func (r *Roster) ResolveNodeLocation(nodeID string) (relayID string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	relayID, ok = r.nodeRelay[nodeID]
	return relayID, ok && relayID != ""
}

// SetAffectedNodes 注入位置变更时扩展受影响节点集合的回调（可选）。
// 传 nil 关闭扩展，仅通知变更节点自身。
func (r *Roster) SetAffectedNodes(fn AffectedNodesFunc) {
	r.mu.Lock()
	r.affected = fn
	r.mu.Unlock()
}

// NodeRelay 返回「nodeID→relayID」映射的快照副本，供 configsvc 构造 Snapshot。
func (r *Roster) NodeRelay() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make(map[string]string, len(r.nodeRelay))
	for k, v := range r.nodeRelay {
		cp[k] = v
	}
	return cp
}

// RegisterRelay 向 store 持久化 relay 端点/协议/优先级信息。
// relay 以 role=relay 节点身份通过 enroll 注册后，调用此方法补充端点元数据。
func (r *Roster) RegisterRelay(info *store.RelayInfo) error {
	return r.st.UpsertRelayInfo(info)
}

// Topology 从 store 构建 relay 邻接表（relayID → []neighborID），下发给 relay。
// RelayLink 存储有向边；Topology 合并所有方向建邻接表。
func (r *Roster) Topology() (map[string][]string, error) {
	links, err := r.st.ListRelayLinks()
	if err != nil {
		return nil, err
	}

	topo := make(map[string][]string)
	for _, l := range links {
		topo[l.RelayID] = append(topo[l.RelayID], l.NeighborID)
	}
	return topo, nil
}

// peerHost 从 gRPC peer context 提取对端源 IP（去端口，去 IPv6 括号）。
func peerHost(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}
	return host
}

// isLoopback 判断 host 是否为回环地址（127.x.x.x / ::1）。
func isLoopback(host string) bool {
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
