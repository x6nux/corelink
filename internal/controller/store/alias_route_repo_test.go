package store

import (
	"testing"
	"time"
)

func TestNodeAliasCRUD(t *testing.T) {
	s := newMemStore(t)
	a := &NodeAlias{
		NodeID:    "node-a",
		Name:      "db",
		FQDN:      "db.corelink.internal",
		Kind:      "internal",
		TargetVIP: "100.64.0.10",
		Enabled:   true,
	}
	if err := s.CreateNodeAlias(a); err != nil {
		t.Fatalf("CreateNodeAlias: %v", err)
	}
	if a.ID == 0 {
		t.Fatal("ID 应自增")
	}

	got, err := s.GetNodeAlias(a.ID)
	if err != nil {
		t.Fatalf("GetNodeAlias: %v", err)
	}
	if got.FQDN != "db.corelink.internal" {
		t.Fatalf("FQDN = %q, want db.corelink.internal", got.FQDN)
	}

	all, err := s.ListNodeAliases()
	if err != nil {
		t.Fatalf("ListNodeAliases: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len = %d, want 1", len(all))
	}

	byNode, err := s.ListNodeAliasesByNode("node-a")
	if err != nil {
		t.Fatalf("ListNodeAliasesByNode: %v", err)
	}
	if len(byNode) != 1 {
		t.Fatalf("len = %d, want 1", len(byNode))
	}

	if err := s.DeleteNodeAlias(a.ID); err != nil {
		t.Fatalf("DeleteNodeAlias: %v", err)
	}
	_, err = s.GetNodeAlias(a.ID)
	if err != ErrNotFound {
		t.Fatalf("删除后 GetNodeAlias: got %v, want ErrNotFound", err)
	}
}

func TestNodeAliasFQDNUnique(t *testing.T) {
	s := newMemStore(t)
	a1 := &NodeAlias{NodeID: "n1", Name: "db", FQDN: "db.corelink.internal", Kind: "internal", TargetVIP: "100.64.0.10"}
	a2 := &NodeAlias{NodeID: "n2", Name: "db2", FQDN: "db.corelink.internal", Kind: "internal", TargetVIP: "100.64.0.11"}
	if err := s.CreateNodeAlias(a1); err != nil {
		t.Fatalf("CreateNodeAlias a1: %v", err)
	}
	if err := s.CreateNodeAlias(a2); err == nil {
		t.Fatal("重复 FQDN 应失败")
	}
}

func TestPublishedRouteCRUD(t *testing.T) {
	s := newMemStore(t)
	r := &PublishedRoute{
		NodeID:     "node-a",
		Kind:       "static_mapping",
		VIPCIDR:    "100.64.2.0/24",
		TargetCIDR: "10.0.2.0/24",
		Priority:   100,
		SNAT:       true,
		Enabled:    true,
	}
	if err := s.CreatePublishedRoute(r); err != nil {
		t.Fatalf("CreatePublishedRoute: %v", err)
	}

	got, err := s.GetPublishedRoute(r.ID)
	if err != nil {
		t.Fatalf("GetPublishedRoute: %v", err)
	}
	if got.VIPCIDR != "100.64.2.0/24" {
		t.Fatalf("VIPCIDR = %q", got.VIPCIDR)
	}

	all, err := s.ListPublishedRoutes()
	if err != nil {
		t.Fatalf("ListPublishedRoutes: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len = %d, want 1", len(all))
	}

	if err := s.SetPublishedRouteEnabled(r.ID, false); err != nil {
		t.Fatalf("SetPublishedRouteEnabled: %v", err)
	}
	enabled, _ := s.ListPublishedRoutes()
	if len(enabled) != 0 {
		t.Fatal("禁用后不应出现在 enabled 列表中")
	}

	allRoutes, _ := s.ListAllPublishedRoutes()
	if len(allRoutes) != 1 {
		t.Fatal("ListAllPublishedRoutes 应包含禁用的路由")
	}

	if err := s.DeletePublishedRoute(r.ID); err != nil {
		t.Fatalf("DeletePublishedRoute: %v", err)
	}
}

