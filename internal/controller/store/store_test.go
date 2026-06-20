package store

import (
	"errors"
	"testing"
)

func newMemStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrateCreatesTables(t *testing.T) {
	s := newMemStore(t)
	for _, tbl := range []string{"nodes", "leases", "enroll_keys", "certs", "relay_links", "acl_policies"} {
		if !s.DB().Migrator().HasTable(tbl) {
			t.Errorf("缺少表: %s", tbl)
		}
	}
}

func TestCreateNodeAndBumpGeneration(t *testing.T) {
	s := newMemStore(t)
	n := &Node{ID: "n1", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2"}
	if err := s.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	got, err := s.GetNode("n1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Generation != 0 {
		t.Fatalf("初始 generation = %d, want 0", got.Generation)
	}
	// per-node 单调递增（§5.3）
	g1, _ := s.BumpGeneration("n1")
	g2, _ := s.BumpGeneration("n1")
	if g1 != 1 || g2 != 2 {
		t.Fatalf("generation 序列 = %d,%d, want 1,2", g1, g2)
	}
}

func TestGetNodeNotFound(t *testing.T) {
	s := newMemStore(t)
	_, err := s.GetNode("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCertIssueAndRevoke(t *testing.T) {
	s := newMemStore(t)
	if err := s.RecordCert(&Cert{Serial: "1001", NodeID: "n1"}); err != nil {
		t.Fatalf("RecordCert: %v", err)
	}
	if err := s.RevokeCert("1001"); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	serials, err := s.RevokedSerials()
	if err != nil {
		t.Fatalf("RevokedSerials: %v", err)
	}
	if len(serials) != 1 || serials[0] != "1001" {
		t.Fatalf("吊销序列 = %v, want [1001]", serials)
	}
}

func TestLeaseAllocateReleaseRoundTrip(t *testing.T) {
	s := newMemStore(t)
	if err := s.AllocateLease("100.64.0.2", "n1"); err != nil {
		t.Fatalf("AllocateLease: %v", err)
	}
	// 重复分配同一 IP 给不同节点应失败（唯一约束）
	if err := s.AllocateLease("100.64.0.2", "n2"); err == nil {
		t.Fatal("重复分配应失败")
	}
	if err := s.ReleaseLease("100.64.0.2"); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	// 回收后可再次分配
	if err := s.AllocateLease("100.64.0.2", "n2"); err != nil {
		t.Fatalf("回收后再分配应成功: %v", err)
	}
}
