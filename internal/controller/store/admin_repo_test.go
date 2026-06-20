package store

import (
	"errors"
	"testing"
	"time"
)

// ---------- ListEnrollKeys ----------

func TestListEnrollKeys(t *testing.T) {
	s := newMemStore(t)
	exp := time.Now().Add(time.Hour)
	if err := s.CreateEnrollKey(&EnrollKey{Key: "k1", Reusable: true, Tag: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEnrollKey(&EnrollKey{Key: "k2", ExpiresAt: &exp, Tag: "bob"}); err != nil {
		t.Fatal(err)
	}
	keys, err := s.ListEnrollKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
}

func TestListEnrollKeysEmpty(t *testing.T) {
	s := newMemStore(t)
	keys, err := s.ListEnrollKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("got %d keys, want 0", len(keys))
	}
}

// ---------- ListACLPolicies ----------

func TestListACLPolicies(t *testing.T) {
	s := newMemStore(t)
	if _, err := s.SaveACLPolicy(`{"acls":[]}`, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveACLPolicy(`{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`, "admin"); err != nil {
		t.Fatal(err)
	}
	pols, err := s.ListACLPolicies()
	if err != nil {
		t.Fatal(err)
	}
	if len(pols) != 2 {
		t.Fatalf("got %d policies, want 2", len(pols))
	}
	// 期望按版本降序（最新在前）。
	if pols[0].Version < pols[1].Version {
		t.Fatalf("not sorted desc: %d before %d", pols[0].Version, pols[1].Version)
	}
}

// ---------- ListCerts ----------

func TestListCerts(t *testing.T) {
	s := newMemStore(t)
	if err := s.RecordCert(&Cert{Serial: "1", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCert(&Cert{Serial: "2", NodeID: "n2", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeCert("2"); err != nil {
		t.Fatal(err)
	}
	certs, err := s.ListCerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("got %d certs, want 2", len(certs))
	}
	var revoked int
	for _, c := range certs {
		if c.Revoked {
			revoked++
		}
	}
	if revoked != 1 {
		t.Fatalf("got %d revoked, want 1", revoked)
	}
}

// ---------- ListCertsByNode ----------

func TestListCertsByNode(t *testing.T) {
	s := newMemStore(t)
	if err := s.RecordCert(&Cert{Serial: "10", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCert(&Cert{Serial: "11", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCert(&Cert{Serial: "12", NodeID: "n2", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	certs, err := s.ListCertsByNode("n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 2 {
		t.Fatalf("got %d certs for n1, want 2", len(certs))
	}
}

// ---------- GetLeasesByNode ----------

func TestGetLeasesByNode(t *testing.T) {
	s := newMemStore(t)
	if err := s.AllocateLease("100.64.0.5", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AllocateLease("100.64.0.6", "n2"); err != nil {
		t.Fatal(err)
	}
	leases, err := s.GetLeasesByNode("n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].IP != "100.64.0.5" {
		t.Fatalf("got %+v, want single lease 100.64.0.5", leases)
	}
}

// ---------- DeleteNode ----------

func TestDeleteNode(t *testing.T) {
	s := newMemStore(t)
	n := &Node{ID: "n1", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2/32"}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode("n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNode("n1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetNode after delete: got %v, want ErrNotFound", err)
	}
}

func TestDeleteNodeMissing(t *testing.T) {
	s := newMemStore(t)
	// 删除不存在节点不报错（幂等）。
	if err := s.DeleteNode("nope"); err != nil {
		t.Fatalf("DeleteNode missing: %v", err)
	}
}
