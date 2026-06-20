package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// ─── 测试替身 ─────────────────────────────────────────────────────────────────

// spyNotify 记录 RecomputeAndNotify 被调用的节点 ID。
type spyNotify struct {
	mu    sync.Mutex
	calls [][]string
}

func (n *spyNotify) RecomputeAndNotify(ids ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := append([]string(nil), ids...)
	n.calls = append(n.calls, cp)
}

func (n *spyNotify) allNotified() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	seen := map[string]struct{}{}
	for _, c := range n.calls {
		for _, id := range c {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (n *spyNotify) called() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls) > 0
}

// fakeOnline 让指定节点在线。
type fakeOnline struct{ online map[string]bool }

func (o fakeOnline) IsOnline(id string) bool { return o.online[id] }

// spyCA 记录吊销的序列号。
type spyCA struct {
	mu      sync.Mutex
	revoked []string
}

func (c *spyCA) Revoke(serial string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = append(c.revoked, serial)
	return nil
}
func (c *spyCA) CACertPEM() ([]byte, error)       { return []byte("-----CA PEM-----"), nil }
func (c *spyCA) CAPublicKeyHash() (string, error) { return "sha256:deadbeef", nil }

func (c *spyCA) wasRevoked(serial string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.revoked {
		if s == serial {
			return true
		}
	}
	return false
}

// spyIPAM 记录释放的 IP。
type spyIPAM struct {
	mu       sync.Mutex
	released []string
}

func (p *spyIPAM) Release(ip string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released = append(p.released, ip)
	return nil
}
func (p *spyIPAM) wasReleased(ip string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.released {
		if s == ip {
			return true
		}
	}
	return false
}

// failDeleteStore 包装真实 store，可让 DeleteNode 按需失败，用于验证 #16：
// DeleteNode 失败时不应已吊证/放 IP。
type failDeleteStore struct {
	*store.Store
	failDelete bool
}

func (f *failDeleteStore) DeleteNode(id string) error {
	if f.failDelete {
		return errors.New("注入的删除失败")
	}
	return f.Store.DeleteNode(id)
}

// ─── 测试装置 ─────────────────────────────────────────────────────────────────

type harness struct {
	srv    *Server
	st     *store.Store
	notify *spyNotify
	ca     *spyCA
	ipam   *spyIPAM
	online fakeOnline
	token  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	auth := testAuth(t)
	notify := &spyNotify{}
	caSpy := &spyCA{}
	ipamSpy := &spyIPAM{}
	online := fakeOnline{online: map[string]bool{}}
	deps := Deps{
		Auth:   auth,
		Store:  st,
		CA:     caSpy,
		IPAM:   ipamSpy,
		Online: online,
		Notify: notify,
	}
	srv := NewAdminServer(deps)
	tok, err := auth.Login("admin", "s3cret")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return &harness{srv: srv, st: st, notify: notify, ca: caSpy, ipam: ipamSpy, online: online, token: tok}
}

// do 发起带认证的请求。
func (h *harness) do(method, path string, body any) *httptest.ResponseRecorder {
	return h.doToken(method, path, body, h.token)
}

func (h *harness) doToken(method, path string, body any, token string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return v
}

// ─── login ────────────────────────────────────────────────────────────────────

