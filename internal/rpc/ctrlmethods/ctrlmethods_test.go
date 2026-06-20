package ctrlmethods

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/rpc"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ── mock implementations ────────────────────────────────────────────────────

type mockStore struct {
	nodes      []store.Node
	keys       []store.EnrollKey
	certs      []store.Cert
	policies   []store.ACLPolicy
	relayInfos []store.RelayInfo
	relayLinks []store.RelayLink
	leases     []store.Lease

	// track calls
	deletedNodeID   string
	createdKey      *store.EnrollKey
	revokedKey      string
	savedPolicyDoc  string
	savedPolicyAuth string
	savedPolicy     *store.ACLPolicy
}

func (m *mockStore) ListNodes() ([]store.Node, error) { return m.nodes, nil }
func (m *mockStore) GetNode(id string) (*store.Node, error) {
	for i := range m.nodes {
		if m.nodes[i].ID == id {
			return &m.nodes[i], nil
		}
	}
	return nil, store.ErrNotFound
}
func (m *mockStore) DeleteNode(id string) error { m.deletedNodeID = id; return nil }
func (m *mockStore) GetLeasesByNode(_ string) ([]store.Lease, error) {
	return m.leases, nil
}
func (m *mockStore) ListEnrollKeys() ([]store.EnrollKey, error) { return m.keys, nil }
func (m *mockStore) CreateEnrollKey(ek *store.EnrollKey) error {
	m.createdKey = ek
	return nil
}
func (m *mockStore) RevokeEnrollKey(key string) error { m.revokedKey = key; return nil }
func (m *mockStore) ListCerts() ([]store.Cert, error) { return m.certs, nil }
func (m *mockStore) GetLatestACLPolicy() (*store.ACLPolicy, error) {
	if len(m.policies) == 0 {
		return &store.ACLPolicy{}, nil
	}
	return &m.policies[len(m.policies)-1], nil
}
func (m *mockStore) ListACLPolicies() ([]store.ACLPolicy, error) { return m.policies, nil }
func (m *mockStore) SaveACLPolicy(doc, author string) (*store.ACLPolicy, error) {
	m.savedPolicyDoc = doc
	m.savedPolicyAuth = author
	p := &store.ACLPolicy{Version: uint(len(m.policies) + 1), Document: doc, Author: author}
	m.savedPolicy = p
	return p, nil
}
func (m *mockStore) ListRelayInfo() ([]store.RelayInfo, error)  { return m.relayInfos, nil }
func (m *mockStore) ListRelayLinks() ([]store.RelayLink, error) { return m.relayLinks, nil }

type mockCA struct {
	pem         []byte
	fingerprint string
	revokedSer  string
}

func (m *mockCA) CACertPEM() ([]byte, error)       { return m.pem, nil }
func (m *mockCA) CAPublicKeyHash() (string, error) { return m.fingerprint, nil }
func (m *mockCA) Revoke(serial string) error       { m.revokedSer = serial; return nil }

type mockOnline struct {
	onlineIDs map[string]bool
}

func (m *mockOnline) IsOnline(nodeID string) bool { return m.onlineIDs[nodeID] }

type mockNotify struct {
	called  bool
	nodeIDs []string
}

func (m *mockNotify) RecomputeAndNotify(nodeIDs ...string) {
	m.called = true
	m.nodeIDs = nodeIDs
}

type mockTopo struct {
	assignments map[string]*genv1.TopologyAssignment
	status      TopoStatus
}

func (m *mockTopo) AssignmentForNode(nodeID string) (*genv1.TopologyAssignment, bool) {
	a, ok := m.assignments[nodeID]
	return a, ok
}
func (m *mockTopo) Status() TopoStatus { return m.status }

type mockIngress struct {
	sets map[string]*genv1.IngressSet
}

