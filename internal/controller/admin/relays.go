package admin

import (
	"encoding/json"
	"net/http"
)

// relayDTO 是 relay 的管理面视图（端点信息 + 在线 + 邻接拓扑）。
type relayDTO struct {
	NodeID         string   `json:"node_id"`
	TunnelEndpoint string   `json:"tunnel_endpoint"`
	UDPEndpoint    string   `json:"udp_endpoint"`
	Protocols      string   `json:"protocols"`
	Priority       uint     `json:"priority"`
	Online         bool     `json:"online"`
	Neighbors      []string `json:"neighbors"`
}

// handleListRelays GET /admin/api/relays：relay 列表 + RelayInfo + 在线 + 拓扑。
func (s *Server) handleListRelays(w http.ResponseWriter, r *http.Request) {
	infos, err := s.deps.Store.ListRelayInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举 relay 失败")
		return
	}
	// 邻接拓扑：优先用 Roster.Topology（合并方向），否则回退 ListRelayLinks。
	topo := map[string][]string{}
	if s.deps.Roster != nil {
		if t, terr := s.deps.Roster.Topology(); terr == nil {
			topo = t
		}
	} else if links, lerr := s.deps.Store.ListRelayLinks(); lerr == nil {
		for _, l := range links {
			topo[l.RelayID] = append(topo[l.RelayID], l.NeighborID)
		}
	}

	out := make([]relayDTO, 0, len(infos))
	for _, ri := range infos {
		online := false
		if s.deps.Online != nil {
			online = s.deps.Online.IsOnline(ri.NodeID)
		}
		neighbors := topo[ri.NodeID]
		if neighbors == nil {
			neighbors = []string{}
		}
		out = append(out, relayDTO{
			NodeID:         ri.NodeID,
			TunnelEndpoint: ri.TunnelEndpoint,
			UDPEndpoint:    ri.UDPEndpoint,
			Protocols:      ri.Protocols,
			Priority:       ri.Priority,
			Online:         online,
			Neighbors:      neighbors,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": out})
}

// setTopologyRequest 设置某 relay 的邻接列表。
type setTopologyRequest struct {
	RelayID   string   `json:"relay_id"`
	Neighbors []string `json:"neighbors"`
}

// handleSetTopology PUT /admin/api/relays/topology：设邻接→触发该 relay 重算下发。
func (s *Server) handleSetTopology(w http.ResponseWriter, r *http.Request) {
	var req setTopologyRequest
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if req.RelayID == "" {
		writeError(w, http.StatusBadRequest, "relay_id 不能为空")
		return
	}
	if err := s.deps.Store.SetRelayLinks(req.RelayID, req.Neighbors); err != nil {
		writeError(w, http.StatusInternalServerError, "设置 relay 拓扑失败")
		return
	}
	// 触发该 relay 及其邻居重算下发（拓扑变更影响多跳路由）。
	if s.deps.Notify != nil {
		ids := append([]string{req.RelayID}, req.Neighbors...)
		s.deps.Notify.RecomputeAndNotify(ids...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "relay_id": req.RelayID, "neighbors": req.Neighbors})
}
