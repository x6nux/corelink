package admin

// 端到端冒烟测试：
//   login → GET /admin/api/nodes（带 token）→ PUT /admin/api/acl（触发重算）
//   → GET /（SPA index.html 已嵌入）→ GET /admin/api/nodes（无 token → 401）
//
// 使用 httptest.Server 起真实 HTTP 服务，覆盖整个 admin.Server 栈。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// TestSmokeAdminServer 是 admin server 的端到端冒烟测试。
func TestSmokeAdminServer(t *testing.T) {
	// ─── 准备依赖 ─────────────────────────────────────────────────────────────
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// 预置节点，验证 nodes API 返回正确结果。
	if err := st.CreateNode(&store.Node{
		ID:        "smoke-n1",
		Role:      "node",
		WGPubKey:  "pk-smoke1",
		VirtualIP: "100.64.0.10/32",
		User:      "tester",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	auth, err := NewAuthenticatorFromPassword(
		"admin", "smoke-pass",
		[]byte("smoke-hmac-key-0123456789abcdef"),
		time.Hour,
	)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	spy := &spyNotify{}
	deps := Deps{
		Auth:   auth,
		Store:  st,
		CA:     &spyCA{},
		IPAM:   &spyIPAM{},
		Online: fakeOnline{online: map[string]bool{"smoke-n1": true}},
		Notify: spy,
	}
	srv := NewAdminServer(deps)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	base := ts.URL

	client := &http.Client{Timeout: 10 * time.Second}

	// ─── 1. POST /admin/api/login → 获取 token ────────────────────────────────
	t.Log("step 1: login")
	loginBody, _ := json.Marshal(loginRequest{User: "admin", Password: "smoke-pass"})
	resp, err := client.Post(base+"/admin/api/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login: status=%d body=%s", resp.StatusCode, body)
	}
	var lr loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatalf("login decode: %v", err)
	}
	if lr.Token == "" {
		t.Fatal("login: got empty token")
	}
	token := lr.Token
	t.Logf("token obtained (len=%d)", len(token))

	// ─── 2. GET /admin/api/nodes（带 token）→ 200 + JSON ────────────────────
	t.Log("step 2: GET /admin/api/nodes with token")
	req, _ := http.NewRequest(http.MethodGet, base+"/admin/api/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET nodes: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("GET nodes: status=%d body=%s", resp2.StatusCode, body)
	}
	var nodesResp struct {
		Nodes []nodeDTO `json:"nodes"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&nodesResp); err != nil {
		t.Fatalf("nodes decode: %v", err)
	}
	if len(nodesResp.Nodes) != 1 {
		t.Fatalf("nodes: got %d, want 1", len(nodesResp.Nodes))
	}
	if nodesResp.Nodes[0].ID != "smoke-n1" {
		t.Errorf("node ID = %q, want smoke-n1", nodesResp.Nodes[0].ID)
	}
	if !nodesResp.Nodes[0].Online {
		t.Error("smoke-n1 should be online")
	}
	t.Logf("nodes ok, node=%+v", nodesResp.Nodes[0])

	// ─── 3. PUT /admin/api/acl（合法策略）→ 触发重算 ─────────────────────────
	t.Log("step 3: PUT /admin/api/acl to trigger recompute")
	const goodACL = `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	req3, _ := http.NewRequest(http.MethodPut, base+"/admin/api/acl", strings.NewReader(goodACL))
	req3.Header.Set("Authorization", "Bearer "+token)
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("PUT acl: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp3.Body)
		t.Fatalf("PUT acl: status=%d body=%s", resp3.StatusCode, body)
	}
	if !spy.called() {
		t.Error("PUT acl should have triggered RecomputeAndNotify")
	}
	t.Log("acl recompute triggered ok")

	// ─── 4. GET /（SPA fallback）→ 含 <!DOCTYPE html> ────────────────────────
	t.Log("step 4: GET / → SPA index.html")
	resp4, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status=%d", resp4.StatusCode)
	}
	body4, _ := io.ReadAll(resp4.Body)
	html := string(body4)
	if !strings.Contains(strings.ToLower(html), "<!doctype html>") {
		t.Errorf("GET / did not return HTML; body start: %.200s", html)
	}
	if !strings.Contains(html, "root") {
		t.Errorf("GET / missing root div; body: %.200s", html)
	}
	t.Log("SPA embed ok")

	// ─── 5. GET /admin/api/nodes（无 token）→ 401 ────────────────────────────
	t.Log("step 5: GET /admin/api/nodes without token → 401")
	resp5, err := client.Get(base + "/admin/api/nodes")
	if err != nil {
		t.Fatalf("GET nodes no-auth: %v", err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: status=%d, want 401", resp5.StatusCode)
	}
	t.Log("unauthenticated 401 ok")

	t.Log("smoke test passed: login → nodes → acl-recompute → SPA → 401")
}
