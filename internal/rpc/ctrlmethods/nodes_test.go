package ctrlmethods

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// TestBuildIngressDTOs_MultipleIngresses 验证 buildIngressDTOs 正确映射多个 ingress
func TestBuildIngressDTOs_MultipleIngresses(t *testing.T) {
	ing := &mockIngress{
		sets: map[string]*genv1.IngressSet{
			"n1": {
				NodeId: "n1",
				Ingresses: []*genv1.Ingress{
					{Id: "i1", Host: "1.1.1.1", Port: 443, Source: genv1.IngressSource_INGRESS_SOURCE_STUN, Confidence: 90, NatType: genv1.NatType_NAT_TYPE_FULL_CONE},
					{Id: "i2", Host: "2.2.2.2", Port: 8443, Source: genv1.IngressSource_INGRESS_SOURCE_UPNP, Confidence: 100, NatType: genv1.NatType_NAT_TYPE_OPEN},
				},
			},
		},
	}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, ing)
	dtos := buildIngressDTOs(deps, "n1")
	if len(dtos) != 2 {
		t.Fatalf("len = %d, want 2", len(dtos))
	}
	if dtos[0].Host != "1.1.1.1" || dtos[0].Port != 443 {
		t.Errorf("dtos[0] = %+v", dtos[0])
	}
	if dtos[1].Host != "2.2.2.2" || dtos[1].Port != 8443 {
		t.Errorf("dtos[1] = %+v", dtos[1])
	}
}

// TestBuildIngressDTOs_UnknownNode 验证不存在的节点返回空数组
func TestBuildIngressDTOs_UnknownNode(t *testing.T) {
	ing := &mockIngress{sets: map[string]*genv1.IngressSet{}}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, ing)
	dtos := buildIngressDTOs(deps, "nonexistent")
	if len(dtos) != 0 {
		t.Errorf("不存在节点应返回空数组，got %d", len(dtos))
	}
}

// TestHandleNodesList_NilOnline 验证 Online 为 nil 时节点列表所有节点 online=false
func TestHandleNodesList_NilOnline(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{
			{ID: "n1", Hostname: "h1", VirtualIP: "100.64.0.1", Role: "node"},
		},
	}
	// 直接构造 Deps 确保 Online 接口值为 nil（避免 nil *mockOnline 包装成非 nil 接口）
	deps := Deps{
		Store:     ms,
		StartTime: time.Now(),
	}
	h := handleNodesList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []nodeDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Online {
		t.Error("Online 为 nil 时节点不应标记为在线")
	}
}

// TestHandleNodesDelete_InvalidJSON 验证无效 JSON 参数返回错误
func TestHandleNodesDelete_InvalidJSON(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleNodesDelete(deps)
	_, err := h(json.RawMessage(`not-json`))
	if err == nil {
		t.Fatal("无效 JSON 应返回错误")
	}
}
