package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// testAuth 预计算低 cost bcrypt hash（cost=4），避免每个测试重复 hash 导致 120s+ 总耗时。
// 密码: "s3cret"
func testAuth(t *testing.T) *Authenticator {
	t.Helper()
	hash := []byte("$2b$04$fWodp96RcEaue5HztmMLRu2tUTwIHaBy5an9oopRFEB7wlIciZ1Ja")
	a, err := NewAuthenticator("admin", hash, []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// ─── writeJSON / writeError 测试 ─────────────────────────────────────────────

func TestWriteJSON(t *testing.T) {
	// 验证 Content-Type、状态码、JSON 编码正确。
	tests := []struct {
		name   string
		status int
		body   any
	}{
		{"200 对象", http.StatusOK, map[string]string{"key": "value"}},
		{"201 数组", http.StatusCreated, []int{1, 2, 3}},
		{"204 空对象", http.StatusNoContent, struct{}{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeJSON(w, tt.status, tt.body)
			if w.Code != tt.status {
				t.Errorf("状态码 = %d, 期望 %d", w.Code, tt.status)
			}
			ct := w.Header().Get("Content-Type")
			if ct != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			// 确认 body 是合法 JSON。
			var raw json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Errorf("body 不是合法 JSON: %v", err)
			}
		})
	}
}

func TestWriteError(t *testing.T) {
	// 验证 writeError 输出 {"error": msg} 格式。
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "参数错误")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400", w.Code)
	}
	var resp errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if resp.Error != "参数错误" {
		t.Errorf("error = %q, 期望 '参数错误'", resp.Error)
	}
}

// ─── randomKey 测试 ──────────────────────────────────────────────────────────

func TestRandomKeyFormat(t *testing.T) {
	// 验证 randomKey 返回 64 字符的十六进制字符串。
	key, err := randomKey()
	if err != nil {
		t.Fatalf("randomKey: %v", err)
	}
	if len(key) != 64 {
		t.Errorf("key 长度 = %d, 期望 64", len(key))
	}
	// 验证是合法十六进制。
	for _, c := range key {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("key 含非法字符 %q", string(c))
			break
		}
	}
}

func TestRandomKeyUnique(t *testing.T) {
	// 两次生成应不同。
	k1, _ := randomKey()
	k2, _ := randomKey()
	if k1 == k2 {
		t.Error("连续两次 randomKey 不应相同")
	}
}

// ─── DTO 转换测试 ─────────────────────────────────────────────────────────────

func TestToKeyDTO(t *testing.T) {
	now := time.Now()
	exp := now.Add(time.Hour)
	ek := &store.EnrollKey{
		Key:       "test-key",
		Reusable:  true,
		Tag:       "team-a",
		Revoked:   false,
		Consumed:  true,
		ExpiresAt: &exp,
		CreatedAt: now,
	}
	dto := toKeyDTO(ek)
	if dto.Key != "test-key" {
		t.Errorf("Key = %q", dto.Key)
	}
	if !dto.Reusable {
		t.Error("Reusable 应为 true")
	}
	if dto.Tag != "team-a" {
		t.Errorf("Tag = %q", dto.Tag)
	}
	if dto.Revoked {
		t.Error("Revoked 应为 false")
	}
	if !dto.Consumed {
		t.Error("Consumed 应为 true")
	}
	if dto.ExpiresAt == nil {
		t.Error("ExpiresAt 不应为 nil")
	}
}

func TestToKeyDTONoExpiry(t *testing.T) {
	// 无过期时间时 ExpiresAt 应为 nil。
	ek := &store.EnrollKey{Key: "no-exp"}
	dto := toKeyDTO(ek)
	if dto.ExpiresAt != nil {
		t.Error("无过期时间时 ExpiresAt 应为 nil")
	}
}

func TestToCertDTO(t *testing.T) {
	now := time.Now()
	revokedAt := now.Add(-10 * time.Minute)
	c := &store.Cert{
		Serial:    "12345",
		NodeID:    "node-1",
		NotAfter:  now.Add(24 * time.Hour),
		Revoked:   true,
		RevokedAt: &revokedAt,
		CreatedAt: now,
	}
	dto := toCertDTO(c)
	if dto.Serial != "12345" {
		t.Errorf("Serial = %q", dto.Serial)
	}
	if dto.NodeID != "node-1" {
		t.Errorf("NodeID = %q", dto.NodeID)
	}
	if !dto.Revoked {
		t.Error("Revoked 应为 true")
	}
	if dto.RevokedAt == nil {
		t.Error("RevokedAt 不应为 nil")
	}
}

func TestToCertDTONotRevoked(t *testing.T) {
	c := &store.Cert{Serial: "999", NodeID: "n2"}
	dto := toCertDTO(c)
	if dto.Revoked {
		t.Error("Revoked 应为 false")
	}
	if dto.RevokedAt != nil {
		t.Error("RevokedAt 应为 nil")
	}
}

