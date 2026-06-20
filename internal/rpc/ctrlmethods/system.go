package ctrlmethods

import (
	"encoding/json"
	"math"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// systemStatusResult is the response for system.status.
type systemStatusResult struct {
	UptimeSeconds float64           `json:"uptime_seconds"`
	Version       string            `json:"version"`
	NodeCount     int               `json:"node_count"`
	OnlineCount   int               `json:"online_count"`
	TopoVersion   uint64            `json:"topo_version"`
	TopoRecompute time.Time         `json:"topo_recompute"`
	TransitCount  int               `json:"transit_count"`
	LeafCount     int               `json:"leaf_count"`
	CertCount     int               `json:"cert_count"`
	KeyCount      int               `json:"key_count"`
	Nodes         []nodeStatusEntry `json:"nodes,omitempty"`
}

// nodeStatusEntry 仪表盘用的节点摘要。
type nodeStatusEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	VIP    string `json:"vip"`
	Role   string `json:"role"`
	Online bool   `json:"online"`
}

func registerSystemMethods(s *rpc.Server, deps Deps) {
	s.Register("system.status", handleSystemStatus(deps))
	s.Register("config.status", handleConfigStatus(deps))
	s.Register("system.logs", handleSystemLogs(deps))
}

func handleSystemStatus(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		nodes, err := deps.Store.ListNodes()
		if err != nil {
			return nil, err
		}
		onlineCount := 0
		entries := make([]nodeStatusEntry, 0, len(nodes))
		for _, n := range nodes {
			online := false
			if deps.Online != nil && deps.Online.IsOnline(n.ID) {
				online = true
				onlineCount++
			}
			role := n.Role
			if deps.Topo != nil {
				if a, ok := deps.Topo.AssignmentForNode(n.ID); ok && a != nil {
					role = a.GetRole().String()
				}
			}
			entries = append(entries, nodeStatusEntry{
				ID:     n.ID,
				Name:   n.Name,
				VIP:    n.VirtualIP,
				Role:   role,
				Online: online,
			})
		}

		var topoVersion uint64
		var topoRecompute time.Time
		var transitCount, leafCount int
		if deps.Topo != nil {
			ts := deps.Topo.Status()
			topoVersion = ts.Version
			topoRecompute = ts.LastRecompute
			transitCount = ts.TransitCount
			leafCount = ts.LeafCount
		}

		certCount := 0
		if certs, err := deps.Store.ListCerts(); err == nil {
			certCount = len(certs)
		}

		keyCount := 0
		if keys, err := deps.Store.ListEnrollKeys(); err == nil {
			for _, k := range keys {
				if !k.Revoked && !k.Consumed {
					keyCount++
				}
			}
		}

		uptime := time.Since(deps.StartTime).Seconds()
		uptime = math.Round(uptime*100) / 100

		return systemStatusResult{
			UptimeSeconds: uptime,
			Version:       deps.Version,
			NodeCount:     len(nodes),
			OnlineCount:   onlineCount,
			TopoVersion:   topoVersion,
			TopoRecompute: topoRecompute,
			TransitCount:  transitCount,
			LeafCount:     leafCount,
			CertCount:     certCount,
			KeyCount:      keyCount,
			Nodes:         entries,
		}, nil
	}
}

func handleConfigStatus(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		if deps.Config == nil {
			return map[string]string{"status": "unavailable"}, nil
		}
		return deps.Config, nil
	}
}

// logsParams system.logs 请求参数。
type logsParams struct {
	Count int `json:"count"`
}

// logsResult system.logs 响应。
type logsResult struct {
	Entries []rpc.LogEntry `json:"entries"`
}

func handleSystemLogs(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		count := 100
		if params != nil {
			var p logsParams
			if err := json.Unmarshal(params, &p); err == nil && p.Count > 0 {
				count = p.Count
			}
		}
		if deps.LogBuffer == nil {
			return logsResult{Entries: []rpc.LogEntry{}}, nil
		}
		entries := deps.LogBuffer.Recent(count)
		if entries == nil {
			entries = []rpc.LogEntry{}
		}
		return logsResult{Entries: entries}, nil
	}
}
