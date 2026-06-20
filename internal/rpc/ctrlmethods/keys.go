package ctrlmethods

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/rpc"
)

// keyDTO is the RPC view of an enrollment key.
type keyDTO struct {
	Key       string     `json:"key"`
	Reusable  bool       `json:"reusable"`
	Tag       string     `json:"tag"`
	Revoked   bool       `json:"revoked"`  // 管理员吊销
	Consumed  bool       `json:"consumed"` // 一次性 key 已被消费
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// createKeyParams is the parameter for keys.create.
type createKeyParams struct {
	Reusable   bool   `json:"reusable"`
	Tag        string `json:"tag"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

// revokeKeyParams is the parameter for keys.revoke.
type revokeKeyParams struct {
	Key string `json:"key"`
}

func registerKeysMethods(s *rpc.Server, deps Deps) {
	s.Register("keys.list", handleKeysList(deps))
	s.Register("keys.create", handleKeysCreate(deps))
	s.Register("keys.revoke", handleKeysRevoke(deps))
}

func handleKeysList(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		keys, err := deps.Store.ListEnrollKeys()
		if err != nil {
			return nil, err
		}
		out := make([]keyDTO, 0, len(keys))
		for i := range keys {
			out = append(out, toKeyDTO(&keys[i]))
		}
		return out, nil
	}
}

func handleKeysCreate(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p createKeyParams
		if params != nil {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
		}
		key, err := randomEnrollKey()
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
		ek := &store.EnrollKey{
			Key:       key,
			Reusable:  p.Reusable,
			Tag:       p.Tag,
			CreatedAt: time.Now(),
		}
		if p.TTLSeconds > 0 {
			exp := time.Now().Add(time.Duration(p.TTLSeconds) * time.Second)
			ek.ExpiresAt = &exp
		}
		if err := deps.Store.CreateEnrollKey(ek); err != nil {
			return nil, err
		}
		return toKeyDTO(ek), nil
	}
}

func handleKeysRevoke(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p revokeKeyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.Key == "" {
			return nil, fmt.Errorf("key is required")
		}
		if err := deps.Store.RevokeEnrollKey(p.Key); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}
}

func toKeyDTO(ek *store.EnrollKey) keyDTO {
	return keyDTO{
		Key:       ek.Key,
		Reusable:  ek.Reusable,
		Tag:       ek.Tag,
		Revoked:   ek.Revoked,
		Consumed:  ek.Consumed,
		CreatedAt: ek.CreatedAt,
		ExpiresAt: ek.ExpiresAt,
	}
}

// randomEnrollKey generates a 32-byte random hex enrollment key.
func randomEnrollKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
