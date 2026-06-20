package nodemethods

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/x6nux/corelink/internal/rpc"
)

func registerDebugMethods(s *rpc.Server, deps Deps) {
	s.Register("debug.block_peer", handleDebugBlockPeer(deps))
	s.Register("debug.unblock_peer", handleDebugUnblockPeer(deps))
	s.Register("debug.list_blocked", handleDebugListBlocked(deps))
}

type peerParam struct {
	PeerID string `json:"peer_id"`
}

// resolvePeerID 在服务端解析 peer 标识——支持完整 nodeID、nodeID 前缀、VIP、hostname。
func resolvePeerID(deps Deps, hint string) (string, error) {
	if deps.Peers == nil {
		return hint, nil
	}
	peers := deps.Peers()
	// 精确匹配 VIP / hostname / 完整 nodeID
	// VIP 可能是 CIDR 格式（如 "100.64.0.3/32"），需要提取地址部分匹配
	for _, p := range peers {
		if p.NodeID == hint || p.Hostname == hint {
			return p.NodeID, nil
		}
		// VIP 匹配：支持 CIDR 格式和纯地址
		if p.VIP == hint {
			return p.NodeID, nil
		}
		if prefix, err := netip.ParsePrefix(p.VIP); err == nil {
			if prefix.Addr().String() == hint {
				return p.NodeID, nil
			}
		}
	}
	// 前缀匹配 nodeID
	var matches []PeerInfo
	for _, p := range peers {
		if strings.HasPrefix(p.NodeID, hint) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return hint, nil // 没匹配到，原样传递
	case 1:
		return matches[0].NodeID, nil
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.NodeID[:min(12, len(m.NodeID))]
		}
		return "", fmt.Errorf("前缀 %q 匹配到多个 peer: %v", hint, ids)
	}
}

func handleDebugBlockPeer(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		if deps.DebugBlockPeer == nil {
			return nil, fmt.Errorf("debug 接口未注入")
		}
		var p peerParam
		if err := json.Unmarshal(params, &p); err != nil || p.PeerID == "" {
			return nil, fmt.Errorf("参数错误: 需要 {\"peer_id\": \"...\"}")
		}
		resolved, err := resolvePeerID(deps, p.PeerID)
		if err != nil {
			return nil, err
		}
		deps.DebugBlockPeer(resolved)
		return map[string]string{"status": "blocked", "peer_id": resolved}, nil
	}
}

func handleDebugUnblockPeer(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		if deps.DebugUnblockPeer == nil {
			return nil, fmt.Errorf("debug 接口未注入")
		}
		var p peerParam
		if err := json.Unmarshal(params, &p); err != nil || p.PeerID == "" {
			return nil, fmt.Errorf("参数错误: 需要 {\"peer_id\": \"...\"}")
		}
		resolved, err := resolvePeerID(deps, p.PeerID)
		if err != nil {
			return nil, err
		}
		deps.DebugUnblockPeer(resolved)
		return map[string]string{"status": "unblocked", "peer_id": resolved}, nil
	}
}

func handleDebugListBlocked(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		if deps.DebugListBlocked == nil {
			return nil, fmt.Errorf("debug 接口未注入")
		}
		blocked := deps.DebugListBlocked()
		if blocked == nil {
			blocked = []string{}
		}
		return map[string]any{"blocked": blocked}, nil
	}
}
