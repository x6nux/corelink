package ctrlmethods

import (
	"encoding/json"
	"fmt"

	"github.com/x6nux/corelink/internal/rpc"
)

// nodeDTO is the RPC view of a node.
type nodeDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Remark     string `json:"remark,omitempty"`
	Hostname   string `json:"hostname"`
	VIP        string `json:"vip"`
	Role       string `json:"role"`
	Online     bool   `json:"online"`
	Generation uint64 `json:"generation"`
}

// ingressDTO is the RPC view of an ingress entry.
type ingressDTO struct {
	Host       string `json:"host"`
	Port       uint32 `json:"port"`
	Source     string `json:"source"`
	Confidence uint32 `json:"confidence"`
	NatType    string `json:"nat_type"`
}

// nodeDetailDTO is the detailed RPC view of a node (includes ingresses).
type nodeDetailDTO struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Remark     string       `json:"remark,omitempty"`
	Hostname   string       `json:"hostname"`
	VIP        string       `json:"vip"`
	Role       string       `json:"role"`
	Online     bool         `json:"online"`
	Generation uint64       `json:"generation"`
	Ingresses  []ingressDTO `json:"ingresses"`
}

// nodesGetParams is the parameter for nodes.get / nodes.ingresses / nodes.delete.
type nodeIDParams struct {
	ID string `json:"id"`
}

func registerNodesMethods(s *rpc.Server, deps Deps) {
	s.Register("nodes.list", handleNodesList(deps))
	s.Register("nodes.get", handleNodesGet(deps))
	s.Register("nodes.delete", handleNodesDelete(deps))
	s.Register("nodes.ingresses", handleNodesIngresses(deps))
}

func handleNodesList(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		nodes, err := deps.Store.ListNodes()
		if err != nil {
			return nil, err
		}
		out := make([]nodeDTO, 0, len(nodes))
		for _, n := range nodes {
			online := false
			if deps.Online != nil {
				online = deps.Online.IsOnline(n.ID)
			}
			out = append(out, nodeDTO{
				ID:         n.ID,
				Name:       n.Name,
				Remark:     n.Remark,
				Hostname:   n.Hostname,
				VIP:        n.VirtualIP,
				Role:       n.Role,
				Online:     online,
				Generation: n.Generation,
			})
		}
		return out, nil
	}
}

func handleNodesGet(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p nodeIDParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		n, err := deps.Store.GetNode(p.ID)
		if err != nil {
			return nil, err
		}
		online := false
		if deps.Online != nil {
			online = deps.Online.IsOnline(n.ID)
		}

		ingresses := buildIngressDTOs(deps, n.ID)

		return nodeDetailDTO{
			ID:         n.ID,
			Name:       n.Name,
			Remark:     n.Remark,
			Hostname:   n.Hostname,
			VIP:        n.VirtualIP,
			Role:       n.Role,
			Online:     online,
			Generation: n.Generation,
			Ingresses:  ingresses,
		}, nil
	}
}

func handleNodesDelete(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p nodeIDParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		if err := deps.Store.DeleteNode(p.ID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	}
}

func handleNodesIngresses(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p nodeIDParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		return buildIngressDTOs(deps, p.ID), nil
	}
}

// buildIngressDTOs extracts ingress info for a node from the Ingress interface.
func buildIngressDTOs(deps Deps, nodeID string) []ingressDTO {
	if deps.Ingress == nil {
		return []ingressDTO{}
	}
	set, ok := deps.Ingress.GetIngressSet(nodeID)
	if !ok || set == nil {
		return []ingressDTO{}
	}
	out := make([]ingressDTO, 0, len(set.Ingresses))
	for _, ing := range set.Ingresses {
		out = append(out, ingressDTO{
			Host:       ing.Host,
			Port:       ing.Port,
			Source:     ing.Source.String(),
			Confidence: ing.Confidence,
			NatType:    ing.NatType.String(),
		})
	}
	return out
}
