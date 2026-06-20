package store

import "testing"

func TestSplitRuleCRUD(t *testing.T) {
	s := newMemStore(t)

	r := &SplitRuleRow{Match: "geoip:cn", Action: "direct", SortOrder: 10, Enabled: true}
	if err := s.CreateSplitRule(r); err != nil {
		t.Fatalf("CreateSplitRule: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("期望自增 ID > 0")
	}

	all, err := s.ListSplitRules()
	if err != nil {
		t.Fatalf("ListSplitRules: %v", err)
	}
	if len(all) != 1 || all[0].Match != "geoip:cn" {
		t.Fatalf("期望 1 条规则, got %d", len(all))
	}

	if err := s.DeleteSplitRule(r.ID); err != nil {
		t.Fatalf("DeleteSplitRule: %v", err)
	}
	all, _ = s.ListSplitRules()
	if len(all) != 0 {
		t.Fatalf("删除后期望 0 条, got %d", len(all))
	}
}

func TestGeoIPMetaCRUD(t *testing.T) {
	s := newMemStore(t)

	m := &GeoIPMeta{SHA256: "abc123", FilePath: "/tmp/geoip.dat", FileSize: 1024}
	if err := s.UpsertGeoIPMeta(m); err != nil {
		t.Fatalf("UpsertGeoIPMeta: %v", err)
	}

	got, err := s.GetLatestGeoIPMeta()
	if err != nil {
		t.Fatalf("GetLatestGeoIPMeta: %v", err)
	}
	if got.SHA256 != "abc123" {
		t.Fatalf("期望 SHA256=abc123, got %s", got.SHA256)
	}
}
