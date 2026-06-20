package ctrlmethods

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// topoStatusResult is the response for topo.status.
type topoStatusResult struct {
	Version       uint64    `json:"version"`
	TransitCount  int       `json:"transit_count"`
	LeafCount     int       `json:"leaf_count"`
	LastRecompute time.Time `json:"last_recompute"`
}

// tracerouteParams is the parameter for topo.traceroute.
type tracerouteParams struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// tracerouteHop is a single hop in a traceroute path.
type tracerouteHop struct {
	NodeID    string `json:"node_id"`
	IngressID string `json:"ingress_id"`
	Host      string `json:"host"`
	Port      uint32 `json:"port"`
}

// traceroutePath is a single route path from src to dst.
type traceroutePath struct {
	Hops      []tracerouteHop `json:"hops"`
	TotalHops int             `json:"total_hops"`
	Active    bool            `json:"active"`
}

// tracerouteResult is the response for topo.traceroute.
type tracerouteResult struct {
	Paths []traceroutePath `json:"paths"`
}

// topoGraphNode 拓扑图节点。
type topoGraphNode struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	VIP      string `json:"vip"`
	Role     string `json:"role"`
	Online   bool   `json:"online"`
}

// topoGraphEdge 拓扑图边（relay 邻接链路）。
type topoGraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// topoGraphResult topo.graph 响应。
type topoGraphResult struct {
	Nodes []topoGraphNode `json:"nodes"`
	Edges []topoGraphEdge `json:"edges"`
}

func registerTopoMethods(s *rpc.Server, deps Deps) {
	s.Register("topo.status", handleTopoStatus(deps))
	s.Register("topo.graph", handleTopoGraph(deps))
	s.Register("topo.traceroute", handleTopoTraceroute(deps))
}

func handleTopoStatus(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		if deps.Topo == nil {
			return topoStatusResult{}, nil
		}
		ts := deps.Topo.Status()
		return topoStatusResult{
			Version:       ts.Version,
			TransitCount:  ts.TransitCount,
			LeafCount:     ts.LeafCount,
			LastRecompute: ts.LastRecompute,
		}, nil
	}
}

func handleTopoGraph(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		nodes, err := deps.Store.ListNodes()
		if err != nil {
			return nil, err
		}

		gNodes := make([]topoGraphNode, 0, len(nodes))
		for _, n := range nodes {
			online := false
			if deps.Online != nil {
				online = deps.Online.IsOnline(n.ID)
			}
			gNodes = append(gNodes, topoGraphNode{
				ID:       n.ID,
				Hostname: n.Hostname,
				VIP:      n.VirtualIP,
				Role:     n.Role,
				Online:   online,
			})
		}

		var gEdges []topoGraphEdge
		if links, err := deps.Store.ListRelayLinks(); err == nil {
			for _, l := range links {
				gEdges = append(gEdges, topoGraphEdge{
					From: l.RelayID,
					To:   l.NeighborID,
				})
			}
		}
		if gEdges == nil {
			gEdges = []topoGraphEdge{}
		}

		return topoGraphResult{Nodes: gNodes, Edges: gEdges}, nil
	}
}

func handleTopoTraceroute(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p tracerouteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if p.Src == "" || p.Dst == "" {
			return nil, fmt.Errorf("src and dst are required")
		}
		if deps.Topo == nil {
			return tracerouteResult{Paths: []traceroutePath{}}, nil
		}

		assignment, ok := deps.Topo.AssignmentForNode(p.Src)
		if !ok || assignment == nil {
			return tracerouteResult{Paths: []traceroutePath{}}, nil
		}

		var paths []traceroutePath
		for i, route := range assignment.BaselineRoutes {
			if route.DstNode != p.Dst {
				continue
			}
			hops := make([]tracerouteHop, 0, len(route.Hops))
			for _, h := range route.Hops {
				hop := tracerouteHop{
					NodeID:    h.NodeId,
					IngressID: h.IngressId,
				}
				// Enrich with ingress details if available.
				if deps.Ingress != nil {
					if set, ok := deps.Ingress.GetIngressSet(h.NodeId); ok && set != nil {
						for _, ing := range set.Ingresses {
							if ing.Id == h.IngressId {
								hop.Host = ing.Host
								hop.Port = ing.Port
								break
							}
						}
					}
				}
				hops = append(hops, hop)
			}
			paths = append(paths, traceroutePath{
				Hops:      hops,
				TotalHops: len(hops),
				Active:    i == 0, // first route is the active/best one
			})
		}
		if paths == nil {
			paths = []traceroutePath{}
		}
		return tracerouteResult{Paths: paths}, nil
	}
}
