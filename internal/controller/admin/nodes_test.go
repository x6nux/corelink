package admin

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 测试 nodeDTO JSON 标签与 handleGetNode 成功路径。

func TestNodeDTOJSONTags(t *testing.T) {
	// 验证 nodeDTO 的 JSON 键名正确。
	dto := nodeDTO{
		ID: "n1", Role: "node", Hostname: "host",
		User: "alice", VirtualIP: "100.64.0.1/32",
		WGPubKey: "pk1", Generation: 10, Online: true,
	}
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	expected := []string{"id", "role", "hostname", "user", "virtual_ip", "wg_public_key", "generation", "online"}
	for _, key := range expected {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON 中缺少键 %q", key)
		}
	}
}

func TestHandleGetNodeSuccess(t *testing.T) {
	// 验证 GET /admin/api/nodes/{id} 成功返回单个节点。
	h := newHarness(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	h.online.online["n1"] = true

	rec := h.do(http.MethodGet, "/admin/api/nodes/n1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[nodeDTO](t, rec)
	if resp.ID != "n1" {
		t.Errorf("ID = %q, 期望 n1", resp.ID)
	}
	if !resp.Online {
		t.Error("n1 应在线")
	}
}

func TestHandleListNodesEmpty(t *testing.T) {
	// 无节点时返回空数组。
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/admin/api/nodes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	resp := decode[struct {
		Nodes []nodeDTO `json:"nodes"`
	}](t, rec)
	if len(resp.Nodes) != 0 {
		t.Errorf("期望空列表, 实际 %d 个节点", len(resp.Nodes))
	}
}