func (m *mockIngress) GetIngressSet(nodeID string) (*genv1.IngressSet, bool) {
	s, ok := m.sets[nodeID]
	return s, ok
}
func (m *mockIngress) AllIngressSets() []*genv1.IngressSet {
	out := make([]*genv1.IngressSet, 0, len(m.sets))
	for _, s := range m.sets {
		out = append(out, s)
	}
	return out
}

// ── helper ──────────────────────────────────────────────────────────────────

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// buildTestDeps creates a standard test Deps with the given mocks.
func buildTestDeps(ms *mockStore, ca *mockCA, on *mockOnline, notify *mockNotify, topo *mockTopo, ingress *mockIngress) Deps {
	return Deps{
		Store:     ms,
		CA:        ca,
		Online:    on,
		Notify:    notify,
		Topo:      topo,
		Ingress:   ingress,
		StartTime: time.Now().Add(-10 * time.Second),
		Version:   "test-0.1.0",
	}
}

// ── system tests ────────────────────────────────────────────────────────────

func TestSystemStatus(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{
			{ID: "n1"}, {ID: "n2"}, {ID: "n3"}, {ID: "n4"}, {ID: "n5"},
		},
	}
	on := &mockOnline{onlineIDs: map[string]bool{"n1": true, "n3": true, "n5": true}}
	topo := &mockTopo{status: TopoStatus{Version: 42}}
	deps := buildTestDeps(ms, nil, on, nil, topo, nil)

	h := handleSystemStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got systemStatusResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.NodeCount != 5 {
		t.Errorf("node_count = %d, want 5", got.NodeCount)
	}
	if got.OnlineCount != 3 {
		t.Errorf("online_count = %d, want 3", got.OnlineCount)
	}
	if got.Version != "test-0.1.0" {
		t.Errorf("version = %q, want %q", got.Version, "test-0.1.0")
	}
	if got.TopoVersion != 42 {
		t.Errorf("topo_version = %d, want 42", got.TopoVersion)
	}
	if got.UptimeSeconds < 9 {
		t.Errorf("uptime_seconds = %f, want >= 9", got.UptimeSeconds)
	}
}

// ── config.status tests ────────────────────────────────────────────────────

func TestConfigStatus_NilConfig(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	// Config 默认 nil
	h := handleConfigStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "unavailable" {
		t.Errorf("status = %q, want %q", got["status"], "unavailable")
	}
}

func TestConfigStatus_WithConfig(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	deps.Config = &ConfigSummary{
		DBDSN:      "sqlite:///data/ctrl.db",
		ListenAddr: ":9090",
		AdminAddr:  ":8081",
		VirtualCIDR:    "100.64.0.0/10",
		TLSMode:        "mtls",
		CASubject:      "CN=CoreLink CA",
		CAHash:         "sha256:abc",
	}
	h := handleConfigStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got ConfigSummary
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DBDSN != "sqlite:///data/ctrl.db" {
		t.Errorf("db_dsn = %q", got.DBDSN)
	}
	if got.VirtualCIDR != "100.64.0.0/10" {
		t.Errorf("virtual_cidr = %q", got.VirtualCIDR)
	}
	if got.TLSMode != "mtls" {
		t.Errorf("tls_mode = %q", got.TLSMode)
	}
}

// ── system.logs tests ──────────────────────────────────────────────────────

func TestSystemLogs_NilLogBuffer(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	// LogBuffer 默认 nil
	h := handleSystemLogs(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("空 LogBuffer 应返回空数组，got %d entries", len(got.Entries))
	}
}

func TestSystemLogs_WithLogBuffer(t *testing.T) {
	buf := rpc.NewLogBuffer(10)
	buf.Add(rpc.LogEntry{Level: "INFO", Message: "log1"})
	buf.Add(rpc.LogEntry{Level: "WARN", Message: "log2"})
	buf.Add(rpc.LogEntry{Level: "ERROR", Message: "log3"})

	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("len = %d, want 3", len(got.Entries))
	}
	if got.Entries[0].Message != "log1" || got.Entries[2].Message != "log3" {
		t.Errorf("entries = %+v", got.Entries)
	}
}

