package admin

import (
	"io"
	"net/http"
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/x6nux/corelink/internal/controller/acl"
	"github.com/x6nux/corelink/internal/controller/store"
)

// aclDTO 是当前 ACL 策略响应。
type aclDTO struct {
	Version  uint   `json:"version"`
	Document string `json:"document"`
	Author   string `json:"author"`
}

// handleGetACL GET /admin/api/acl：返回当前生效策略 + 版本。
func (s *Server) handleGetACL(w http.ResponseWriter, r *http.Request) {
	p, err := s.deps.Store.GetLatestACLPolicy()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 ACL 策略失败")
		return
	}
	writeJSON(w, http.StatusOK, aclDTO{Version: p.Version, Document: p.Document, Author: p.Author})
}

// handlePutACL PUT /admin/api/acl：校验→保存新版本→对全网重算下发。
func (s *Server) handlePutACL(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	// 解析 + 校验（ParsePolicy 内部已调用 Validate）。
	if _, err := acl.ParsePolicy(body); err != nil {
		writeError(w, http.StatusBadRequest, "ACL 策略校验失败: "+err.Error())
		return
	}
	author := UserFromContext(r.Context())
	saved, err := s.deps.Store.SaveACLPolicy(string(body), author)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存 ACL 策略失败")
		return
	}
	// 对全网节点重算下发。
	s.recomputeAll()
	writeJSON(w, http.StatusOK, aclDTO{Version: saved.Version, Document: saved.Document, Author: saved.Author})
}

// handleACLHistory GET /admin/api/acl/history：策略版本历史（降序）。
func (s *Server) handleACLHistory(w http.ResponseWriter, r *http.Request) {
	pols, err := s.deps.Store.ListACLPolicies()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取策略历史失败")
		return
	}
	out := make([]aclDTO, 0, len(pols))
	for _, p := range pols {
		out = append(out, aclDTO{Version: p.Version, Document: p.Document, Author: p.Author})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

// handleACLPreview POST /admin/api/acl/preview：给定候选策略，返回视图会变化的节点列表。
func (s *Server) handleACLPreview(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	candidate, err := acl.ParsePolicy(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ACL 策略校验失败: "+err.Error())
		return
	}

	nodes, err := s.deps.Store.ListNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举节点失败")
		return
	}
	relayInfos, err := s.deps.Store.ListRelayInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举 relay 失败")
		return
	}
	cur, err := s.deps.Store.GetLatestACLPolicy()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取当前策略失败")
		return
	}
	var curPolicy *acl.Policy
	if cur != nil && cur.Document != "" {
		if p, perr := acl.ParsePolicy([]byte(cur.Document)); perr == nil {
			curPolicy = p
		}
	}

	curCfgs := acl.Generate(buildAdminSnapshot(nodes, relayInfos, curPolicy))
	newCfgs := acl.Generate(buildAdminSnapshot(nodes, relayInfos, candidate))

	changed := make([]string, 0)
	for _, n := range nodes {
		if !proto.Equal(curCfgs[n.ID], newCfgs[n.ID]) {
			changed = append(changed, n.ID)
		}
	}
	sort.Strings(changed)
	writeJSON(w, http.StatusOK, map[string]any{"changed_nodes": changed})
}

// buildAdminSnapshot 把 store 数据组装成 acl.Snapshot（不含 NodeRelay；
// 预览仅关注 ACL 策略导致的 peer 可见性变化）。
func buildAdminSnapshot(nodes []store.Node, relayInfos []store.RelayInfo, policy *acl.Policy) acl.Snapshot {
	nodeViews := make([]acl.NodeView, 0, len(nodes))
	for _, n := range nodes {
		nodeViews = append(nodeViews, acl.NodeView{
			ID:        n.ID,
			User:      n.User,
			WGPubKey:  n.WGPubKey,
			VirtualIP: n.VirtualIP,
		})
	}
	relayViews := make([]acl.RelayView, 0, len(relayInfos))
	for _, ri := range relayInfos {
		relayViews = append(relayViews, acl.RelayView{ID: ri.NodeID, Priority: ri.Priority})
	}
	return acl.Snapshot{Policy: policy, Nodes: nodeViews, Relays: relayViews}
}

// recomputeAll 对全网节点触发重算下发。
func (s *Server) recomputeAll() {
	if s.deps.Notify == nil {
		return
	}
	nodes, err := s.deps.Store.ListNodes()
	if err != nil {
		return
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	if len(ids) > 0 {
		s.deps.Notify.RecomputeAndNotify(ids...)
	}
}
