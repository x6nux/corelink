package ctrlmethods

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// aclDTO is the RPC view of an ACL policy.
type aclDTO struct {
	Version   uint      `json:"version"`
	Document  string    `json:"document"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// aclSetParams is the parameter for acl.set.
type aclSetParams struct {
	Document string `json:"document"`
	Author   string `json:"author"`
}

func registerACLMethods(s *rpc.Server, deps Deps) {
	s.Register("acl.get", handleACLGet(deps))
	s.Register("acl.set", handleACLSet(deps))
	s.Register("acl.history", handleACLHistory(deps))
}

func handleACLGet(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		p, err := deps.Store.GetLatestACLPolicy()
		if err != nil {
			return nil, err
		}
		return aclDTO{
			Version:  p.Version,
			Document: p.Document,
			Author:   p.Author,
		}, nil
	}
}

func handleACLSet(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p aclSetParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.Document == "" {
			return nil, fmt.Errorf("document is required")
		}
		author := p.Author
		if author == "" {
			author = "rpc"
		}
		saved, err := deps.Store.SaveACLPolicy(p.Document, author)
		if err != nil {
			return nil, err
		}
		return aclDTO{
			Version:  saved.Version,
			Document: saved.Document,
			Author:   saved.Author,
		}, nil
	}
}

func handleACLHistory(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		pols, err := deps.Store.ListACLPolicies()
		if err != nil {
			return nil, err
		}
		out := make([]aclDTO, 0, len(pols))
		for _, p := range pols {
			out = append(out, aclDTO{
				Version:   p.Version,
				Document:  p.Document,
				Author:    p.Author,
				CreatedAt: p.CreatedAt,
			})
		}
		return out, nil
	}
}
