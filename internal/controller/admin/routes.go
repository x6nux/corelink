package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/x6nux/corelink/internal/controller/routepolicy"
	"github.com/x6nux/corelink/internal/controller/store"
)

type createRouteRequest struct {
	NodeID        string `json:"node_id"`
	Kind          string `json:"kind"`
	RouteCIDR     string `json:"route_cidr"`
	VIPCIDR       string `json:"vip_cidr"`
	TargetCIDR    string `json:"target_cidr"`
	Priority      uint32 `json:"priority"`
	Metric        uint32 `json:"metric"`
	SNAT          *bool  `json:"snat"`
	DiscoveryMode string `json:"discovery_mode"`
}

type patchRouteRequest struct {
	Enabled *bool `json:"enabled"`
}

type routeDTO struct {
	ID            uint   `json:"id"`
	NodeID        string `json:"node_id"`
	Kind          string `json:"kind"`
	RouteCIDR     string `json:"route_cidr,omitempty"`
	VIPCIDR       string `json:"vip_cidr,omitempty"`
	TargetCIDR    string `json:"target_cidr,omitempty"`
	Priority      uint32 `json:"priority"`
	Metric        uint32 `json:"metric"`
	SNAT          bool   `json:"snat"`
	Enabled       bool   `json:"enabled"`
	DiscoveryMode string `json:"discovery_mode,omitempty"`
}

func (s *Server) handleListRoutes(w http.ResponseWriter, _ *http.Request) {
	routes, err := s.deps.Store.ListAllPublishedRoutes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举路由失败")
		return
	}
	out := make([]routeDTO, 0, len(routes))
	for _, r := range routes {
		out = append(out, routeDTO{
			ID: r.ID, NodeID: r.NodeID, Kind: r.Kind, RouteCIDR: r.RouteCIDR,
			VIPCIDR: r.VIPCIDR, TargetCIDR: r.TargetCIDR, Priority: r.Priority,
			Metric: r.Metric, SNAT: r.SNAT, Enabled: r.Enabled, DiscoveryMode: r.DiscoveryMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": out})
}

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	var req createRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if err := routepolicy.ValidatePublishedRoute(req.Kind, req.RouteCIDR, req.VIPCIDR, req.TargetCIDR); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snat := true
	if req.SNAT != nil {
		snat = *req.SNAT
	}
	priority := req.Priority
	if priority == 0 {
		priority = 100
	}
	metric := req.Metric
	if metric == 0 {
		metric = 100
	}
	route := &store.PublishedRoute{
		NodeID:        req.NodeID,
		Kind:          req.Kind,
		RouteCIDR:     req.RouteCIDR,
		VIPCIDR:       req.VIPCIDR,
		TargetCIDR:    req.TargetCIDR,
		Priority:      priority,
		Metric:        metric,
		SNAT:          snat,
		Enabled:       true,
		DiscoveryMode: req.DiscoveryMode,
	}
	if err := s.deps.Store.CreatePublishedRoute(route); err != nil {
		writeError(w, http.StatusInternalServerError, "创建路由失败: "+err.Error())
		return
	}
	s.recomputeAll()
	writeJSON(w, http.StatusCreated, routeDTO{
		ID: route.ID, NodeID: route.NodeID, Kind: route.Kind, RouteCIDR: route.RouteCIDR,
		VIPCIDR: route.VIPCIDR, TargetCIDR: route.TargetCIDR, Priority: route.Priority,
		Metric: route.Metric, SNAT: route.SNAT, Enabled: route.Enabled, DiscoveryMode: route.DiscoveryMode,
	})
}

func (s *Server) handlePatchRoute(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "无效的 id")
		return
	}
	var req patchRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if req.Enabled != nil {
		if err := s.deps.Store.SetPublishedRouteEnabled(uint(id), *req.Enabled); err != nil {
			writeError(w, http.StatusInternalServerError, "更新路由失败")
			return
		}
	}
	s.recomputeAll()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "无效的 id")
		return
	}
	if err := s.deps.Store.DeletePublishedRoute(uint(id)); err != nil {
		writeError(w, http.StatusInternalServerError, "删除路由失败")
		return
	}
	s.recomputeAll()
	w.WriteHeader(http.StatusNoContent)
}
