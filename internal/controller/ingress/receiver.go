// Package ingress implements the controller-side intake for CoreLink's ingress
// discovery (spec §3.3/§7): it receives the candidate ingress sets, edge quality
// reports and edge events that nodes (agents/relays) report, observes a caller's
// source address over gRPC, and provides the controller's built-in STUN
// reflection endpoint and public-IP HTTP endpoint that nodes probe against.
//
// This package is the controller counterpart to internal/nodecore/ingress (the
// node-side discovery). It is a separate package path, so the shared name
// "ingress" does not conflict.
package ingress

import (
	"context"
	"log/slog"
	"sync"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// Sink is the injected downstream hand-off for received reports. In P2 it will
// be implemented by the topostore/optimizer (which are not built yet), so the
// Receiver holds a possibly-nil Sink and only keeps an in-memory copy when nil.
//
// All methods are called synchronously from the corresponding RPC handlers
// after the in-memory copy has been stored; implementations must be safe for
// concurrent use and should not block for long.
type Sink interface {
	PutIngressSet(*genv1.IngressSet)
	PutQuality(*genv1.QualityReport)
	PutEdgeEvent(*genv1.EdgeEvent)
	PutMachineSpec(*genv1.MachineSpec)
}

// GeoPersister 持久化节点定位（解耦 store 依赖）。
type GeoPersister interface {
	UpdateNodeGeo(id string, lat, lon float64, city, country, accuracy, colIATA string, colLat, colLon, cfRttMs float64) error
}

// Receiver implements genv1.IngressServiceServer: it accepts ingress sets,
// quality reports, edge events and machine specs from nodes, keeps the latest
// per-node copy in memory (thread-safe), and forwards each report to the
// injected Sink (if any).
//
// Concurrency: mu protects all maps. The embedded
// UnimplementedIngressServiceServer keeps the implementation forward-compatible
// when new methods are added to the service.
type Receiver struct {
	genv1.UnimplementedIngressServiceServer

	sink Sink

	// Notify 入口上报后触发该节点配置重算；接口类型避免 import configsvc。
	Notify interface{ RecomputeAndNotify(nodeIDs ...string) }
	// OnTopoTick 入口变更后立即触发拓扑重算（指向 TopoService.Tick）。
	OnTopoTick func()
	// GeoPersist 定位持久化（可选，nil 则仅内存）。
	GeoPersist GeoPersister

	mu           sync.RWMutex
	ingressSets  map[string]*genv1.IngressSet    // nodeID → latest reported set
	quality      map[string]*genv1.QualityReport // srcNode → latest quality report
	edgeEvents   map[string]*genv1.EdgeEvent     // srcNode → latest edge event
	machineSpecs map[string]*genv1.MachineSpec   // nodeID → latest machine spec
	nodeGeo      map[string]*genv1.NodeGeo       // nodeID → 定位
	routeReports map[string]*genv1.RouteReport   // srcNode → 路由
}

// New constructs a Receiver. sink may be nil; when nil the Receiver only keeps
// reports in memory (useful before the P2 topostore/optimizer exist, and in
// tests).
func New(sink Sink) *Receiver {
	return &Receiver{
		sink:         sink,
		ingressSets:  make(map[string]*genv1.IngressSet),
		quality:      make(map[string]*genv1.QualityReport),
		edgeEvents:   make(map[string]*genv1.EdgeEvent),
		machineSpecs: make(map[string]*genv1.MachineSpec),
		nodeGeo:      make(map[string]*genv1.NodeGeo),
		routeReports: make(map[string]*genv1.RouteReport),
	}
}

// ReportIngress stores the node's candidate ingress set (keyed by node_id) and
// forwards it to the Sink.
//
// nodeID provenance: this task trusts the node_id carried in the message. When
// the controller is wired up in P4 the authoritative identity will instead be
// taken from the caller's mTLS certificate CN (matching the existing
// configsvc/relayroster pattern), and the message node_id will be validated
// against it.
func (r *Receiver) ReportIngress(_ context.Context, set *genv1.IngressSet) (*genv1.Ack, error) {
	if set != nil {
		r.mu.Lock()
		r.ingressSets[set.NodeId] = set
		r.mu.Unlock()

		if r.sink != nil {
			r.sink.PutIngressSet(set)
		}

		// 入口上报后立即触发拓扑重算（含 damping 节流）+ 该节点配置重算。
		// OnTopoTick 驱动全网拓扑更新（邻居入口地址变更需要通知所有相关节点）。
		if r.OnTopoTick != nil {
			r.OnTopoTick()
		}
		if r.Notify != nil {
			r.Notify.RecomputeAndNotify(set.NodeId)
		}
	}
	return &genv1.Ack{Ok: true}, nil
}

// ReportQuality stores the node's edge quality report (keyed by src_node) and
// forwards it to the Sink. See ReportIngress for the nodeID provenance note.
func (r *Receiver) ReportQuality(_ context.Context, q *genv1.QualityReport) (*genv1.Ack, error) {
	if q != nil {
		r.mu.Lock()
		r.quality[q.SrcNode] = q
		r.mu.Unlock()

		if r.sink != nil {
			r.sink.PutQuality(q)
		}
	}
	return &genv1.Ack{Ok: true}, nil
}

// ReportEdgeEvent stores the node's edge event (keyed by src_node) and forwards
// it to the Sink. See ReportIngress for the nodeID provenance note.
func (r *Receiver) ReportEdgeEvent(_ context.Context, ev *genv1.EdgeEvent) (*genv1.Ack, error) {
	if ev != nil {
		r.mu.Lock()
		r.edgeEvents[ev.SrcNode] = ev
		r.mu.Unlock()

		if r.sink != nil {
			r.sink.PutEdgeEvent(ev)
		}
	}
	return &genv1.Ack{Ok: true}, nil
}

// ObserveSource returns the caller's source address as observed by the gRPC
// transport (the reflexive address through any NAT in front of the caller). The
// request body is currently advisory; the answer is derived from the gRPC peer.
// Implemented in srcobserve.go.
func (r *Receiver) ObserveSource(ctx context.Context, _ *genv1.ObserveRequest) (*genv1.SourceAddr, error) {
	return sourceAddrFromContext(ctx)
}

// GetIngressSet returns the latest ingress set reported by nodeID, if any.
// Provided for P2/optimizer and tests.
func (r *Receiver) GetIngressSet(nodeID string) (*genv1.IngressSet, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set, ok := r.ingressSets[nodeID]
	return set, ok
}

// AllIngressSets returns a snapshot slice of every node's latest ingress set.
// The slice is freshly allocated; the contained pointers are shared with the
// stored copies (callers must treat them as read-only).
func (r *Receiver) AllIngressSets() []*genv1.IngressSet {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*genv1.IngressSet, 0, len(r.ingressSets))
	for _, set := range r.ingressSets {
		out = append(out, set)
	}
	return out
}

