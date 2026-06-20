package nodemethods

import (
	"encoding/json"
	"testing"

	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandleRouteTrace_EmptyDstParam 验证空 dst 参数返回错误
func TestHandleRouteTrace_EmptyDstParam(t *testing.T) {
	deps := buildTestDeps()
	h := handleRouteTrace(deps)
	_, err := h(mustMarshal(t, routeTraceParams{Dst: ""}))
	if err == nil {
		t.Fatal("空 dst 应返回错误")
	}
}

// TestHandleRouteTrace_SinglePath 验证单路径路由追踪
func TestHandleRouteTrace_SinglePath(t *testing.T) {
	deps := buildTestDeps()
	deps.Routes = func(dst string) []RouteInfo {
		if dst == "target" {
			return []RouteInfo{
				{
					Hops:      []HopInfo{{NodeID: "target", IngressID: "i1", Host: "10.0.0.1", Port: 443}},
					TotalHops: 1,
					Active:    true,
				},
			}
		}
		return nil
	}
	h := handleRouteTrace(deps)
	result, err := h(mustMarshal(t, routeTraceParams{Dst: "target"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got routeTraceResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Paths) != 1 {
		t.Fatalf("paths len = %d, want 1", len(got.Paths))
	}
	if !got.Paths[0].Active || got.Paths[0].TotalHops != 1 {
		t.Errorf("path = %+v", got.Paths[0])
	}
}

// TestHandleRoutePeers_FieldMapping 验证 peers 字段映射
func TestHandleRoutePeers_FieldMapping(t *testing.T) {
	deps := buildTestDeps()
	deps.Peers = func() []PeerInfo {
		return []PeerInfo{
			{NodeID: "node-x", Hostname: "host-x", VIP: "100.64.1.1"},
		}
	}
	h := handleRoutePeers(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []PeerInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].NodeID != "node-x" || got[0].VIP != "100.64.1.1" {
		t.Errorf("peer = %+v", got[0])
	}
}

// TestRegisterRouteMethods_NoPanic 验证注册不 panic
func TestRegisterRouteMethods_NoPanic(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps()
	registerRouteMethods(srv, deps)
}