// ─── toNodeDTO 测试 ──────────────────────────────────────────────────────────

func TestToNodeDTO(t *testing.T) {
	h := newHarness(t)
	h.online.online["n-online"] = true
	node := &store.Node{
		ID:         "n-online",
		Role:       "node",
		Hostname:   "host-1",
		User:       "alice",
		VirtualIP:  "100.64.0.5/32",
		WGPubKey:   "pk-test",
		Generation: 7,
	}
	dto := h.srv.toNodeDTO(node)
	if dto.ID != "n-online" || dto.Role != "node" || dto.Hostname != "host-1" {
		t.Errorf("基本字段错误: %+v", dto)
	}
	if dto.User != "alice" || dto.VirtualIP != "100.64.0.5/32" || dto.WGPubKey != "pk-test" {
		t.Errorf("其他字段错误: %+v", dto)
	}
	if dto.Generation != 7 {
		t.Errorf("Generation = %d, 期望 7", dto.Generation)
	}
	if !dto.Online {
		t.Error("n-online 应在线")
	}
}

func TestToNodeDTOOffline(t *testing.T) {
	h := newHarness(t)
	node := &store.Node{ID: "n-offline"}
	dto := h.srv.toNodeDTO(node)
	if dto.Online {
		t.Error("n-offline 不应在线")
	}
}

func TestToNodeDTONilOnlineIface(t *testing.T) {
	// Online 接口为 nil 时不 panic，online 默认 false。
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	auth := testAuth(t)
	srv := NewAdminServer(Deps{Auth: auth, Store: st})
	dto := srv.toNodeDTO(&store.Node{ID: "x"})
	if dto.Online {
		t.Error("Online 为 nil 时应默认 false")
	}
}

// ─── handleLogout 测试 ───────────────────────────────────────────────────────

func TestHandleLogout(t *testing.T) {
	h := newHarness(t)
	rec := h.doToken(http.MethodPost, "/admin/api/logout", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, 期望 ok", resp["status"])
	}
}

// ─── handleLogin 边界用例 ────────────────────────────────────────────────────

func TestHandleLoginInvalidJSON(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/login", nil)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400", rec.Code)
	}
}

// ─── createKey 请求验证 ──────────────────────────────────────────────────────

func TestCreateKeyNegativeTTL(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodPost, "/admin/api/keys", createKeyRequest{TTLSeconds: -100})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400", rec.Code)
	}
}

