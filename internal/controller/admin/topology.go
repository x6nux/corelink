package admin

import (
	"encoding/json"
	"net/http"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// topoNodeDTO 是拓扑视图中单个节点的管理面视图。
type topoNodeDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	VIP       string  `json:"vip"`
	Online    bool    `json:"online"`
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
	City      string  `json:"city"`
	Country   string  `json:"country"`
	Accuracy  string  `json:"accuracy"`
	CFRttMs   float64 `json:"cf_rtt_ms"`
	ColIATA   string  `json:"col_iata,omitempty"`
}

// topoEdgeDTO 是物理拓扑的一条边（一对相邻直连节点）。
type topoEdgeDTO struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// topoRouteDTO 是一条有效路由（源→目的→下一跳 + RTT）。
type topoRouteDTO struct {
	Src     string `json:"src"`
	Dst     string `json:"dst"`
	NextHop string `json:"next_hop"`
	RttMs   uint32 `json:"rtt_ms"`
}

// topologyDTO 是 GET /admin/api/topology 的响应体。
type topologyDTO struct {
	Nodes         []topoNodeDTO  `json:"nodes"`
	PhysicalEdges []topoEdgeDTO  `json:"physical_edges"`
	ActiveRoutes  []topoRouteDTO `json:"active_routes"`
}

// handleGetTopology 聚合节点列表 + 定位 + 路由选路为单一拓扑视图。
//
// 数据来源：
//   - nodes: Store.ListNodes（基础节点信息）
//   - 定位: Topology.AllNodeGeo（按 NodeId 关联）
//   - 路由: Topology.AllRouteReports（每个 hop 出一条 active_route）
//   - 物理边: 路由 hop 中的 Ranked 邻居列表（去重无向边）
//
// Topology 为 nil 时仅返回 nodes，edges/routes 为空。
func (s *Server) handleGetTopology(w http.ResponseWriter, r *http.Request) {
	nodes, _ := s.deps.Store.ListNodes()

	geoMap := map[string]*genv1.NodeGeo{}
	if s.deps.Topology != nil {
		for _, g := range s.deps.Topology.AllNodeGeo() {
			geoMap[g.NodeId] = g
		}
	}

	out := topologyDTO{Nodes: make([]topoNodeDTO, 0, len(nodes))}
	for i := range nodes {
		n := &nodes[i]
		dto := topoNodeDTO{
			ID:   n.ID,
			Name: n.Name,
			VIP:  n.VirtualIP,
		}
		if s.deps.Online != nil {
			dto.Online = s.deps.Online.IsOnline(n.ID)
		}
		if g, ok := geoMap[n.ID]; ok {
			dto.Latitude = g.Latitude
			dto.Longitude = g.Longitude
			dto.City = g.City
			dto.Country = g.Country
			dto.Accuracy = g.Accuracy
			dto.CFRttMs = g.CfRttMs
			dto.ColIATA = g.ColoIata
		}
		out.Nodes = append(out.Nodes, dto)
	}

	if s.deps.Topology != nil {
		seen := map[string]bool{}
		for _, rep := range s.deps.Topology.AllRouteReports() {
			for _, hop := range rep.Routes {
				out.ActiveRoutes = append(out.ActiveRoutes, topoRouteDTO{
					Src:     rep.SrcNodeId,
					Dst:     hop.DstNodeId,
					NextHop: hop.NextHopId,
					RttMs:   hop.RttMs,
				})
				// Ranked 是源节点直连可达的邻居列表，作为物理边（去重无向）。
				for _, h := range hop.Ranked {
					key := rep.SrcNodeId + ">" + h
					rkey := h + ">" + rep.SrcNodeId
					if !seen[key] && !seen[rkey] {
						seen[key] = true
						out.PhysicalEdges = append(out.PhysicalEdges, topoEdgeDTO{
							Src: rep.SrcNodeId,
							Dst: h,
						})
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// handleSetNodeGeo 手动修正节点定位坐标。
//
// 请求体: {"lat": <float>, "lon": <float>, "city": "<string>"}
// Accuracy 固定为 "manual"，标识管理员手动覆盖（覆盖节点自动上报）。
func (s *Server) handleSetNodeGeo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.deps.Topology == nil {
		writeError(w, http.StatusInternalServerError, "拓扑服务不可用")
		return
	}
	var req struct {
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		City string  `json:"city"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	s.deps.Topology.SetNodeGeo(&genv1.NodeGeo{
		NodeId:    id,
		Latitude:  req.Lat,
		Longitude: req.Lon,
		City:      req.City,
		Accuracy:  "manual",
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
