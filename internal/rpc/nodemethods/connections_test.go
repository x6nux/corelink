package nodemethods

import (
	"encoding/json"
	"testing"

	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandleConnectionsList_EmptySlice 验证返回空切片时序列化为空数组
func TestHandleConnectionsList_EmptySlice(t *testing.T) {
	deps := buildTestDeps()
	deps.Connections = func() []ConnectionInfo { return []ConnectionInfo{} }
	h := handleConnectionsList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []ConnectionInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空切片应返回空数组，got %d", len(got))
	}
}

// TestHandleConnectionsList_FieldMapping 验证所有字段正确映射
func TestHandleConnectionsList_FieldMapping(t *testing.T) {
	deps := buildTestDeps()
	deps.Connections = func() []ConnectionInfo {
		return []ConnectionInfo{
			{
				PeerID:     "p1",
				VIP:        "100.64.0.2",
				PeerIP:     "203.0.113.10:51820",
				InternalIP: "192.168.1.10:51820",
				LinkType:   "direct",
				RTTms:      3,
				RTTValid:   true,
				Loss:       5,
				LossValid:  true,
				State:      "active",
			},
		}
	}
	h := handleConnectionsList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []ConnectionInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	c := got[0]
	if c.PeerID != "p1" || c.VIP != "100.64.0.2" || c.PeerIP != "203.0.113.10:51820" || c.InternalIP != "192.168.1.10:51820" || c.LinkType != "direct" {
		t.Errorf("字段不匹配: %+v", c)
	}
	if c.RTTms != 3 || !c.RTTValid || c.Loss != 5 || !c.LossValid || c.State != "active" {
		t.Errorf("数值字段不匹配: %+v", c)
	}
}

// TestRegisterConnectionsMethods_NoPanic 验证注册不 panic
func TestRegisterConnectionsMethods_NoPanic(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps()
	registerConnectionsMethods(srv, deps)
}
