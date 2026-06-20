package store_test

import (
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

func mustStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// TestIsCertRevoked 覆盖：未吊销→false、已吊销→true、不存在的序列号→false。
func TestIsCertRevoked(t *testing.T) {
	st := mustStore(t)

	// 录入两张证书：一张保持有效，一张随后吊销。
	if err := st.RecordCert(&store.Cert{Serial: "100", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("RecordCert 100: %v", err)
	}
	if err := st.RecordCert(&store.Cert{Serial: "200", NodeID: "n2", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("RecordCert 200: %v", err)
	}

	// 未吊销 → false
	revoked, err := st.IsCertRevoked("100")
	if err != nil {
		t.Fatalf("IsCertRevoked 100: %v", err)
	}
	if revoked {
		t.Errorf("序列号 100 未吊销，应返回 false")
	}

	// 吊销 200 后 → true
	if err := st.RevokeCert("200"); err != nil {
		t.Fatalf("RevokeCert 200: %v", err)
	}
	revoked, err = st.IsCertRevoked("200")
	if err != nil {
		t.Fatalf("IsCertRevoked 200: %v", err)
	}
	if !revoked {
		t.Errorf("序列号 200 已吊销，应返回 true")
	}

	// 不存在的序列号 → false（无错误）
	revoked, err = st.IsCertRevoked("999")
	if err != nil {
		t.Fatalf("IsCertRevoked 999: %v", err)
	}
	if revoked {
		t.Errorf("不存在的序列号应返回 false")
	}
}

// TestDeleteCert: 删除证书记录后，该序列号不再出现在任何查询中（用于补偿孤儿证书）。
func TestDeleteCert(t *testing.T) {
	st := mustStore(t)
	if err := st.RecordCert(&store.Cert{Serial: "777", NodeID: "n1", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("RecordCert: %v", err)
	}

	// 删除刚记录的证书
	if err := st.DeleteCert("777"); err != nil {
		t.Fatalf("DeleteCert: %v", err)
	}

	// 记录应已不存在（用 CountCerts 断言总数归零）
	n, err := st.CountCerts()
	if err != nil {
		t.Fatalf("CountCerts: %v", err)
	}
	if n != 0 {
		t.Fatalf("DeleteCert 后记录仍存在，count=%d", n)
	}

	// 删除不存在的序列号应幂等（不报错）
	if err := st.DeleteCert("nonexistent"); err != nil {
		t.Fatalf("DeleteCert 不存在序列号应幂等，却返回: %v", err)
	}
}

// TestGetCertFingerprint 覆盖：存在未吊销证书→返回指纹+ok=true、节点不存在→ok=false。
func TestGetCertFingerprint(t *testing.T) {
	st := mustStore(t)
	if err := st.RecordCert(&store.Cert{Serial: "s1", NodeID: "nodeA", Fingerprint: "abc123"}); err != nil {
		t.Fatal(err)
	}
	fp, ok, err := st.GetCertFingerprint("nodeA")
	if err != nil || !ok || fp != "abc123" {
		t.Fatalf("got (%q,%v,%v), want (abc123,true,nil)", fp, ok, err)
	}
	if _, ok, _ := st.GetCertFingerprint("missing"); ok {
		t.Fatal("missing 节点应返回 ok=false")
	}
}

// TestListActiveCertFingerprints 覆盖：多证书取最新、已吊销不出现、指纹为空不出现。
func TestListActiveCertFingerprints(t *testing.T) {
	st := mustStore(t)

	base := time.Now()

	// 节点 A：两张证书，应返回较新的 fpA2
	if err := st.RecordCert(&store.Cert{Serial: "a1", NodeID: "nodeA", Fingerprint: "fpA1", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordCert(&store.Cert{Serial: "a2", NodeID: "nodeA", Fingerprint: "fpA2", CreatedAt: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	// 节点 B：一张有效证书
	if err := st.RecordCert(&store.Cert{Serial: "b1", NodeID: "nodeB", Fingerprint: "fpB1", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}

	// 节点 C：证书已吊销，不应出现
	if err := st.RecordCert(&store.Cert{Serial: "c1", NodeID: "nodeC", Fingerprint: "fpC1", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeCert("c1"); err != nil {
		t.Fatal(err)
	}

	// 节点 D：指纹为空，不应出现
	if err := st.RecordCert(&store.Cert{Serial: "d1", NodeID: "nodeD", Fingerprint: "", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}

	m, err := st.ListActiveCertFingerprints()
	if err != nil {
		t.Fatalf("ListActiveCertFingerprints: %v", err)
	}

	// 应只包含 nodeA 和 nodeB
	if len(m) != 2 {
		t.Fatalf("期望 2 条记录，得 %d: %v", len(m), m)
	}
	if fp := m["nodeA"]; fp != "fpA2" {
		t.Errorf("nodeA 指纹应为 fpA2，得 %q", fp)
	}
	if fp := m["nodeB"]; fp != "fpB1" {
		t.Errorf("nodeB 指纹应为 fpB1，得 %q", fp)
	}
	if _, ok := m["nodeC"]; ok {
		t.Error("已吊销的 nodeC 不应出现")
	}
	if _, ok := m["nodeD"]; ok {
		t.Error("指纹为空的 nodeD 不应出现")
	}
}

// TestCountCerts: 统计当前证书记录总数（供合并后的 enroll 失败补偿测试断言无孤儿证书）。
func TestCountCerts(t *testing.T) {
	st := mustStore(t)

	n, err := st.CountCerts()
	if err != nil {
		t.Fatalf("CountCerts: %v", err)
	}
	if n != 0 {
		t.Fatalf("空库 CountCerts 应为 0，得 %d", n)
	}

	if err := st.RecordCert(&store.Cert{Serial: "888", NodeID: "n2", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("RecordCert: %v", err)
	}
	n, err = st.CountCerts()
	if err != nil {
		t.Fatalf("CountCerts: %v", err)
	}
	if n != 1 {
		t.Fatalf("记录 1 条后 CountCerts 应为 1，得 %d", n)
	}
}
