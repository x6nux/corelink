package nodemethods

import (
	"encoding/json"
	"testing"

	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandlePortmapStatus_InactiveState 验证 portmap.status 非激活状态
func TestHandlePortmapStatus_InactiveState(t *testing.T) {
	deps := buildTestDeps()
	deps.PortmapStatus = func() PortmapStatusInfo {
		return PortmapStatusInfo{Active: false, ManagedCount: 0}
	}
	h := handlePortmapStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got PortmapStatusInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Active {
		t.Error("active 应为 false")
	}
	if got.ManagedCount != 0 {
		t.Errorf("managed_count = %d, want 0", got.ManagedCount)
	}
}

// TestHandlePortmapList_SingleMapping 验证单个映射正确返回
func TestHandlePortmapList_SingleMapping(t *testing.T) {
	deps := buildTestDeps()
	deps.PortmapMappings = func() []MappingInfo {
		return []MappingInfo{
			{Protocol: "PCP", ExternalIP: "5.5.5.5", ExternalPort: 9999, InternalPort: 8888, Transport: "UDP", TTL: "3600s", RenewIn: "1800s"},
		}
	}
	h := handlePortmapList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []MappingInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Protocol != "PCP" || got[0].ExternalPort != 9999 {
		t.Errorf("mapping = %+v", got[0])
	}
}

// TestRegisterPortmapMethods_NoPanic 验证注册不 panic
func TestRegisterPortmapMethods_NoPanic(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps()
	registerPortmapMethods(srv, deps)
}
