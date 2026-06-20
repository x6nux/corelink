package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/x6nux/corelink/internal/controller/store"
)

type createSplitRuleRequest struct {
	NodeID     string `json:"node_id"`
	Match      string `json:"match"`
	Action     string `json:"action"`
	ExitNodeID string `json:"exit_node_id"`
	SortOrder  uint32 `json:"sort_order"`
}

type splitRuleDTO struct {
	ID         uint   `json:"id"`
	NodeID     string `json:"node_id"`
	Match      string `json:"match"`
	Action     string `json:"action"`
	ExitNodeID string `json:"exit_node_id"`
	SortOrder  uint32 `json:"sort_order"`
	Enabled    bool   `json:"enabled"`
}

// validateSplitMatch 校验 match 格式。
func validateSplitMatch(match string) error {
	switch {
	case strings.HasPrefix(match, "geoip:!"):
		code := strings.TrimPrefix(match, "geoip:!")
		if len(code) < 2 {
			return fmt.Errorf("无效的 geoip 取反规则: %s", match)
		}
	case strings.HasPrefix(match, "geoip:"):
		code := strings.TrimPrefix(match, "geoip:")
		if len(code) < 2 {
			return fmt.Errorf("无效的 geoip 规则: %s", match)
		}
	case strings.HasPrefix(match, "cidr:"):
		cidr := strings.TrimPrefix(match, "cidr:")
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("无效的 CIDR: %s", cidr)
		}
	default:
		return fmt.Errorf("不支持的 match 格式: %s（支持 geoip:xx / geoip:!xx / cidr:x.x.x.x/y）", match)
	}
	return nil
}

func (s *Server) handleListSplitRules(w http.ResponseWriter, _ *http.Request) {
	rules, err := s.deps.Store.ListSplitRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举分流规则失败")
		return
	}
	out := make([]splitRuleDTO, 0, len(rules))
	for _, r := range rules {
		out = append(out, splitRuleDTO{
			ID: r.ID, NodeID: r.NodeID, Match: r.Match, Action: r.Action,
			ExitNodeID: r.ExitNodeID, SortOrder: r.SortOrder, Enabled: r.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (s *Server) handleCreateSplitRule(w http.ResponseWriter, r *http.Request) {
	var req createSplitRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if req.Match == "" || req.Action == "" {
		writeError(w, http.StatusBadRequest, "match 和 action 为必填")
		return
	}
	if req.Action != "direct" && req.Action != "proxy" {
		writeError(w, http.StatusBadRequest, "action 仅支持 direct/proxy")
		return
	}
	if err := validateSplitMatch(req.Match); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Action == "proxy" && req.ExitNodeID == "" {
		writeError(w, http.StatusBadRequest, "action=proxy 时 exit_node_id 为必填")
		return
	}
	row := &store.SplitRuleRow{
		NodeID: req.NodeID, Match: req.Match, Action: req.Action,
		ExitNodeID: req.ExitNodeID, SortOrder: req.SortOrder, Enabled: true,
	}
	if err := s.deps.Store.CreateSplitRule(row); err != nil {
		writeError(w, http.StatusInternalServerError, "创建分流规则失败: "+err.Error())
		return
	}
	s.recomputeAll()
	writeJSON(w, http.StatusCreated, splitRuleDTO{
		ID: row.ID, NodeID: row.NodeID, Match: row.Match, Action: row.Action,
		ExitNodeID: row.ExitNodeID, SortOrder: row.SortOrder, Enabled: row.Enabled,
	})
}

func (s *Server) handleDeleteSplitRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "无效的 id")
		return
	}
	if err := s.deps.Store.DeleteSplitRule(uint(id)); err != nil {
		writeError(w, http.StatusInternalServerError, "删除分流规则失败")
		return
	}
	s.recomputeAll()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReorderSplitRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "无效的 id")
		return
	}
	var req struct {
		SortOrder uint32 `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if err := s.deps.Store.ReorderSplitRule(uint(id), req.SortOrder); err != nil {
		writeError(w, http.StatusInternalServerError, "调整排序失败")
		return
	}
	s.recomputeAll()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSplitTunnelStatus(w http.ResponseWriter, _ *http.Request) {
	rules, _ := s.deps.Store.ListSplitRules()
	meta, _ := s.deps.Store.GetLatestGeoIPMeta()
	resp := map[string]any{"rule_count": len(rules)}
	if meta != nil {
		resp["geoip_version"] = meta.SHA256
		resp["geoip_updated_at"] = meta.UpdatedAt
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGeoIPStatus(w http.ResponseWriter, _ *http.Request) {
	meta, err := s.deps.Store.GetLatestGeoIPMeta()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询 GeoIP 状态失败")
		return
	}
	if meta == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "no_data"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sha256": meta.SHA256, "file_size": meta.FileSize, "updated_at": meta.UpdatedAt,
	})
}