func TestDNSSettingsUpsert(t *testing.T) {
	s := newMemStore(t)

	got, err := s.GetDNSSettings()
	if err != nil {
		t.Fatalf("GetDNSSettings 空表: %v", err)
	}
	if got != nil {
		t.Fatal("空表应返回 nil")
	}

	d := &DNSSettings{
		ID:            1,
		Enabled:       true,
		ZonesJSON:     `["corelink.internal"]`,
		UpstreamsJSON: `["8.8.8.8"]`,
		InterceptMode: "local",
		ListenAddr:    "127.0.0.1",
		ListenPort:    5353,
	}
	if err := s.UpsertDNSSettings(d); err != nil {
		t.Fatalf("UpsertDNSSettings: %v", err)
	}

	got, err = s.GetDNSSettings()
	if err != nil {
		t.Fatalf("GetDNSSettings: %v", err)
	}
	if !got.Enabled || got.ListenPort != 5353 {
		t.Fatalf("DNS 设置不匹配: enabled=%v port=%d", got.Enabled, got.ListenPort)
	}

	d.ListenPort = 5354
	if err := s.UpsertDNSSettings(d); err != nil {
		t.Fatalf("UpsertDNSSettings update: %v", err)
	}
	got, _ = s.GetDNSSettings()
	if got.ListenPort != 5354 {
		t.Fatalf("更新后 port = %d, want 5354", got.ListenPort)
	}
}

func TestDiscoveredMappingUpsert(t *testing.T) {
	s := newMemStore(t)
	r := &PublishedRoute{
		NodeID:     "node-a",
		Kind:       "discovered_mapping",
		VIPCIDR:    "100.64.0.0/16",
		TargetCIDR: "10.1.0.0/16",
		Enabled:    true,
	}
	if err := s.CreatePublishedRoute(r); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	m := &DiscoveredMapping{
		RouteID:    r.ID,
		NodeID:     "node-a",
		TargetIP:   "10.1.0.8",
		VIPIP:      "100.64.0.8",
		Priority:   100,
		ObservedAt: now,
		StaleAfter: 5 * time.Minute,
		Winner:     true,
	}
	if err := s.UpsertDiscoveredMapping(m); err != nil {
		t.Fatalf("UpsertDiscoveredMapping: %v", err)
	}
	if m.ID == 0 {
		t.Fatal("ID 应非零")
	}

	m.ObservedAt = now.Add(time.Second)
	if err := s.UpsertDiscoveredMapping(m); err != nil {
		t.Fatalf("UpsertDiscoveredMapping update: %v", err)
	}

	byRoute, err := s.ListDiscoveredMappingsByRoute(r.ID)
	if err != nil {
		t.Fatalf("ListDiscoveredMappingsByRoute: %v", err)
	}
	if len(byRoute) != 1 {
		t.Fatalf("len = %d, want 1", len(byRoute))
	}
}

func TestNodeAlias_DeleteNonExistent(t *testing.T) {
	s := newMemStore(t)
	// 删除不存在的 alias ID 应幂等成功，不报错
	if err := s.DeleteNodeAlias(99999); err != nil {
		t.Fatalf("删除不存在的 alias 应不报错, got %v", err)
	}
}

func TestDiscoveredMapping_EmptyList(t *testing.T) {
	s := newMemStore(t)
	// 空数据库下 ListFreshDiscoveredMappings 应返回空切片且不报错
	got, err := s.ListFreshDiscoveredMappings(time.Now())
	if err != nil {
		t.Fatalf("ListFreshDiscoveredMappings 空表: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("空表应返回空列表, got len = %d", len(got))
	}
}

func TestPublishedRoute_DuplicateKind(t *testing.T) {
	s := newMemStore(t)
	// 同一 node 创建多个相同 kind 的 route 应互不冲突
	r1 := &PublishedRoute{
		NodeID:     "node-a",
		Kind:       "static_mapping",
		VIPCIDR:    "100.64.2.0/24",
		TargetCIDR: "10.0.2.0/24",
		Enabled:    true,
	}
	r2 := &PublishedRoute{
		NodeID:     "node-a",
		Kind:       "static_mapping",
		VIPCIDR:    "100.64.3.0/24",
		TargetCIDR: "10.0.3.0/24",
		Enabled:    true,
	}
	if err := s.CreatePublishedRoute(r1); err != nil {
		t.Fatalf("CreatePublishedRoute r1: %v", err)
	}
	if err := s.CreatePublishedRoute(r2); err != nil {
		t.Fatalf("同一 node 同 kind 第二条 route 应成功: %v", err)
	}
	if r1.ID == r2.ID {
		t.Fatal("两条 route 应有不同的自增 ID")
	}

	byNode, err := s.ListPublishedRoutesByNode("node-a")
	if err != nil {
		t.Fatalf("ListPublishedRoutesByNode: %v", err)
	}
	if len(byNode) != 2 {
		t.Fatalf("node-a 应有 2 条 route, got %d", len(byNode))
	}
}

func TestMigrateNewTables(t *testing.T) {
	s := newMemStore(t)
	for _, tbl := range []string{"node_aliases", "published_routes", "discovered_mappings", "dns_settings"} {
		if !s.DB().Migrator().HasTable(tbl) {
			t.Errorf("缺少表: %s", tbl)
		}
	}
}
