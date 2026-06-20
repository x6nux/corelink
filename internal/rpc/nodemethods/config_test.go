package nodemethods

import (
	"encoding/json"
	"testing"

	"github.com/x6nux/corelink/internal/rpc"
)

// TestHandleConfigGet_NilConfig 验证 Config 返回 nil 时不报错
func TestHandleConfigGet_NilConfig(t *testing.T) {
	deps := buildTestDeps()
	deps.Config = func() any { return nil }
	h := handleConfigGet(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// nil 配置应序列化为 JSON null
	b, _ := json.Marshal(result)
	if string(b) != "null" {
		t.Errorf("nil config 应序列化为 null，got %s", string(b))
	}
}

// TestHandleConfigGet_ComplexConfig 验证复杂配置结构可正确返回
func TestHandleConfigGet_ComplexConfig(t *testing.T) {
	deps := buildTestDeps()
	deps.Config = func() any {
		return map[string]any{
			"listen":    ":8080",
			"debug":     true,
			"max_peers": 100,
		}
	}
	h := handleConfigGet(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["listen"] != ":8080" {
		t.Errorf("listen = %v", got["listen"])
	}
	if got["debug"] != true {
		t.Errorf("debug = %v", got["debug"])
	}
}

// TestRegisterConfigMethods_NoPanic 验证 registerConfigMethods 注册不 panic
func TestRegisterConfigMethods_NoPanic(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps()
	registerConfigMethods(srv, deps)
}
