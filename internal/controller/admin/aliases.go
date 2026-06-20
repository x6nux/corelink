package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/x6nux/corelink/internal/controller/routepolicy"
	"github.com/x6nux/corelink/internal/controller/store"
)

type createAliasRequest struct {
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	FQDN      string `json:"fqdn"`
	Kind      string `json:"kind"`
	TargetVIP string `json:"target_vip"`
}

type aliasDTO struct {
	ID        uint   `json:"id"`
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	FQDN      string `json:"fqdn"`
	Kind      string `json:"kind"`
	TargetVIP string `json:"target_vip"`
	Enabled   bool   `json:"enabled"`
}

func (s *Server) handleListAliases(w http.ResponseWriter, _ *http.Request) {
	aliases, err := s.deps.Store.ListNodeAliases()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举别名失败")
		return
	}
	out := make([]aliasDTO, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, aliasDTO{
			ID: a.ID, NodeID: a.NodeID, Name: a.Name, FQDN: a.FQDN,
			Kind: a.Kind, TargetVIP: a.TargetVIP, Enabled: a.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": out})
}

func (s *Server) handleCreateAlias(w http.ResponseWriter, r *http.Request) {
	var req createAliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if err := routepolicy.ValidateAlias(req.Name, req.FQDN, req.Kind); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := &store.NodeAlias{
		NodeID:    req.NodeID,
		Name:      req.Name,
		FQDN:      req.FQDN,
		Kind:      req.Kind,
		TargetVIP: req.TargetVIP,
		Enabled:   true,
	}
	if err := s.deps.Store.CreateNodeAlias(a); err != nil {
		writeError(w, http.StatusInternalServerError, "创建别名失败: "+err.Error())
		return
	}
	s.recomputeAll()
	writeJSON(w, http.StatusCreated, aliasDTO{
		ID: a.ID, NodeID: a.NodeID, Name: a.Name, FQDN: a.FQDN,
		Kind: a.Kind, TargetVIP: a.TargetVIP, Enabled: a.Enabled,
	})
}

func (s *Server) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "无效的 id")
		return
	}
	if err := s.deps.Store.DeleteNodeAlias(uint(id)); err != nil {
		writeError(w, http.StatusInternalServerError, "删除别名失败")
		return
	}
	s.recomputeAll()
	w.WriteHeader(http.StatusNoContent)
}
