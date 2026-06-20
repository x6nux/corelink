package nodemethods

import (
	"encoding/json"
	"fmt"

	"github.com/x6nux/corelink/internal/rpc"
)

// routeTraceParams is the request for route.trace.
type routeTraceParams struct {
	Dst string `json:"dst"`
}

// routeTraceResult is the response for route.trace.
type routeTraceResult struct {
	Paths []RouteInfo `json:"paths"`
}

func registerRouteMethods(s *rpc.Server, deps Deps) {
	s.Register("route.trace", handleRouteTrace(deps))
	s.Register("route.peers", handleRoutePeers(deps))
}

func handleRouteTrace(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p routeTraceParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
		}
		if p.Dst == "" {
			return nil, fmt.Errorf("dst is required")
		}
		routes := deps.Routes(p.Dst)
		if routes == nil {
			routes = []RouteInfo{}
		}
		return routeTraceResult{Paths: routes}, nil
	}
}

func handleRoutePeers(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		peers := deps.Peers()
		if peers == nil {
			peers = []PeerInfo{}
		}
		return peers, nil
	}
}
