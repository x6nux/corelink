package nodemethods

import (
	"encoding/json"
	"math"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// systemStatusResult is the response for system.status.
type systemStatusResult struct {
	NodeID        string    `json:"node_id"`
	VIP           string    `json:"vip"`
	Role          string    `json:"role"`
	Uptime        float64   `json:"uptime_seconds"`
	TopoVer       uint64    `json:"topo_version"`
	TopoUpdatedAt time.Time `json:"topo_updated_at"`
	Connected     bool      `json:"connected"`

	PeerCount       int  `json:"peer_count"`
	ConnectionCount int  `json:"connection_count"`
	AvgRTTms        int  `json:"avg_rtt_ms"`
	IngressCount    int  `json:"ingress_count"`
	PortmapActive   bool `json:"portmap_active"`
}

func registerSystemMethods(s *rpc.Server, deps Deps) {
	s.Register("system.status", handleSystemStatus(deps))
	s.Register("system.logs", handleSystemLogs(deps))
}

func handleSystemStatus(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		uptime := deps.Uptime().Seconds()
		uptime = math.Round(uptime*100) / 100

		var topoUpdated time.Time
		if deps.TopoUpdatedAt != nil {
			topoUpdated = deps.TopoUpdatedAt()
		}

		var peerCount, connCount, avgRTT, ingressCount int
		var portmapActive bool

		if deps.Peers != nil {
			peerCount = len(deps.Peers())
		}
		if deps.Connections != nil {
			conns := deps.Connections()
			connCount = len(conns)
			if connCount > 0 {
				var total uint32
				var rttCount int
				for _, c := range conns {
					if !c.RTTValid {
						continue
					}
					total += c.RTTms
					rttCount++
				}
				if rttCount > 0 {
					avgRTT = int(total) / rttCount
				}
			}
		}
		if deps.Ingresses != nil {
			ingressCount = len(deps.Ingresses())
		}
		if deps.PortmapStatus != nil {
			portmapActive = deps.PortmapStatus().Active
		}

		return systemStatusResult{
			NodeID:        deps.NodeID,
			VIP:           deps.VIP,
			Role:          deps.Role(),
			Uptime:        uptime,
			TopoVer:       deps.TopoVer(),
			TopoUpdatedAt: topoUpdated,
			Connected:     deps.Connected(),

			PeerCount:       peerCount,
			ConnectionCount: connCount,
			AvgRTTms:        avgRTT,
			IngressCount:    ingressCount,
			PortmapActive:   portmapActive,
		}, nil
	}
}

type logsParams struct {
	Count int `json:"count"`
}

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
