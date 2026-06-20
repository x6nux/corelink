package nodemethods

import (
	"encoding/json"
	"testing"

	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandleIngressList_SingleItem 验证单个 ingress 条目正确返回
func TestHandleIngressList_SingleItem(t *testing.T) {
	deps := buildTestDeps()
	deps.Ingresses = func() []IngressInfo {
		return []IngressInfo{
			{Host: "10.0.0.1", Port: 8443, Source: "NETIF", Confidence: 50, NATType: "SYMMETRIC"},
		}
	}
	h := handleIngressList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []IngressInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Host != "10.0.0.1" || got[0].NATType != "SYMMETRIC" {
		t.Errorf("ingress = %+v", got[0])
	}
}

// TestHandleIngressList_EmptySlice 验证空切片正常返回
func TestHandleIngressList_EmptySlice(t *testing.T) {
	deps := buildTestDeps()
	deps.Ingresses = func() []IngressInfo { return []IngressInfo{} }
	h := handleIngressList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []IngressInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空切片应返回空数组，got %d", len(got))
	}
}

// TestRegisterIngressMethods_NoPanic 验证注册不 panic
func TestRegisterIngressMethods_NoPanic(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps()
	registerIngressMethods(srv, deps)
}