func TestSystemLogs_WithCountParam(t *testing.T) {
	buf := rpc.NewLogBuffer(10)
	for i := range 5 {
		buf.Add(rpc.LogEntry{Level: "INFO", Message: fmt.Sprintf("msg%d", i)})
	}

	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	deps.LogBuffer = buf

	h := handleSystemLogs(deps)
	result, err := h(mustMarshal(t, logsParams{Count: 2}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got logsResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("len = %d, want 2", len(got.Entries))
	}
	// 最近 2 条是 msg3, msg4
	if got.Entries[0].Message != "msg3" || got.Entries[1].Message != "msg4" {
		t.Errorf("entries = %+v", got.Entries)
	}
}

func TestSystemStatus_CertAndKeyCount(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{{ID: "n1"}},
		certs: []store.Cert{{Serial: "1"}, {Serial: "2"}},
		keys: []store.EnrollKey{
			{Key: "k1", Revoked: false, Consumed: false}, // 有效
			{Key: "k2", Revoked: true, Consumed: false},  // 已吊销，不计
			{Key: "k3", Revoked: false, Consumed: true},  // 已消费，不计
			{Key: "k4", Revoked: false, Consumed: false}, // 有效
		},
	}
	deps := buildTestDeps(ms, nil, &mockOnline{onlineIDs: map[string]bool{}}, nil, &mockTopo{}, nil)

	h := handleSystemStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got systemStatusResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CertCount != 2 {
		t.Errorf("cert_count = %d, want 2", got.CertCount)
	}
	if got.KeyCount != 2 {
		t.Errorf("key_count = %d（只计有效 key），want 2", got.KeyCount)
	}
}

func TestSystemStatus_NodesEntries(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{
			{ID: "n1", Hostname: "h1", VirtualIP: "100.64.0.1", Role: "node"},
			{ID: "n2", Hostname: "h2", VirtualIP: "100.64.0.2", Role: "node"},
		},
	}
	on := &mockOnline{onlineIDs: map[string]bool{"n1": true}}
	deps := buildTestDeps(ms, nil, on, nil, &mockTopo{}, nil)

	h := handleSystemStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got systemStatusResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("nodes len = %d, want 2", len(got.Nodes))
	}
	if got.Nodes[0].ID != "n1" || !got.Nodes[0].Online {
		t.Errorf("nodes[0] = %+v", got.Nodes[0])
	}
	if got.Nodes[1].ID != "n2" || got.Nodes[1].Online {
		t.Errorf("nodes[1] = %+v", got.Nodes[1])
	}
}

// ── nodes tests ─────────────────────────────────────────────────────────────

