package configsvc

import (
	"sort"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// integrationFixture 集成测试通用 fixture。
type integrationFixture struct {
	st *store.Store
	h  *ConfigHTTP
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(&store.Node{ID: "node-a", Role: "node", WGPubKey: "pk-a", VirtualIP: "100.64.0.1/32", User: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(&store.Node{ID: "node-b", Role: "node", WGPubKey: "pk-b", VirtualIP: "100.64.0.2/32", User: "admin"}); err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(st)
	h := NewConfigHTTP(adapter, CRLProviderFunc(func(_ time.Duration) ([]byte, error) { return nil, nil }), nil)
	return &integrationFixture{st: st, h: h}
}

// ─── (a) TestIntegration_NodeAlias_Internal ─────────────────────────────────

func TestIntegration_NodeAlias_Internal(t *testing.T) {
	f := newIntegrationFixture(t)
	if err := f.st.CreateNodeAlias(&store.NodeAlias{
		NodeID: "node-a", Name: "db", FQDN: "db.corelink.internal",
		Kind: "internal", TargetVIP: "100.64.0.1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertDNSSettings(&store.DNSSettings{
		ID: 1, Enabled: true, ZonesJSON: `["corelink.internal"]`,
		UpstreamsJSON: `["8.8.8.8"]`, InterceptMode: "local",
		ListenAddr: "127.0.0.1", ListenPort: 5353,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetDns() == nil || !cfg.GetDns().GetEnabled() {
		t.Fatal("DNS 应启用")
	}
	found := false
	for _, r := range cfg.GetDns().GetRecords() {
		if r.GetFqdn() == "db.corelink.internal" && r.GetTargetIp() == "100.64.0.1" && r.GetRecordType() == "A" {
			found = true
		}
	}
	if !found {
		t.Fatal("应包含 internal alias → A record")
	}
}

// ─── (b) TestIntegration_NodeAlias_External ─────────────────────────────────

func TestIntegration_NodeAlias_External(t *testing.T) {
	f := newIntegrationFixture(t)
	if err := f.st.CreateNodeAlias(&store.NodeAlias{
		NodeID: "node-b", Name: "web", FQDN: "web.example.com",
		Kind: "external", TargetVIP: "100.64.0.2", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertDNSSettings(&store.DNSSettings{
		ID: 1, Enabled: true, ZonesJSON: `["corelink.internal"]`,
		UpstreamsJSON: `["8.8.8.8"]`, InterceptMode: "local",
		ListenAddr: "127.0.0.1", ListenPort: 5353,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := f.h.buildNodeConfig("node-a")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range cfg.GetDns().GetRecords() {
		if r.GetFqdn() == "web.example.com" && r.GetTargetIp() == "100.64.0.2" {
			found = true
		}
	}
	if !found {
		t.Fatal("应包含 external alias → DNS record")
	}
}

// ─── (c) TestIntegration_DirectRoute ────────────────────────────────────────

func TestIntegration_DirectRoute(t *testing.T) {
	f := newIntegrationFixture(t)
	if err := f.st.CreatePublishedRoute(&store.PublishedRoute{
		NodeID: "node-a", Kind: "direct", RouteCIDR: "10.0.0.0/16",
		Priority: 100, SNAT: true, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// node-b 应在 peer AllowedIPs 中看到 direct route
	cfgB, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range cfgB.GetPeers() {
		if peer.GetNodeId() == "node-a" {
			ips := peer.GetAllowedIps()
			sort.Strings(ips)
			hasRoute := false
			for _, ip := range ips {
				if ip == "10.0.0.0/16" {
					hasRoute = true
				}
			}
			if !hasRoute {
				t.Fatalf("node-b peer node-a AllowedIPs 应包含 10.0.0.0/16, got %v", ips)
			}
		}
	}

	// node-a 应有 egress rule
	cfgA, err := f.h.buildNodeConfig("node-a")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range cfgA.GetEgressRules() {
		if r.GetKind() == "direct" && r.GetVipPrefix() == "10.0.0.0/16" && r.GetSnat() {
			found = true
		}
	}
	if !found {
		t.Fatal("node-a 应有 direct egress rule with SNAT")
	}
}

// ─── (d) TestIntegration_StaticMapping ──────────────────────────────────────

func TestIntegration_StaticMapping(t *testing.T) {
	f := newIntegrationFixture(t)
	if err := f.st.CreatePublishedRoute(&store.PublishedRoute{
		NodeID: "node-a", Kind: "static_mapping",
		VIPCIDR: "100.64.2.0/24", TargetCIDR: "10.0.2.0/24",
		Priority: 100, SNAT: true, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// node-b 的 AllowedIPs 应包含 VIP CIDR
	cfgB, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range cfgB.GetPeers() {
		if peer.GetNodeId() == "node-a" {
			hasVIP := false
			for _, ip := range peer.GetAllowedIps() {
				if ip == "100.64.2.0/24" {
					hasVIP = true
				}
			}
			if !hasVIP {
				t.Fatalf("AllowedIPs 应包含 VIP CIDR 100.64.2.0/24, got %v", peer.GetAllowedIps())
			}
		}
	}

	// node-a 的 egress rule 应含 DNAT 信息
	cfgA, err := f.h.buildNodeConfig("node-a")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range cfgA.GetEgressRules() {
		if r.GetKind() == "static_mapping" && r.GetVipPrefix() == "100.64.2.0/24" && r.GetTargetPrefix() == "10.0.2.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatal("node-a 应有 static_mapping egress rule (VIP→Target)")
	}
}

// ─── (e) TestIntegration_DiscoveredMapping ──────────────────────────────────

func TestIntegration_DiscoveredMapping(t *testing.T) {
	f := newIntegrationFixture(t)
	discRoute := &store.PublishedRoute{
		NodeID: "node-a", Kind: "discovered_mapping",
		VIPCIDR: "100.64.0.0/16", TargetCIDR: "10.1.0.0/16",
		Priority: 100, SNAT: true, Enabled: true,
	}
	if err := f.st.CreatePublishedRoute(discRoute); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.st.UpsertDiscoveredMapping(&store.DiscoveredMapping{
		RouteID: discRoute.ID, NodeID: "node-a",
		TargetIP: "10.1.0.8", VIPIP: "100.64.0.8",
		Priority: 100, ObservedAt: now, StaleAfter: 5 * time.Minute, Winner: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertDiscoveredMapping(&store.DiscoveredMapping{
		RouteID: discRoute.ID, NodeID: "node-a",
		TargetIP: "10.1.0.9", VIPIP: "100.64.0.9",
		Priority: 100, ObservedAt: now, StaleAfter: 5 * time.Minute, Winner: false,
	}); err != nil {
		t.Fatal(err)
	}

	cfgB, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}

	// 应只看到 winner /32
	foundWinner, foundNonWinner := false, false
	for _, pp := range cfgB.GetPublishedPrefixes() {
		if pp.GetPrefix() == "100.64.0.8/32" {
			foundWinner = true
		}
		if pp.GetPrefix() == "100.64.0.9/32" {
			foundNonWinner = true
		}
		if pp.GetPrefix() == "100.64.0.0/16" {
			t.Fatal("不应发布整个 VIP 池 100.64.0.0/16")
		}
	}
	if !foundWinner {
		t.Fatal("应发布 winner /32: 100.64.0.8/32")
	}
	if foundNonWinner {
		t.Fatal("不应发布 non-winner /32: 100.64.0.9/32")
	}
}

// ─── (f) TestIntegration_DNS_ConfigPropagation ──────────────────────────────

func TestIntegration_DNS_ConfigPropagation(t *testing.T) {
	f := newIntegrationFixture(t)
	if err := f.st.CreateNodeAlias(&store.NodeAlias{
		NodeID: "node-a", Name: "api", FQDN: "api.corelink.internal",
		Kind: "internal", TargetVIP: "100.64.0.1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertDNSSettings(&store.DNSSettings{
		ID: 1, Enabled: true, ZonesJSON: `["corelink.internal"]`,
		UpstreamsJSON: `["1.1.1.1","8.8.8.8"]`, InterceptMode: "lan",
		ListenAddr: "0.0.0.0", ListenPort: 53,
		LANIfacesJSON: `["eth0"]`, LANCIDRsJSON: `["192.168.1.0/24"]`,
	}); err != nil {
		t.Fatal(err)
	}

	// DNS config 应下发给 node-a 和 node-b
	for _, nodeID := range []string{"node-a", "node-b"} {
		cfg, err := f.h.buildNodeConfig(nodeID)
		if err != nil {
			t.Fatalf("buildNodeConfig(%s): %v", nodeID, err)
		}
		dns := cfg.GetDns()
		if dns == nil || !dns.GetEnabled() {
			t.Fatalf("%s 应收到启用的 DNS 配置", nodeID)
		}
		if dns.GetInterceptMode() != "lan" {
			t.Fatalf("%s DNS intercept = %q, want lan", nodeID, dns.GetInterceptMode())
		}
		if len(dns.GetRecords()) != 1 {
			t.Fatalf("%s DNS records = %d, want 1", nodeID, len(dns.GetRecords()))
		}
		if len(dns.GetUpstreams()) != 2 {
			t.Fatalf("%s DNS upstreams = %d, want 2", nodeID, len(dns.GetUpstreams()))
		}
	}
}

// ─── (g) TestIntegration_DisabledRoute_NotVisible ───────────────────────────

func TestIntegration_DisabledRoute_NotVisible(t *testing.T) {
	f := newIntegrationFixture(t)
	r := &store.PublishedRoute{
		NodeID: "node-a", Kind: "direct", RouteCIDR: "10.99.0.0/16",
		Priority: 100, SNAT: true, Enabled: true,
	}
	if err := f.st.CreatePublishedRoute(r); err != nil {
		t.Fatal(err)
	}

	// 先验证启用时可见
	cfgB, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}
	hasBefore := false
	for _, pp := range cfgB.GetPublishedPrefixes() {
		if pp.GetPrefix() == "10.99.0.0/16" {
			hasBefore = true
		}
	}
	if !hasBefore {
		t.Fatal("启用的路由应可见")
	}

	// 禁用后不可见
	if err := f.st.SetPublishedRouteEnabled(r.ID, false); err != nil {
		t.Fatal(err)
	}
	cfgB2, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, pp := range cfgB2.GetPublishedPrefixes() {
		if pp.GetPrefix() == "10.99.0.0/16" {
			t.Fatal("禁用的路由不应出现在 published prefixes 中")
		}
	}
	for _, peer := range cfgB2.GetPeers() {
		if peer.GetNodeId() == "node-a" {
			for _, ip := range peer.GetAllowedIps() {
				if ip == "10.99.0.0/16" {
					t.Fatal("禁用的路由不应出现在 AllowedIPs 中")
				}
			}
		}
	}
}

// ─── (h) TestIntegration_CrossNode_Isolation ────────────────────────────────

func TestIntegration_CrossNode_Isolation(t *testing.T) {
	f := newIntegrationFixture(t)
	// node-a 发布路由
	if err := f.st.CreatePublishedRoute(&store.PublishedRoute{
		NodeID: "node-a", Kind: "static_mapping",
		VIPCIDR: "100.64.3.0/24", TargetCIDR: "10.0.3.0/24",
		Priority: 100, SNAT: true, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	cfgA, err := f.h.buildNodeConfig("node-a")
	if err != nil {
		t.Fatal(err)
	}
	cfgB, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}

	// node-a 应有 egress rules
	if len(cfgA.GetEgressRules()) == 0 {
		t.Fatal("node-a（出口节点）应有 egress rules")
	}

	// node-b 不应有 egress rules
	if len(cfgB.GetEgressRules()) != 0 {
		t.Fatalf("node-b 不应有 egress rules, got %d", len(cfgB.GetEgressRules()))
	}

	// node-b 不应有 discovery configs
	if len(cfgB.GetDiscoveryConfigs()) != 0 {
		t.Fatalf("node-b 不应有 discovery configs, got %d", len(cfgB.GetDiscoveryConfigs()))
	}

	// 但 node-b 应能在 peer AllowedIPs 中看到前缀
	for _, peer := range cfgB.GetPeers() {
		if peer.GetNodeId() == "node-a" {
			hasVIP := false
			for _, ip := range peer.GetAllowedIps() {
				if ip == "100.64.3.0/24" {
					hasVIP = true
				}
			}
			if !hasVIP {
				t.Fatalf("node-b 应能看到 node-a 的 published prefix, got %v", peer.GetAllowedIps())
			}
		}
	}
}

// ─── 保留原有综合测试 ───────────────────────────────────────────────────────

func TestIntegration_SharedVIPPool(t *testing.T) {
	f := newIntegrationFixture(t)

	if err := f.st.CreateNodeAlias(&store.NodeAlias{
		NodeID: "node-a", Name: "db", FQDN: "db.corelink.internal",
		Kind: "internal", TargetVIP: "100.64.0.1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreatePublishedRoute(&store.PublishedRoute{
		NodeID: "node-a", Kind: "static_mapping",
		VIPCIDR: "100.64.2.0/24", TargetCIDR: "10.0.2.0/24",
		Priority: 100, SNAT: true, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	discRoute := &store.PublishedRoute{
		NodeID: "node-a", Kind: "discovered_mapping",
		VIPCIDR: "100.64.0.0/16", TargetCIDR: "10.1.0.0/16",
		Priority: 100, SNAT: true, Enabled: true,
	}
	if err := f.st.CreatePublishedRoute(discRoute); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.st.UpsertDiscoveredMapping(&store.DiscoveredMapping{
		RouteID: discRoute.ID, NodeID: "node-a",
		TargetIP: "10.1.0.8", VIPIP: "100.64.0.8",
		Priority: 100, ObservedAt: now, StaleAfter: 5 * time.Minute, Winner: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertDiscoveredMapping(&store.DiscoveredMapping{
		RouteID: discRoute.ID, NodeID: "node-a",
		TargetIP: "10.1.0.9", VIPIP: "100.64.0.9",
		Priority: 100, ObservedAt: now, StaleAfter: 5 * time.Minute, Winner: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertDNSSettings(&store.DNSSettings{
		ID: 1, Enabled: true, ZonesJSON: `["corelink.internal"]`,
		UpstreamsJSON: `["8.8.8.8"]`, InterceptMode: "local",
		ListenAddr: "127.0.0.1", ListenPort: 5353,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := f.h.buildNodeConfig("node-b")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetDns() == nil || !cfg.GetDns().GetEnabled() {
		t.Fatal("node-b 应收到 DNS 配置")
	}
	if len(cfg.GetDns().GetRecords()) != 1 {
		t.Fatalf("DNS records = %d, want 1", len(cfg.GetDns().GetRecords()))
	}
	foundStatic, foundDiscovered := false, false
	for _, pp := range cfg.GetPublishedPrefixes() {
		if pp.GetPrefix() == "100.64.2.0/24" {
			foundStatic = true
		}
		if pp.GetPrefix() == "100.64.0.8/32" {
			foundDiscovered = true
		}
	}
	if !foundStatic {
		t.Fatal("应包含 static_mapping 前缀")
	}
	if !foundDiscovered {
		t.Fatal("应包含 discovered winner /32")
	}
	if len(cfg.GetEgressRules()) != 0 {
		t.Fatalf("node-b 不应有 egress rules, got %d", len(cfg.GetEgressRules()))
	}
	cfgA, err := f.h.buildNodeConfig("node-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgA.GetEgressRules()) == 0 {
		t.Fatal("node-a 应有 egress rules")
	}
	if len(cfgA.GetDiscoveryConfigs()) == 0 {
		t.Fatal("node-a 应有 discovery configs")
	}
}
