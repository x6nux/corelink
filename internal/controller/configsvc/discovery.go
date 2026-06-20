package configsvc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// discoveryStoreIface 是 discovery report handler 需要的 store 接口。
type discoveryStoreIface interface {
	UpsertDiscoveredMapping(m *store.DiscoveredMapping) error
	GetPublishedRoute(id uint) (*store.PublishedRoute, error)
	ListDiscoveredMappingsByRoute(routeID uint) ([]store.DiscoveredMapping, error)
	SetDiscoveredMappingWinner(id uint, winner bool) error
}

// discoveryNotifier 触发配置重算。
type discoveryNotifier interface {
	RecomputeAndNotify(nodeIDs ...string)
}

// DiscoveryHTTP 处理 node 上报的 ARP/邻居发现结果。
type DiscoveryHTTP struct {
	st     discoveryStoreIface
	notify discoveryNotifier
}

// NewDiscoveryHTTP 构造 discovery report handler。
func NewDiscoveryHTTP(st discoveryStoreIface, notify discoveryNotifier) *DiscoveryHTTP {
	return &DiscoveryHTTP{st: st, notify: notify}
}

type discoveryReport struct {
	RouteID    uint   `json:"route_id"`
	NodeID     string `json:"node_id"`
	TargetIP   string `json:"target_ip"`
	VIPIP      string `json:"vip_ip"`
	ObservedAt string `json:"observed_at"`
}

func (h *DiscoveryHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	nodeID, err := NodeIDFromTLSCerts(r.TLS)
	if err != nil {
		slog.Warn("discovery report: mTLS 认证失败", "err", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var report discoveryReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// 验证 route 存在且属于 discovered_mapping
	route, err := h.st.GetPublishedRoute(report.RouteID)
	if err != nil {
		http.Error(w, "Route not found", http.StatusNotFound)
		return
	}
	if route.Kind != "discovered_mapping" {
		http.Error(w, "Route is not a discovered_mapping", http.StatusBadRequest)
		return
	}

	observedAt := time.Now()
	if report.ObservedAt != "" {
		if t, err := time.Parse(time.RFC3339, report.ObservedAt); err == nil {
			observedAt = t
		}
	}

	mapping := &store.DiscoveredMapping{
		RouteID:    report.RouteID,
		NodeID:     nodeID,
		TargetIP:   report.TargetIP,
		VIPIP:      report.VIPIP,
		Priority:   route.Priority,
		ObservedAt: observedAt,
		StaleAfter: 5 * time.Minute,
	}
	if err := h.st.UpsertDiscoveredMapping(mapping); err != nil {
		slog.Error("discovery report: upsert failed", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 重新计算 winner
	if err := h.recomputeWinners(route.ID); err != nil {
		slog.Warn("discovery report: winner recompute failed", "err", err)
	}

	h.notify.RecomputeAndNotify(nodeID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *DiscoveryHTTP) recomputeWinners(routeID uint) error {
	mappings, err := h.st.ListDiscoveredMappingsByRoute(routeID)
	if err != nil {
		return fmt.Errorf("list mappings: %w", err)
	}

	// 按 VIPIP 分组，每个 VIP 只保留一个 winner（priority 最低、observed_at 最新）
	type winCandidate struct {
		id       uint
		priority uint32
		observed time.Time
		nodeID   string
	}
	vipWinners := make(map[string]*winCandidate)
	for _, m := range mappings {
		existing, ok := vipWinners[m.VIPIP]
		if !ok || m.Priority < existing.priority ||
			(m.Priority == existing.priority && m.ObservedAt.After(existing.observed)) ||
			(m.Priority == existing.priority && m.ObservedAt.Equal(existing.observed) && m.NodeID < existing.nodeID) {
			vipWinners[m.VIPIP] = &winCandidate{id: m.ID, priority: m.Priority, observed: m.ObservedAt, nodeID: m.NodeID}
		}
	}

	winnerIDs := make(map[uint]bool)
	for _, w := range vipWinners {
		winnerIDs[w.id] = true
	}

	for _, m := range mappings {
		shouldWin := winnerIDs[m.ID]
		if m.Winner != shouldWin {
			if err := h.st.SetDiscoveredMappingWinner(m.ID, shouldWin); err != nil {
				return fmt.Errorf("set winner %d: %w", m.ID, err)
			}
		}
	}
	return nil
}
