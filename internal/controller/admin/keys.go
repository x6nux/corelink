package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// keyDTO 是注册密钥的管理面视图。
type keyDTO struct {
	Key       string     `json:"key"`
	Reusable  bool       `json:"reusable"`
	Tag       string     `json:"tag"`
	Revoked   bool       `json:"revoked"`  // 管理员吊销
	Consumed  bool       `json:"consumed"` // 一次性 key 已被消费
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func toKeyDTO(ek *store.EnrollKey) keyDTO {
	return keyDTO{
		Key:       ek.Key,
		Reusable:  ek.Reusable,
		Tag:       ek.Tag,
		Revoked:   ek.Revoked,
		Consumed:  ek.Consumed,
		ExpiresAt: ek.ExpiresAt,
		CreatedAt: ek.CreatedAt,
	}
}

// handleListKeys GET /admin/api/keys。
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.deps.Store.ListEnrollKeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "列举注册密钥失败")
		return
	}
	out := make([]keyDTO, 0, len(keys))
	for i := range keys {
		out = append(out, toKeyDTO(&keys[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// createKeyRequest 是生成密钥请求体。
type createKeyRequest struct {
	Reusable   bool   `json:"reusable"`
	Tag        string `json:"tag"`
	TTLSeconds int64  `json:"ttl_seconds"` // 0 表示永不过期
}

// handleCreateKey POST /admin/api/keys：生成新注册密钥。
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if req.TTLSeconds < 0 {
		writeError(w, http.StatusBadRequest, "ttl_seconds 不能为负")
		return
	}
	key, err := randomKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	// 强制一次性使用：注册密钥不可复用（每个节点独立生成）
	ek := &store.EnrollKey{
		Key:       key,
		Reusable:  false,
		Tag:       req.Tag,
		CreatedAt: time.Now(),
	}
	if req.TTLSeconds > 0 {
		exp := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)
		ek.ExpiresAt = &exp
	}
	if err := s.deps.Store.CreateEnrollKey(ek); err != nil {
		writeError(w, http.StatusInternalServerError, "保存密钥失败")
		return
	}
	// 响应中附带 ca_hash，方便节点自动化部署时一次请求获取所有注册所需信息。
	resp := struct {
		keyDTO
		CAHash string `json:"ca_hash,omitempty"`
	}{keyDTO: toKeyDTO(ek)}
	if s.deps.CA != nil {
		if h, err := s.deps.CA.CAPublicKeyHash(); err == nil {
			resp.CAHash = h
		}
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleRevokeKey DELETE /admin/api/keys/{key}：吊销密钥。
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, err := s.deps.Store.GetEnrollKey(key); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "密钥不存在")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "查询密钥失败")
		return
	}
	if err := s.deps.Store.RevokeEnrollKey(key); err != nil {
		writeError(w, http.StatusInternalServerError, "吊销密钥失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "key": key})
}

// randomKey 生成 32 字节随机十六进制注册密钥。
func randomKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
