package admin

import (
	"net/http"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// certDTO 是证书的管理面视图。
type certDTO struct {
	Serial    string     `json:"serial"`
	NodeID    string     `json:"node_id"`
	NotAfter  time.Time  `json:"not_after"`
	Revoked   bool       `json:"revoked"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func toCertDTO(c *store.Cert) certDTO {
	return certDTO{
		Serial:    c.Serial,
		NodeID:    c.NodeID,
		NotAfter:  c.NotAfter,
		Revoked:   c.Revoked,
		RevokedAt: c.RevokedAt,
		CreatedAt: c.CreatedAt,
	}
}

// handleListCerts GET /admin/api/certs。
func (s *Server) handleListCerts(w http.ResponseWriter, r *http.Request) {
	certs, err := s.deps.Store.ListCerts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举证书失败")
		return
	}
	out := make([]certDTO, 0, len(certs))
	for i := range certs {
		out = append(out, toCertDTO(&certs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"certs": out})
}

// handleRevokeCert POST /admin/api/certs/{serial}/revoke。
func (s *Server) handleRevokeCert(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")
	if serial == "" {
		writeError(w, http.StatusBadRequest, "serial 不能为空")
		return
	}
	// 校验序列号存在（在已签发证书集合内）。
	certs, err := s.deps.Store.ListCerts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询证书失败")
		return
	}
	found := false
	for _, c := range certs {
		if c.Serial == serial {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "证书不存在")
		return
	}
	if s.deps.CA != nil {
		if err := s.deps.CA.Revoke(serial); err != nil {
			writeError(w, http.StatusInternalServerError, "吊销证书失败")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "serial": serial})
}

// caDTO 是 CA 信息响应（CA 证书 PEM + CA 公钥哈希，供节点入网钉扎）。
type caDTO struct {
	CACertPEM string `json:"ca_cert_pem"`
	CAHash    string `json:"ca_hash"`
}

// handleGetCA GET /admin/api/ca。
func (s *Server) handleGetCA(w http.ResponseWriter, r *http.Request) {
	if s.deps.CA == nil {
		writeError(w, http.StatusInternalServerError, "CA 不可用")
		return
	}
	pem, err := s.deps.CA.CACertPEM()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 CA 证书失败")
		return
	}
	h, err := s.deps.CA.CAPublicKeyHash()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "计算 CA 公钥哈希失败")
		return
	}
	writeJSON(w, http.StatusOK, caDTO{CACertPEM: string(pem), CAHash: h})
}