func TestCreateKeyNoTTL(t *testing.T) {
	// TTLSeconds=0 表示永不过期：创建的 key 应无 ExpiresAt。
	h := newHarness(t)
	rec := h.do(http.MethodPost, "/admin/api/keys", createKeyRequest{Tag: "forever"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d, 期望 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ExpiresAt != nil {
		t.Error("TTLSeconds=0 时 ExpiresAt 应为 nil")
	}
}

// ─── 创建 key 时 ca_hash 字段 ────────────────────────────────────────────────

func TestCreateKeyContainsCAHash(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodPost, "/admin/api/keys", createKeyRequest{Tag: "with-hash"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	hash, ok := resp["ca_hash"].(string)
	if !ok || hash == "" {
		t.Errorf("创建 key 响应应包含 ca_hash 字段: %v", resp)
	}
}

// ─── handleGetCA 无 CA 时错误 ────────────────────────────────────────────────

func TestHandleGetCANilCA(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	auth := testAuth(t)
	// CA 为 nil。
	srv := NewAdminServer(Deps{Auth: auth, Store: st})
	tok, _ := auth.Login("admin", "s3cret")
	req := httptest.NewRequest(http.MethodGet, "/admin/api/ca", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, 期望 500", rec.Code)
	}
}

// ─── handleRevokeCert 空 serial 路径 ─────────────────────────────────────────

func TestRevokeCertEmptySerial(t *testing.T) {
	h := newHarness(t)
	// Go 1.22+ ServeMux 中 {serial} 为空字符串时，路径为 /admin/api/certs//revoke。
	// 由于 mux 匹配方式，该路径可能不匹配路由或 serial 为空。
	// 构造显式请求绕过 mux 路由匹配测试 handler 本身。
	req := httptest.NewRequest(http.MethodPost, "/admin/api/certs//revoke", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	// 期望 400 或 404（取决于 mux 路由匹配）。
	if rec.Code == http.StatusOK {
		t.Fatal("空 serial 不应返回 200")
	}
}

// ─── spaHandler 测试 ─────────────────────────────────────────────────────────

func TestSpaHandlerFallbackToIndex(t *testing.T) {
	// spaHandler 对不存在的路径应返回 index.html 内容。
	handler := spaHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/some/random/path", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rec.Code)
	}
	// SPA 嵌入了 dist/index.html，应包含 HTML 内容。
	body := rec.Body.String()
	if body == "" {
		t.Error("spaHandler 返回空 body")
	}
}

func TestSpaFSNotNil(t *testing.T) {
	// 验证嵌入的 spaFS 不为 nil。
	if spaFS == nil {
		t.Fatal("spaFS 为 nil，嵌入 dist 目录失败")
	}
}

// ─── recomputeAll 测试 ───────────────────────────────────────────────────────

func TestRecomputeAllNoNodes(t *testing.T) {
	// 无节点时 recomputeAll 不 panic。
	h := newHarness(t)
	h.srv.recomputeAll()
	if h.notify.called() {
		t.Error("无节点时不应调用 RecomputeAndNotify")
	}
}

func TestRecomputeAllWithNodes(t *testing.T) {
	h := newHarness(t)
	seedNode(t, h.st, "r1", "100.64.0.10/32")
	seedNode(t, h.st, "r2", "100.64.0.11/32")
	h.srv.recomputeAll()
	if !h.notify.called() {
		t.Error("有节点时应调用 RecomputeAndNotify")
	}
	notified := h.notify.allNotified()
	if len(notified) != 2 {
		t.Errorf("notified = %v, 期望 2 个节点", notified)
	}
}

func TestRecomputeAllNilNotify(t *testing.T) {
	// Notify 为 nil 时不 panic。
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	auth := testAuth(t)
	srv := NewAdminServer(Deps{Auth: auth, Store: st})
	srv.recomputeAll() // 不 panic
}

// ─── 路由注册完整性验证 ──────────────────────────────────────────────────────

func TestRoutesRegistered(t *testing.T) {
	// 验证关键路由已注册（通过发起带认证的请求，返回非 404/405 确认）。
	h := newHarness(t)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/admin/api/login"},
		{http.MethodPost, "/admin/api/logout"},
		// 以下受保护路由：无 token 应返回 401（说明路由已注册、进入了中间件）。
	}
	for _, r := range routes {
		rec := h.doToken(r.method, r.path, nil, "")
		// login/logout 是公开端点，返回非 404/405 即可。
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s 返回 %d，路由可能未注册", r.method, r.path, rec.Code)
		}
	}

	// 受保护端点：无 token 时应返回 401。
	protectedRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/api/nodes"},
		{http.MethodGet, "/admin/api/acl"},
		{http.MethodGet, "/admin/api/acl/history"},
		{http.MethodGet, "/admin/api/keys"},
		{http.MethodGet, "/admin/api/relays"},
		{http.MethodGet, "/admin/api/certs"},
		{http.MethodGet, "/admin/api/ca"},
	}
	for _, r := range protectedRoutes {
		rec := h.doToken(r.method, r.path, nil, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 无 token 应返回 401，实际 %d", r.method, r.path, rec.Code)
		}
	}
}

// ─── setTopology 空 neighbors ────────────────────────────────────────────────

func TestSetTopologyEmptyNeighbors(t *testing.T) {
	h := newHarness(t)
	// 先设置拓扑。
	h.do(http.MethodPut, "/admin/api/relays/topology", setTopologyRequest{RelayID: "r1", Neighbors: []string{"r2"}})
	// 清空邻居。
	rec := h.do(http.MethodPut, "/admin/api/relays/topology", setTopologyRequest{RelayID: "r1", Neighbors: []string{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d; body=%s", rec.Code, rec.Body.String())
	}
	links, _ := h.st.ListRelayLinks()
	for _, l := range links {
		if l.RelayID == "r1" {
			t.Error("清空后不应有 r1 的邻接关系")
		}
	}
}

// ─── handleListRelays 使用 Roster 优先 ───────────────────────────────────────

type fakeRoster struct {
	topo map[string][]string
	err  error
}

func (r *fakeRoster) Topology() (map[string][]string, error) { return r.topo, r.err }

func TestListRelaysWithRoster(t *testing.T) {
	h := newHarness(t)
	if err := h.st.UpsertRelayInfo(&store.RelayInfo{NodeID: "r1", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	// 注入 Roster：优先使用 Roster.Topology 的邻接。
	roster := &fakeRoster{topo: map[string][]string{"r1": {"r9", "r10"}}}
	deps := h.srv.deps
	deps.Roster = roster
	srv := NewAdminServer(deps)
	tok, _ := deps.Auth.Login("admin", "s3cret")
	req := httptest.NewRequest(http.MethodGet, "/admin/api/relays", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	resp := decode[struct {
		Relays []relayDTO `json:"relays"`
	}](t, rec)
	if len(resp.Relays) != 1 {
		t.Fatalf("relays len = %d", len(resp.Relays))
	}
	r := resp.Relays[0]
	if len(r.Neighbors) != 2 || r.Neighbors[0] != "r9" {
		t.Errorf("Roster 拓扑未生效: neighbors = %v", r.Neighbors)
	}
}
