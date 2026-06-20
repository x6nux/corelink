package routepolicy

import (
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

func TestResolveDirectRoute(t *testing.T) {
	in := ResolveInput{
		Routes: []store.PublishedRoute{
			{ID: 1, NodeID: "node-a", Kind: "direct", RouteCIDR: "10.0.0.0/16", Priority: 100, SNAT: true, Enabled: true},
		},
		Now: time.Now(),
	}
	out := Resolve(in)

	prefixes := out.PublishedPrefixes["node-a"]
	if len(prefixes) != 1 || prefixes[0] != "10.0.0.0/16" {
		t.Fatalf("prefixes = %v, want [10.0.0.0/16]", prefixes)
	}

	rules := out.EgressRules["node-a"]
	if len(rules) != 1 || rules[0].Kind != "direct" {
		t.Fatalf("egress rules = %v", rules)
	}
}

func TestResolveStaticMapping(t *testing.T) {
	in := ResolveInput{
		Routes: []store.PublishedRoute{
			{ID: 2, NodeID: "node-a", Kind: "static_mapping", VIPCIDR: "100.64.2.0/24", TargetCIDR: "10.0.2.0/24", Priority: 100, SNAT: true, Enabled: true},
		},
		Now: time.Now(),
	}
	out := Resolve(in)

	prefixes := out.PublishedPrefixes["node-a"]
	if len(prefixes) != 1 || prefixes[0] != "100.64.2.0/24" {
		t.Fatalf("prefixes = %v, want [100.64.2.0/24]", prefixes)
	}

	rules := out.EgressRules["node-a"]
	if len(rules) != 1 || rules[0].TargetPrefix != "10.0.2.0/24" {
		t.Fatalf("egress rules = %v", rules)
	}
}

func TestResolveDiscoveredMapping(t *testing.T) {
	in := ResolveInput{
		Routes: []store.PublishedRoute{
			{ID: 3, NodeID: "node-a", Kind: "discovered_mapping", VIPCIDR: "100.64.0.0/16", TargetCIDR: "10.1.0.0/16", Priority: 100, SNAT: true, Enabled: true},
		},
		Discovered: []store.DiscoveredMapping{
			{RouteID: 3, NodeID: "node-a", TargetIP: "10.1.0.8", VIPIP: "100.64.0.8", Winner: true},
			{RouteID: 3, NodeID: "node-a", TargetIP: "10.1.0.9", VIPIP: "100.64.0.9", Winner: false},
		},
		Now: time.Now(),
	}
	out := Resolve(in)

	prefixes := out.PublishedPrefixes["node-a"]
	if len(prefixes) != 1 || prefixes[0] != "100.64.0.8/32" {
		t.Fatalf("只应发布 winner /32, got %v", prefixes)
	}

	rules := out.EgressRules["node-a"]
	if len(rules) != 1 || rules[0].VipPrefix != "100.64.0.8/32" || rules[0].TargetPrefix != "10.1.0.8/32" {
		t.Fatalf("egress rules = %+v", rules)
	}

	disc := out.DiscoveryConfigs["node-a"]
	if len(disc) != 1 || disc[0].Mode != "arp" {
		t.Fatalf("discovery configs = %v", disc)
	}
}

func TestResolveDNSFromAliases(t *testing.T) {
	in := ResolveInput{
		Aliases: []store.NodeAlias{
			{NodeID: "node-a", Name: "db", FQDN: "db.corelink.internal", Kind: "internal", TargetVIP: "100.64.0.10", Enabled: true},
			{NodeID: "node-b", Name: "web", FQDN: "web.example.com", Kind: "external", TargetVIP: "100.64.0.20", Enabled: true},
		},
		DNSSettings: &store.DNSSettings{
			Enabled:       true,
			ZonesJSON:     `["corelink.internal"]`,
			UpstreamsJSON: `["8.8.8.8"]`,
			InterceptMode: "local",
			ListenAddr:    "127.0.0.1",
			ListenPort:    5353,
		},
		Now: time.Now(),
	}
	out := Resolve(in)

	if len(out.DNSRecords) != 2 {
		t.Fatalf("DNS records = %d, want 2", len(out.DNSRecords))
	}
	if out.DNSRecords[0].Fqdn != "db.corelink.internal" {
		t.Fatalf("first record fqdn = %q", out.DNSRecords[0].Fqdn)
	}

	if out.DNSConfig == nil || !out.DNSConfig.Enabled {
		t.Fatal("DNS config 应该启用")
	}
	if out.DNSConfig.ListenPort != 5353 {
		t.Fatalf("listen port = %d", out.DNSConfig.ListenPort)
	}
}

func TestResolveDisabledRouteSkipped(t *testing.T) {
	in := ResolveInput{
		Routes: []store.PublishedRoute{
			{ID: 1, NodeID: "node-a", Kind: "direct", RouteCIDR: "10.0.0.0/16", Enabled: false},
		},
	}
	out := Resolve(in)
	if len(out.PublishedPrefixes) != 0 {
		t.Fatalf("禁用路由不应出现: %v", out.PublishedPrefixes)
	}
}

func TestResolveMixedRoutes(t *testing.T) {
	in := ResolveInput{
		Routes: []store.PublishedRoute{
			{ID: 1, NodeID: "node-a", Kind: "direct", RouteCIDR: "10.0.0.0/16", Priority: 100, SNAT: true, Enabled: true},
			{ID: 2, NodeID: "node-a", Kind: "static_mapping", VIPCIDR: "100.64.2.0/24", TargetCIDR: "10.0.2.0/24", Priority: 100, SNAT: true, Enabled: true},
		},
		Discovered: []store.DiscoveredMapping{
			{RouteID: 3, NodeID: "node-a", TargetIP: "10.1.0.8", VIPIP: "100.64.0.8", Winner: true},
		},
		Aliases: []store.NodeAlias{
			{NodeID: "node-a", Name: "db", FQDN: "db.corelink.internal", TargetVIP: "100.64.0.10", Enabled: true},
		},
		Now: time.Now(),
	}
	out := Resolve(in)

	prefixes := out.PublishedPrefixes["node-a"]
	if len(prefixes) != 2 {
		t.Fatalf("prefixes = %v, want 2 entries", prefixes)
	}

	if len(out.DNSRecords) != 1 {
		t.Fatalf("DNS records = %d, want 1", len(out.DNSRecords))
	}
}

func TestResolveEmptyInput(t *testing.T) {
	out := Resolve(ResolveInput{Now: time.Now()})

	if out.PublishedPrefixes == nil {
		t.Fatal("PublishedPrefixes 不应为 nil")
	}
	if len(out.PublishedPrefixes) != 0 {
		t.Fatalf("PublishedPrefixes 应为空, got %v", out.PublishedPrefixes)
	}
	if len(out.DNSRecords) != 0 {
		t.Fatalf("DNSRecords 应为空, got %v", out.DNSRecords)
	}
	if out.DNSConfig != nil {
		t.Fatalf("DNSConfig 应为 nil, got %+v", out.DNSConfig)
	}
}

func TestResolveIPv6Alias(t *testing.T) {
	in := ResolveInput{
		Aliases: []store.NodeAlias{
			{NodeID: "node-a", Name: "db6", FQDN: "db6.corelink.internal", Kind: "internal", TargetVIP: "fd00::10", Enabled: true},
		},
		Now: time.Now(),
	}
	out := Resolve(in)

	if len(out.DNSRecords) != 1 {
		t.Fatalf("DNS records = %d, want 1", len(out.DNSRecords))
	}
	if out.DNSRecords[0].RecordType != "AAAA" {
		t.Fatalf("RecordType = %q, want AAAA", out.DNSRecords[0].RecordType)
	}
}

func TestResolveDisabledAliasSkipped(t *testing.T) {
	in := ResolveInput{
		Aliases: []store.NodeAlias{
			{NodeID: "node-a", Name: "db", FQDN: "db.corelink.internal", Kind: "internal", TargetVIP: "100.64.0.10", Enabled: false},
		},
		Now: time.Now(),
	}
	out := Resolve(in)

	if len(out.DNSRecords) != 0 {
		t.Fatalf("禁用别名不应生成 DNS record: %v", out.DNSRecords)
	}
}