func TestHandleLoginSuccess(t *testing.T) {
	h := newHarness(t)
	rec := h.doToken(http.MethodPost, "/admin/api/login", loginRequest{User: "admin", Password: "s3cret"}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[loginResponse](t, rec)
	if resp.Token == "" {
		t.Fatal("empty token")
	}
}

func TestHandleLoginBadPassword(t *testing.T) {
	h := newHarness(t)
	rec := h.doToken(http.MethodPost, "/admin/api/login", loginRequest{User: "admin", Password: "nope"}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// ─── 未认证拦截 ───────────────────────────────────────────────────────────────

func TestProtectedRejectsUnauthenticated(t *testing.T) {
	h := newHarness(t)
	for _, ep := range []struct {
		method, path string
	}{
		{http.MethodGet, "/admin/api/nodes"},
		{http.MethodGet, "/admin/api/acl"},
		{http.MethodGet, "/admin/api/keys"},
		{http.MethodGet, "/admin/api/relays"},
		{http.MethodGet, "/admin/api/certs"},
		{http.MethodGet, "/admin/api/ca"},
	} {
		rec := h.doToken(ep.method, ep.path, nil, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", ep.method, ep.path, rec.Code)
		}
	}
}

// ─── nodes ────────────────────────────────────────────────────────────────────

func seedNode(t *testing.T, st *store.Store, id, ip string) {
	t.Helper()
	if err := st.CreateNode(&store.Node{ID: id, Role: "node", WGPubKey: "pk-" + id, VirtualIP: ip, User: "alice"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
}

func TestListNodesWithOnline(t *testing.T) {
	h := newHarness(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	seedNode(t, h.st, "n2", "100.64.0.3/32")
	h.online.online["n1"] = true

	rec := h.do(http.MethodGet, "/admin/api/nodes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	resp := decode[struct {
		Nodes []nodeDTO `json:"nodes"`
	}](t, rec)
	if len(resp.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(resp.Nodes))
	}
	byID := map[string]nodeDTO{}
	for _, n := range resp.Nodes {
		byID[n.ID] = n
	}
	if !byID["n1"].Online {
		t.Error("n1 should be online")
	}
	if byID["n2"].Online {
		t.Error("n2 should be offline")
	}
	if byID["n1"].VirtualIP != "100.64.0.2/32" || byID["n1"].User != "alice" {
		t.Errorf("n1 fields wrong: %+v", byID["n1"])
	}
}

func TestGetNodeNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/admin/api/nodes/ghost", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteNodeSideEffects(t *testing.T) {
	h := newHarness(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	seedNode(t, h.st, "n2", "100.64.0.3/32")
	// 给 n1 分配租约 + 证书。
	if err := h.st.AllocateLease("100.64.0.2", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordCert(&store.Cert{Serial: "555", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodDelete, "/admin/api/nodes/n1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// 副作用 1：Node 被删。
	if _, err := h.st.GetNode("n1"); err == nil {
		t.Error("n1 should be deleted")
	}
	// 副作用 2：证书被吊销。
	if !h.ca.wasRevoked("555") {
		t.Error("cert 555 should be revoked")
	}
	// 副作用 3：IP 被释放。
	if !h.ipam.wasReleased("100.64.0.2") {
		t.Error("IP should be released")
	}
	// 副作用 4：其余节点（n2）被通知重算。
	notified := h.notify.allNotified()
	if len(notified) != 1 || notified[0] != "n2" {
		t.Errorf("notified = %v, want [n2]", notified)
	}
}

// newHarnessWithFailStore 复用 newHarness 的装配，但替换 deps.Store 为给定包装。
func newHarnessWithFailStore(t *testing.T) (*harness, *failDeleteStore) {
	t.Helper()
	h := newHarness(t)
	fs := &failDeleteStore{Store: h.st}
	deps := Deps{
		Auth:   h.srv.deps.Auth,
		Store:  fs,
		CA:     h.ca,
		IPAM:   h.ipam,
		Online: h.online,
		Notify: h.notify,
	}
	h.srv = NewAdminServer(deps)
	return h, fs
}

// TestDeleteNodeFailDoesNotReleaseOrRevoke 验证 #16：DeleteNode 失败时
// 既不吊证也不放 IP（IP 不进空闲池 → 无虚拟 IP 冲突）。
func TestDeleteNodeFailDoesNotReleaseOrRevoke(t *testing.T) {
	h, fs := newHarnessWithFailStore(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	if err := h.st.AllocateLease("100.64.0.2", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordCert(&store.Cert{Serial: "777", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fs.failDelete = true

	rec := h.do(http.MethodDelete, "/admin/api/nodes/n1", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	// 节点未删 → IP 唯一索引仍占用，不可被重新分配。
	if _, err := h.st.GetNode("n1"); err != nil {
		t.Errorf("n1 应仍存在，got err=%v", err)
	}
	// 证书未被吊销。
	if h.ca.wasRevoked("777") {
		t.Error("DeleteNode 失败后不应吊证 777")
	}
	// IP 未进空闲池。
	if h.ipam.wasReleased("100.64.0.2") {
		t.Error("DeleteNode 失败后不应释放 IP")
	}
	// 不应通知其余节点。
	if h.notify.called() {
		t.Error("DeleteNode 失败后不应触发重算下发")
	}
}

// TestDeleteNodeSuccessStillReleases 验证修复后成功路径副作用不变。
func TestDeleteNodeSuccessStillReleases(t *testing.T) {
	h, _ := newHarnessWithFailStore(t) // failDelete 默认 false
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	seedNode(t, h.st, "n2", "100.64.0.3/32")
	if err := h.st.AllocateLease("100.64.0.2", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordCert(&store.Cert{Serial: "778", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodDelete, "/admin/api/nodes/n1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := h.st.GetNode("n1"); err == nil {
		t.Error("n1 应被删除")
	}
	if !h.ca.wasRevoked("778") {
		t.Error("证书 778 应被吊销")
	}
	if !h.ipam.wasReleased("100.64.0.2") {
		t.Error("IP 应被释放")
	}
	if notified := h.notify.allNotified(); len(notified) != 1 || notified[0] != "n2" {
		t.Errorf("notified = %v, want [n2]", notified)
	}
}

func TestDeleteNodeNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodDelete, "/admin/api/nodes/ghost", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ─── acl ──────────────────────────────────────────────────────────────────────

const validACL = `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
const badACL = `{"acls":[{"action":"deny","src":["*"],"dst":["*:*"]}]}`

func TestGetACLEmpty(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/admin/api/acl", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	resp := decode[aclDTO](t, rec)
	if resp.Version != 0 {
		t.Errorf("empty policy version = %d, want 0", resp.Version)
	}
}

func TestPutACLValidTriggersRecompute(t *testing.T) {
	h := newHarness(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	seedNode(t, h.st, "n2", "100.64.0.3/32")

	req := httptest.NewRequest(http.MethodPut, "/admin/api/acl", bytes.NewBufferString(validACL))
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[aclDTO](t, rec)
	if resp.Version == 0 {
		t.Error("expected non-zero version after save")
	}
	// 副作用：全网重算。
	if !h.notify.called() {
		t.Error("PUT acl should trigger RecomputeAndNotify")
	}
	notified := h.notify.allNotified()
	if len(notified) != 2 {
		t.Errorf("notified = %v, want both nodes", notified)
	}
	// 持久化检查。
	p, _ := h.st.GetLatestACLPolicy()
	if p.Document != validACL {
		t.Errorf("stored doc mismatch")
	}
	if p.Author != "admin" {
		t.Errorf("author = %q, want admin", p.Author)
	}
}

func TestPutACLInvalidRejected(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/acl", bytes.NewBufferString(badACL))
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// 副作用：不应触发重算，不应持久化。
	if h.notify.called() {
		t.Error("invalid ACL must not trigger recompute")
	}
	p, _ := h.st.GetLatestACLPolicy()
	if p.Version != 0 {
		t.Error("invalid ACL must not be saved")
	}
}

func TestACLHistory(t *testing.T) {
	h := newHarness(t)
	if _, err := h.st.SaveACLPolicy(`{"acls":[]}`, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveACLPolicy(validACL, "admin"); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodGet, "/admin/api/acl/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	resp := decode[struct {
		History []aclDTO `json:"history"`
	}](t, rec)
	if len(resp.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(resp.History))
	}
}

func TestACLPreviewChangedNodes(t *testing.T) {
	h := newHarness(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	seedNode(t, h.st, "n2", "100.64.0.3/32")
	// 当前策略为空（无 peer）；候选策略 "*->*" 让两节点互见。
	req := httptest.NewRequest(http.MethodPost, "/admin/api/acl/preview", bytes.NewBufferString(validACL))
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec2 := httptest.NewRecorder()
	h.srv.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec2.Code, rec2.Body.String())
	}
	resp := decode[struct {
		Changed []string `json:"changed_nodes"`
	}](t, rec2)
	sort.Strings(resp.Changed)
	if len(resp.Changed) != 2 {
		t.Fatalf("changed = %v, want [n1 n2]", resp.Changed)
	}
}

func TestACLPreviewInvalid(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/acl/preview", bytes.NewBufferString(badACL))
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ─── keys ─────────────────────────────────────────────────────────────────────

func TestCreateAndListKeys(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodPost, "/admin/api/keys", createKeyRequest{Reusable: true, Tag: "team", TTLSeconds: 3600})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", rec.Code, rec.Body.String())
	}
	created := decode[keyDTO](t, rec)
	if created.Key == "" || created.Reusable || created.Tag != "team" || created.ExpiresAt == nil {
		t.Fatalf("created key wrong: %+v", created)
	}

	rec = h.do(http.MethodGet, "/admin/api/keys", nil)
	resp := decode[struct {
		Keys []keyDTO `json:"keys"`
	}](t, rec)
	if len(resp.Keys) != 1 {
		t.Fatalf("keys len = %d, want 1", len(resp.Keys))
	}
}

func TestRevokeKey(t *testing.T) {
	h := newHarness(t)
	if err := h.st.CreateEnrollKey(&store.EnrollKey{Key: "k1", Tag: "t"}); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodDelete, "/admin/api/keys/k1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	ek, _ := h.st.GetEnrollKey("k1")
	if !ek.Revoked {
		t.Error("key should be revoked")
	}
}

func TestRevokeKeyNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodDelete, "/admin/api/keys/ghost", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ─── relays ───────────────────────────────────────────────────────────────────

func TestListRelays(t *testing.T) {
	h := newHarness(t)
	if err := h.st.UpsertRelayInfo(&store.RelayInfo{NodeID: "r1", TunnelEndpoint: "r1:443", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetRelayLinks("r1", []string{"r2"}); err != nil {
		t.Fatal(err)
	}
	h.online.online["r1"] = true

	rec := h.do(http.MethodGet, "/admin/api/relays", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	resp := decode[struct {
		Relays []relayDTO `json:"relays"`
	}](t, rec)
	if len(resp.Relays) != 1 {
		t.Fatalf("relays len = %d, want 1", len(resp.Relays))
	}
	r := resp.Relays[0]
	if r.NodeID != "r1" || !r.Online || r.TunnelEndpoint != "r1:443" {
		t.Errorf("relay fields wrong: %+v", r)
	}
	if len(r.Neighbors) != 1 || r.Neighbors[0] != "r2" {
		t.Errorf("neighbors = %v, want [r2]", r.Neighbors)
	}
}

func TestSetTopology(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodPut, "/admin/api/relays/topology", setTopologyRequest{RelayID: "r1", Neighbors: []string{"r2", "r3"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	links, _ := h.st.ListRelayLinks()
	if len(links) != 2 {
		t.Fatalf("links = %d, want 2", len(links))
	}
	// 副作用：r1 + 邻居被通知。
	notified := h.notify.allNotified()
	want := []string{"r1", "r2", "r3"}
	if len(notified) != 3 {
		t.Fatalf("notified = %v, want %v", notified, want)
	}
}

func TestSetTopologyMissingRelayID(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodPut, "/admin/api/relays/topology", setTopologyRequest{Neighbors: []string{"r2"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ─── certs / ca ───────────────────────────────────────────────────────────────

func TestListCerts(t *testing.T) {
	h := newHarness(t)
	if err := h.st.RecordCert(&store.Cert{Serial: "1", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordCert(&store.Cert{Serial: "2", NodeID: "n2", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodGet, "/admin/api/certs", nil)
	resp := decode[struct {
		Certs []certDTO `json:"certs"`
	}](t, rec)
	if len(resp.Certs) != 2 {
		t.Fatalf("certs len = %d, want 2", len(resp.Certs))
	}
}

func TestRevokeCert(t *testing.T) {
	h := newHarness(t)
	if err := h.st.RecordCert(&store.Cert{Serial: "777", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodPost, "/admin/api/certs/777/revoke", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !h.ca.wasRevoked("777") {
		t.Error("cert 777 should be revoked via CA")
	}
}

func TestRevokeCertNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodPost, "/admin/api/certs/nope/revoke", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetCA(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/admin/api/ca", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	resp := decode[caDTO](t, rec)
	if resp.CACertPEM == "" || resp.CAHash != "sha256:deadbeef" {
		t.Errorf("ca dto wrong: %+v", resp)
	}
}

func TestHandleGetCAReturnsCAHashField(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/admin/api/ca", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d, body=%s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if _, ok := m["server_fingerprint"]; ok {
		t.Fatalf("响应不应再含 server_fingerprint 字段")
	}
	hash, ok := m["ca_hash"].(string)
	if !ok || !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("ca_hash 字段缺失或格式错误: %v", m["ca_hash"])
	}
}