// AllQuality returns a snapshot slice of every node's latest quality report
// (keyed internally by src_node). The slice is freshly allocated; the contained
// pointers are shared with the stored copies (callers must treat them as
// read-only). Provided for the P4 topology adapter (IngressSourceAdapter) to
// aggregate a topology.QualityMatrix from reported edge samples.
func (r *Receiver) AllQuality() []*genv1.QualityReport {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*genv1.QualityReport, 0, len(r.quality))
	for _, q := range r.quality {
		out = append(out, q)
	}
	return out
}

// ReportMachineSpec 存储节点上报的机器规格（keyed by node_id）并转发给 Sink。
// 阶段1 仅建立"采集→上报→controller 接收存储"通路，不参与任何决策。
func (r *Receiver) ReportMachineSpec(_ context.Context, spec *genv1.MachineSpec) (*genv1.Ack, error) {
	if spec != nil {
		r.mu.Lock()
		r.machineSpecs[spec.NodeId] = spec
		r.mu.Unlock()

		if r.sink != nil {
			r.sink.PutMachineSpec(spec)
		}
	}
	return &genv1.Ack{Ok: true}, nil
}

// MachineSpec 返回 nodeID 最近上报的机器规格，不存在时返回 nil。
// 供阶段2 选举打分使用。
func (r *Receiver) MachineSpec(nodeID string) *genv1.MachineSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.machineSpecs[nodeID]
}

// ReportNodeGeo 存储节点定位结果（内存 + DB 持久化）。
func (r *Receiver) ReportNodeGeo(_ context.Context, geo *genv1.NodeGeo) (*genv1.Ack, error) {
	if geo != nil {
		r.mu.Lock()
		r.nodeGeo[geo.NodeId] = geo
		r.mu.Unlock()
		r.persistGeo(geo)
	}
	return &genv1.Ack{Ok: true}, nil
}

// ReportRoutes 存储节点路由选路结果（内存快照）。
func (r *Receiver) ReportRoutes(_ context.Context, rep *genv1.RouteReport) (*genv1.Ack, error) {
	if rep != nil {
		r.mu.Lock()
		r.routeReports[rep.SrcNodeId] = rep
		r.mu.Unlock()
	}
	return &genv1.Ack{Ok: true}, nil
}

// SetNodeGeo 直接写入定位（admin 手动修正 / controller 自身定位）。
func (r *Receiver) SetNodeGeo(geo *genv1.NodeGeo) {
	r.mu.Lock()
	r.nodeGeo[geo.NodeId] = geo
	r.mu.Unlock()
	r.persistGeo(geo)
}

// persistGeo 异步写 DB（不阻塞 RPC）。
func (r *Receiver) persistGeo(geo *genv1.NodeGeo) {
	if r.GeoPersist == nil {
		return
	}
	go func() {
		if err := r.GeoPersist.UpdateNodeGeo(geo.NodeId,
			geo.Latitude, geo.Longitude, geo.City, geo.Country, geo.Accuracy,
			geo.ColoIata, geo.ColoLat, geo.ColoLon, geo.CfRttMs,
		); err != nil {
			slog.Warn("location: DB 持久化失败", "node", geo.NodeId, "err", err)
		}
	}()
}

// GetNodeGeo 返回 nodeID 最近上报的定位，不存在时 ok=false。
func (r *Receiver) GetNodeGeo(nodeID string) (*genv1.NodeGeo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.nodeGeo[nodeID]
	return g, ok
}

// AllNodeGeo 返回所有节点最新定位的快照切片（指针共享，调用方应只读）。
func (r *Receiver) AllNodeGeo() []*genv1.NodeGeo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*genv1.NodeGeo, 0, len(r.nodeGeo))
	for _, g := range r.nodeGeo {
		out = append(out, g)
	}
	return out
}

// LoadGeo 从持久化数据恢复内存 nodeGeo（controller 启动时调用）。
func (r *Receiver) LoadGeo(geos []*genv1.NodeGeo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range geos {
		if g != nil && g.NodeId != "" && (g.Latitude != 0 || g.Longitude != 0) {
			r.nodeGeo[g.NodeId] = g
		}
	}
	slog.Info("location: 从 DB 恢复定位", "count", len(r.nodeGeo))
}

// AllRouteReports 返回所有节点最新路由选路的快照切片（指针共享，调用方应只读）。
func (r *Receiver) AllRouteReports() []*genv1.RouteReport {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*genv1.RouteReport, 0, len(r.routeReports))
	for _, rr := range r.routeReports {
		out = append(out, rr)
	}
	return out
}
