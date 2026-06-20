package ctrlmethods

import (
	"encoding/json"
	"testing"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandleACLGet_EmptyPolicies 验证无策略时 acl.get 返回空版本
func TestHandleACLGet_EmptyPolicies(t *testing.T) {
	ms := &mockStore{policies: []store.ACLPolicy{}}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleACLGet(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got aclDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 0 {
		t.Errorf("空策略列表的 version 应为 0，got %d", got.Version)
	}
}

// TestHandleACLSet_InvalidJSON 验证无效 JSON 参数返回错误
func TestHandleACLSet_InvalidJSON(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleACLSet(deps)
	_, err := h(json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("无效 JSON 应返回错误")
	}
}

// TestHandleACLHistory_EmptyPolicies 验证无策略时历史返回空数组
func TestHandleACLHistory_EmptyPolicies(t *testing.T) {
	ms := &mockStore{policies: []store.ACLPolicy{}}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleACLHistory(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []aclDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空策略列表应返回空数组，got %d", len(got))
	}
}

// TestRegisterACLMethods_NoPanic 验证 registerACLMethods 不 panic
func TestRegisterACLMethods_NoPanic(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	registerACLMethods(srv, deps)
}