func TestNodesList(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{
			{ID: "n1", Hostname: "host1", VirtualIP: "100.64.0.1", Role: "node", Generation: 1},
			{ID: "n2", Hostname: "host2", VirtualIP: "100.64.0.2", Role: "node", Generation: 3},
		},
	}
	on := &mockOnline{onlineIDs: map[string]bool{"n1": true}}
	deps := buildTestDeps(ms, nil, on, nil, nil, nil)

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
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "n1" || got[0].Hostname != "host1" || !got[0].Online {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].ID != "n2" || got[1].Role != "node" || got[1].Online {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestNodesGet(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{
			{ID: "n1", Hostname: "host1", VirtualIP: "100.64.0.1", Role: "node", Generation: 5},
		},
	}
	on := &mockOnline{onlineIDs: map[string]bool{"n1": true}}
	ing := &mockIngress{
		sets: map[string]*genv1.IngressSet{
			"n1": {
				NodeId: "n1",
				Ingresses: []*genv1.Ingress{
					{Id: "ing1", Host: "1.2.3.4", Port: 443, Source: genv1.IngressSource_INGRESS_SOURCE_STUN, Confidence: 85, NatType: genv1.NatType_NAT_TYPE_FULL_CONE},
				},
			},
		},
	}
	deps := buildTestDeps(ms, nil, on, nil, nil, ing)

	h := handleNodesGet(deps)
	result, err := h(mustMarshal(t, nodeIDParams{ID: "n1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got nodeDetailDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "n1" || !got.Online || got.Generation != 5 {
		t.Errorf("node fields: %+v", got)
	}
	if len(got.Ingresses) != 1 {
		t.Fatalf("ingresses len = %d, want 1", len(got.Ingresses))
	}
	if got.Ingresses[0].Host != "1.2.3.4" || got.Ingresses[0].Port != 443 {
		t.Errorf("ingress = %+v", got.Ingresses[0])
	}
}

func TestNodesDelete(t *testing.T) {
	ms := &mockStore{}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)

	h := handleNodesDelete(deps)
	_, err := h(mustMarshal(t, nodeIDParams{ID: "node-x"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.deletedNodeID != "node-x" {
		t.Errorf("deletedNodeID = %q, want %q", ms.deletedNodeID, "node-x")
	}
}

func TestNodesIngresses(t *testing.T) {
	ing := &mockIngress{
		sets: map[string]*genv1.IngressSet{
			"n1": {
				NodeId: "n1",
				Ingresses: []*genv1.Ingress{
					{Id: "i1", Host: "1.1.1.1", Port: 443, Source: genv1.IngressSource_INGRESS_SOURCE_STUN, NatType: genv1.NatType_NAT_TYPE_FULL_CONE, Confidence: 90},
					{Id: "i2", Host: "2.2.2.2", Port: 8443, Source: genv1.IngressSource_INGRESS_SOURCE_UPNP, NatType: genv1.NatType_NAT_TYPE_OPEN, Confidence: 100},
				},
			},
		},
	}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, ing)

	h := handleNodesIngresses(deps)
	result, err := h(mustMarshal(t, nodeIDParams{ID: "n1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []ingressDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Host != "1.1.1.1" || got[0].Port != 443 || got[0].Confidence != 90 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Host != "2.2.2.2" || got[1].Port != 8443 {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestNodesIngresses_NoIngress(t *testing.T) {
	// Ingress 接口为 nil 时应返回空数组（直接构造 Deps 确保接口值为 nil）
	deps := Deps{
		Store:     &mockStore{},
		StartTime: time.Now(),
	}
	h := handleNodesIngresses(deps)
	result, err := h(mustMarshal(t, nodeIDParams{ID: "n1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []ingressDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("无 ingress 应返回空数组，got %d", len(got))
	}
}

func TestNodesIngresses_MissingID(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleNodesIngresses(deps)
	_, err := h(mustMarshal(t, nodeIDParams{ID: ""}))
	if err == nil {
		t.Fatal("空 id 应返回错误")
	}
}

func TestNodesGet_NotFound(t *testing.T) {
	ms := &mockStore{nodes: []store.Node{}}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleNodesGet(deps)
	_, err := h(mustMarshal(t, nodeIDParams{ID: "nonexistent"}))
	if err == nil {
		t.Fatal("不存在的节点应返回错误")
	}
}

func TestNodesGet_MissingID(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleNodesGet(deps)
	_, err := h(mustMarshal(t, nodeIDParams{ID: ""}))
	if err == nil {
		t.Fatal("空 id 应返回错误")
	}
}

func TestNodesDelete_MissingID(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleNodesDelete(deps)
	_, err := h(mustMarshal(t, nodeIDParams{ID: ""}))
	if err == nil {
		t.Fatal("空 id 应返回错误")
	}
}

// ── keys tests ──────────────────────────────────────────────────────────────

func TestKeysList(t *testing.T) {
	now := time.Now()
	ms := &mockStore{
		keys: []store.EnrollKey{
			{Key: "key1", Reusable: true, Tag: "dev", CreatedAt: now},
			{Key: "key2", Reusable: false, Tag: "prod", Revoked: true, CreatedAt: now},
		},
	}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)

	h := handleKeysList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got []keyDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Key != "key1" || !got[0].Reusable {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Key != "key2" || !got[1].Revoked {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestKeysCreate(t *testing.T) {
	ms := &mockStore{}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)

	h := handleKeysCreate(deps)
	result, err := h(mustMarshal(t, createKeyParams{Reusable: true, Tag: "test", TTLSeconds: 3600}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got keyDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Key == "" {
		t.Error("key should not be empty")
	}
	if len(got.Key) != 64 { // 32 bytes = 64 hex chars
		t.Errorf("key length = %d, want 64", len(got.Key))
	}
	if !got.Reusable {
		t.Error("reusable should be true")
	}
	if got.Tag != "test" {
		t.Errorf("tag = %q, want %q", got.Tag, "test")
	}
	if got.ExpiresAt == nil {
		t.Error("expires_at should be set with TTL > 0")
	}
	if ms.createdKey == nil {
		t.Fatal("store.CreateEnrollKey not called")
	}
}

func TestKeysRevoke(t *testing.T) {
	ms := &mockStore{}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)

	h := handleKeysRevoke(deps)
	_, err := h(mustMarshal(t, revokeKeyParams{Key: "key-abc"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.revokedKey != "key-abc" {
		t.Errorf("revokedKey = %q, want %q", ms.revokedKey, "key-abc")
	}
}

func TestKeysCreate_NoTTL(t *testing.T) {
	ms := &mockStore{}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleKeysCreate(deps)
	result, err := h(mustMarshal(t, createKeyParams{Reusable: false, Tag: "one-shot"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got keyDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("TTL=0 时 expires_at 应为 nil，got %v", got.ExpiresAt)
	}
	if got.Reusable {
		t.Error("reusable 应为 false")
	}
	if got.Tag != "one-shot" {
		t.Errorf("tag = %q, want %q", got.Tag, "one-shot")
	}
}

func TestKeysCreate_NilParams(t *testing.T) {
	ms := &mockStore{}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleKeysCreate(deps)
	// nil params 应使用默认值
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got keyDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Key == "" || len(got.Key) != 64 {
		t.Errorf("key 不合法: %q", got.Key)
	}
}

func TestKeysRevoke_MissingKey(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleKeysRevoke(deps)
	_, err := h(mustMarshal(t, revokeKeyParams{Key: ""}))
	if err == nil {
		t.Fatal("空 key 应返回错误")
	}
}

// ── certs tests ─────────────────────────────────────────────────────────────

func TestCertsList(t *testing.T) {
	now := time.Now()
	ms := &mockStore{
		certs: []store.Cert{
			{Serial: "100", NodeID: "n1", NotAfter: now.Add(24 * time.Hour)},
			{Serial: "101", NodeID: "n2", NotAfter: now.Add(48 * time.Hour), Revoked: true, RevokedAt: &now},
		},
	}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)

	h := handleCertsList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got []certDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Serial != "100" || got[0].NodeID != "n1" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if !got[1].Revoked || got[1].RevokedAt == nil {
		t.Errorf("got[1] should be revoked: %+v", got[1])
	}
}

func TestCAInfo(t *testing.T) {
	ca := &mockCA{
		pem:         []byte("-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n"),
		fingerprint: "sha256:abcdef1234567890",
	}
	deps := buildTestDeps(&mockStore{}, ca, nil, nil, nil, nil)

	h := handleCAInfo(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got caInfoResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CACertPEM == "" {
		t.Error("ca_cert_pem should not be empty")
	}
	if got.CAHash != "sha256:abcdef1234567890" {
		t.Errorf("ca_hash = %q", got.CAHash)
	}
}

func TestHandleCAInfoReturnsCAHash(t *testing.T) {
	ca := &mockCA{pem: []byte("PEM"), fingerprint: "sha256:deadbeef"}
	deps := buildTestDeps(&mockStore{}, ca, nil, nil, nil, nil)
	h := handleCAInfo(deps)
	res, err := h(nil)
	if err != nil {
		t.Fatalf("handleCAInfo: %v", err)
	}
	b, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["server_fingerprint"]; ok {
		t.Fatalf("不应含 server_fingerprint")
	}
	if m["ca_hash"] != "sha256:deadbeef" {
		t.Fatalf("ca_hash=%v", m["ca_hash"])
	}
}

// ── acl tests ───────────────────────────────────────────────────────────────

func TestACLGet(t *testing.T) {
	ms := &mockStore{
		policies: []store.ACLPolicy{
			{Version: 1, Document: `{"acl":"v1"}`, Author: "admin"},
			{Version: 2, Document: `{"acl":"v2"}`, Author: "admin2"},
		},
	}
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
	if got.Version != 2 {
		t.Errorf("version = %d, want 2", got.Version)
	}
	if got.Document != `{"acl":"v2"}` {
		t.Errorf("document = %q", got.Document)
	}
	if got.Author != "admin2" {
		t.Errorf("author = %q", got.Author)
	}
}

func TestACLSet(t *testing.T) {
	ms := &mockStore{}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)

	h := handleACLSet(deps)
	result, err := h(mustMarshal(t, aclSetParams{Document: `{"acl":"new"}`, Author: "tester"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got aclDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
	if ms.savedPolicyDoc != `{"acl":"new"}` {
		t.Errorf("saved doc = %q", ms.savedPolicyDoc)
	}
	if ms.savedPolicyAuth != "tester" {
		t.Errorf("saved author = %q", ms.savedPolicyAuth)
	}
}

func TestACLHistory(t *testing.T) {
	now := time.Now()
	ms := &mockStore{
		policies: []store.ACLPolicy{
			{Version: 1, Document: `{"v":1}`, Author: "a1", CreatedAt: now.Add(-time.Hour)},
			{Version: 2, Document: `{"v":2}`, Author: "a2", CreatedAt: now},
		},
	}
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
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Version != 1 || got[1].Version != 2 {
		t.Errorf("versions: %d, %d", got[0].Version, got[1].Version)
	}
}

func TestACLSet_EmptyDocument(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, nil, nil)
	h := handleACLSet(deps)
	_, err := h(mustMarshal(t, aclSetParams{Document: ""}))
	if err == nil {
		t.Fatal("空 document 应返回错误")
	}
}

func TestACLSet_DefaultAuthor(t *testing.T) {
	ms := &mockStore{}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleACLSet(deps)
	_, err := h(mustMarshal(t, aclSetParams{Document: `{"ok":true}`, Author: ""}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms.savedPolicyAuth != "rpc" {
		t.Errorf("author 为空时应默认 %q，got %q", "rpc", ms.savedPolicyAuth)
	}
}

// ── topo tests ──────────────────────────────────────────────────────────────

func TestTopoStatus(t *testing.T) {
	now := time.Now()
	topo := &mockTopo{
		status: TopoStatus{
			Version:       7,
			TransitCount:  3,
			LeafCount:     12,
			LastRecompute: now,
		},
	}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, topo, nil)

	h := handleTopoStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got topoStatusResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 7 {
		t.Errorf("version = %d, want 7", got.Version)
	}
	if got.TransitCount != 3 {
		t.Errorf("transit_count = %d, want 3", got.TransitCount)
	}
	if got.LeafCount != 12 {
		t.Errorf("leaf_count = %d, want 12", got.LeafCount)
	}
}

func TestTopoTraceroute(t *testing.T) {
	topo := &mockTopo{
		assignments: map[string]*genv1.TopologyAssignment{
			"src-node": {
				Version: 1,
				BaselineRoutes: []*genv1.Route2{
					{
						DstNode: "dst-node",
						Hops: []*genv1.Hop{
							{NodeId: "relay-a", IngressId: "ing-r1"},
							{NodeId: "dst-node", IngressId: "ing-d1"},
						},
					},
					{
						DstNode: "dst-node",
						Hops: []*genv1.Hop{
							{NodeId: "relay-b", IngressId: "ing-r2"},
							{NodeId: "relay-c", IngressId: "ing-r3"},
							{NodeId: "dst-node", IngressId: "ing-d1"},
						},
					},
					{
						DstNode: "other-node",
						Hops: []*genv1.Hop{
							{NodeId: "other-node", IngressId: "ing-o1"},
						},
					},
				},
			},
		},
	}
	ing := &mockIngress{
		sets: map[string]*genv1.IngressSet{
			"relay-a": {NodeId: "relay-a", Ingresses: []*genv1.Ingress{
				{Id: "ing-r1", Host: "10.0.0.1", Port: 443},
			}},
			"relay-b": {NodeId: "relay-b", Ingresses: []*genv1.Ingress{
				{Id: "ing-r2", Host: "10.0.0.2", Port: 8443},
			}},
			"relay-c": {NodeId: "relay-c", Ingresses: []*genv1.Ingress{
				{Id: "ing-r3", Host: "10.0.0.3", Port: 443},
			}},
			"dst-node": {NodeId: "dst-node", Ingresses: []*genv1.Ingress{
				{Id: "ing-d1", Host: "10.0.0.10", Port: 443},
			}},
		},
	}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, topo, ing)

	h := handleTopoTraceroute(deps)
	result, err := h(mustMarshal(t, tracerouteParams{Src: "src-node", Dst: "dst-node"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, _ := json.Marshal(result)
	var got tracerouteResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Paths) != 2 {
		t.Fatalf("paths len = %d, want 2", len(got.Paths))
	}

	// First path: relay-a -> dst-node (2 hops, active)
	p0 := got.Paths[0]
	if p0.TotalHops != 2 {
		t.Errorf("path[0] total_hops = %d, want 2", p0.TotalHops)
	}
	if !p0.Active {
		t.Error("path[0] should be active")
	}
	if p0.Hops[0].NodeID != "relay-a" || p0.Hops[0].Host != "10.0.0.1" || p0.Hops[0].Port != 443 {
		t.Errorf("path[0].hops[0] = %+v", p0.Hops[0])
	}
	if p0.Hops[1].NodeID != "dst-node" || p0.Hops[1].Host != "10.0.0.10" {
		t.Errorf("path[0].hops[1] = %+v", p0.Hops[1])
	}

	// Second path: relay-b -> relay-c -> dst-node (3 hops, not active)
	p1 := got.Paths[1]
	if p1.TotalHops != 3 {
		t.Errorf("path[1] total_hops = %d, want 3", p1.TotalHops)
	}
	if p1.Active {
		t.Error("path[1] should not be active")
	}
	if p1.Hops[0].NodeID != "relay-b" || p1.Hops[0].Host != "10.0.0.2" {
		t.Errorf("path[1].hops[0] = %+v", p1.Hops[0])
	}
}

func TestTopoGraph(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{
			{ID: "n1", Hostname: "host1", VirtualIP: "100.64.0.1", Role: "node"},
			{ID: "r1", Hostname: "relay1", VirtualIP: "100.64.0.10", Role: "node"},
			{ID: "n2", Hostname: "host2", VirtualIP: "100.64.0.2", Role: "node"},
		},
		relayLinks: []store.RelayLink{
			{RelayID: "r1", NeighborID: "n1"},
			{RelayID: "r1", NeighborID: "n2"},
		},
	}
	on := &mockOnline{onlineIDs: map[string]bool{"n1": true, "r1": true}}
	deps := buildTestDeps(ms, nil, on, nil, nil, nil)

	h := handleTopoGraph(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got topoGraphResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Nodes) != 3 {
		t.Fatalf("nodes len = %d, want 3", len(got.Nodes))
	}
	// 验证在线状态
	nodeMap := make(map[string]topoGraphNode)
	for _, n := range got.Nodes {
		nodeMap[n.ID] = n
	}
	if !nodeMap["n1"].Online {
		t.Error("n1 应在线")
	}
	if !nodeMap["r1"].Online {
		t.Error("r1 应在线")
	}
	if nodeMap["n2"].Online {
		t.Error("n2 应离线")
	}

	if len(got.Edges) != 2 {
		t.Fatalf("edges len = %d, want 2", len(got.Edges))
	}
	if got.Edges[0].From != "r1" || got.Edges[0].To != "n1" {
		t.Errorf("edge[0] = %+v", got.Edges[0])
	}
}

func TestTopoGraph_NoLinks(t *testing.T) {
	ms := &mockStore{
		nodes: []store.Node{{ID: "n1", Hostname: "h1"}},
	}
	deps := buildTestDeps(ms, nil, &mockOnline{onlineIDs: map[string]bool{}}, nil, nil, nil)
	h := handleTopoGraph(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got topoGraphResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Nodes) != 1 {
		t.Errorf("nodes len = %d, want 1", len(got.Nodes))
	}
	if len(got.Edges) != 0 {
		t.Errorf("无链路时 edges 应为空数组，got %d", len(got.Edges))
	}
}

func TestTopoStatus_NilTopo(t *testing.T) {
	// 直接构造 Deps 确保 Topo 接口值为 nil（避免 nil *mockTopo 包装成非 nil 接口）
	deps := Deps{Store: &mockStore{}, StartTime: time.Now()}
	h := handleTopoStatus(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got topoStatusResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 0 {
		t.Errorf("nil topo version 应为 0，got %d", got.Version)
	}
}

func TestTopoTraceroute_MissingParams(t *testing.T) {
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, &mockTopo{}, nil)
	h := handleTopoTraceroute(deps)
	_, err := h(mustMarshal(t, tracerouteParams{Src: "s1", Dst: ""}))
	if err == nil {
		t.Fatal("缺少 dst 应返回错误")
	}
	_, err = h(mustMarshal(t, tracerouteParams{Src: "", Dst: "d1"}))
	if err == nil {
		t.Fatal("缺少 src 应返回错误")
	}
}

func TestTopoTraceroute_NoAssignment(t *testing.T) {
	topo := &mockTopo{assignments: map[string]*genv1.TopologyAssignment{}}
	deps := buildTestDeps(&mockStore{}, nil, nil, nil, topo, nil)
	h := handleTopoTraceroute(deps)
	result, err := h(mustMarshal(t, tracerouteParams{Src: "unknown", Dst: "d1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got tracerouteResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Paths) != 0 {
		t.Errorf("无 assignment 时 paths 应为空，got %d", len(got.Paths))
	}
}

func TestTopoTraceroute_NilTopo(t *testing.T) {
	deps := Deps{Store: &mockStore{}, StartTime: time.Now()}
	h := handleTopoTraceroute(deps)
	result, err := h(mustMarshal(t, tracerouteParams{Src: "s1", Dst: "d1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got tracerouteResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Paths) != 0 {
		t.Errorf("nil topo 时 paths 应为空，got %d", len(got.Paths))
	}
}

// ── RegisterAll integration test ────────────────────────────────────────────

func TestRegisterAll(t *testing.T) {
	srv := rpc.NewServer()
	deps := buildTestDeps(&mockStore{}, &mockCA{pem: []byte("pem"), fingerprint: "fp"}, &mockOnline{onlineIDs: map[string]bool{}}, nil, &mockTopo{}, &mockIngress{sets: map[string]*genv1.IngressSet{}})

	// Should not panic.
	RegisterAll(srv, deps)
}
