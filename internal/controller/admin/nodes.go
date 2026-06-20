package admin

import (
	"encoding/json"
	"net/http"

	"github.com/x6nux/corelink/internal/controller/store"
)

// nodeDTO 是节点的管理面视图。
type nodeDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Remark     string `json:"remark,omitempty"`
	Role       string `json:"role"`
	Hostname   string `json:"hostname"`
	User       string `json:"user"`
	VirtualIP  string `json:"virtual_ip"`
	WGPubKey   string `json:"wg_public_key"`
	Generation uint64 `json:"generation"`
	Online     bool   `json:"online"`
}

func (s *Server) toNodeDTO(n *store.Node) nodeDTO {
	online := false
	if s.deps.Online != nil {
		online = s.deps.Online.IsOnline(n.ID)
	}
	return nodeDTO{
		ID:         n.ID,
		Name:       n.Name,
		Remark:     n.Remark,
		Role:       n.Role,
		Hostname:   n.Hostname,
		User:       n.User,
		VirtualIP:  n.VirtualIP,
		WGPubKey:   n.WGPubKey,
		Generation: n.Generation,
		Online:     online,
	}
}

// handleListNodes GET /admin/api/nodes。
func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.deps.Store.ListNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举节点失败")
		return
	}
	out := make([]nodeDTO, 0, len(nodes))
	for i := range nodes {
		out = append(out, s.toNodeDTO(&nodes[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

// handleGetNode GET /admin/api/nodes/{id} — 支持按 ID、Name、VIP 查找。
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	hint := r.PathValue("id")
	n, err := s.deps.Store.ResolveNode(hint)
	if err != nil {
		writeError(w, http.StatusNotFound, "节点不存在")
		return
	}
	writeJSON(w, http.StatusOK, s.toNodeDTO(n))
}

// handlePatchNode PATCH /admin/api/nodes/{id} — 更新名称/备注。
func (s *Server) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	hint := r.PathValue("id")
	n, err := s.deps.Store.ResolveNode(hint)
	if err != nil {
		writeError(w, http.StatusNotFound, "节点不存在")
		return
	}
	var req struct {
		Name   string `json:"name"`
		Remark string `json:"remark"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	if err := s.deps.Store.UpdateNodeMeta(n.ID, req.Name, req.Remark); err != nil {
		writeError(w, http.StatusInternalServerError, "更新失败: "+err.Error())
		return
	}
	updated, _ := s.deps.Store.GetNode(n.ID)
	if updated == nil {
		updated = n
	}
	writeJSON(w, http.StatusOK, s.toNodeDTO(updated))
}

// handleDeleteNode DELETE /admin/api/nodes/{id} — 支持按 ID/Name/VIP 查找。
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	hint := r.PathValue("id")
	n, err := s.deps.Store.ResolveNode(hint)
	if err != nil {
		writeError(w, http.StatusNotFound, "节点不存在")
		return
	}
	id := n.ID

	// 1. 预读待清理资源（删除前快照，删除后这些行可能已不可查）。
	certs, _ := s.deps.Store.ListCertsByNode(id)
	leases, _ := s.deps.Store.GetLeasesByNode(id)

	// 2. 收集其余节点（删除后需重算下发以剔除该 peer）。
	var others []string
	if all, err := s.deps.Store.ListNodes(); err == nil {
		for _, n := range all {
			if n.ID != id {
				others = append(others, n.ID)
			}
		}
	}

	// 3. 先删除节点记录：成功后才执行不可逆/会回收资源的清理。
	//    若此处失败，证书仍有效、IP 仍占用，节点记录保持一致，无虚拟 IP 冲突。
	if err := s.deps.Store.DeleteNode(id); err != nil {
		writeError(w, http.StatusInternalServerError, "删除节点失败")
		return
	}

	// 4. 吊销该节点的全部证书。
	for _, c := range certs {
		if c.Revoked {
			continue
		}
		if s.deps.CA != nil {
			_ = s.deps.CA.Revoke(c.Serial)
		}
	}

	// 5. 回收该节点持有的 IP 租约。
	for _, l := range leases {
		if s.deps.IPAM != nil {
			_ = s.deps.IPAM.Release(l.IP)
		}
	}

	// 6. 触发其余节点重算下发。
	if s.deps.Notify != nil && len(others) > 0 {
		s.deps.Notify.RecomputeAndNotify(others...)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}
