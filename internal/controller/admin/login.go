package admin

import (
	"encoding/json"
	"net/http"
)

// handleLogin POST /admin/api/login：校验凭据→签发 token。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	// 限制请求体大小，避免超大 body 造成内存放大 / DoS。
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	token, err := s.deps.Auth.Login(req.User, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token, User: req.User})
}

// handleLogout POST /admin/api/logout：无状态 token，客户端丢弃即可。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
