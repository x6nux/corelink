package enroll_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sync"
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/enroll"
	"github.com/x6nux/corelink/internal/controller/ipam"
	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/pki"
)

// ---------- 测试辅助 ----------

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

func mustCA(t *testing.T, st *store.Store) *ca.Manager {
	t.Helper()
	encKey := make([]byte, 32)
	m, err := ca.EnsureCA(st, "TestCA", encKey)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	return m
}

func mustIPAM(t *testing.T, st *store.Store) *ipam.Allocator {
	t.Helper()
	a, err := ipam.New("10.100.0.0/24", st)
	if err != nil {
		t.Fatalf("ipam.New: %v", err)
	}
	return a
}

func mustCSR(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	der, key, err := pki.GenerateCSR(cn)
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}
	return der, key
}

// newPeerCtx 构造带 mTLS peer info 的 context（模拟 gRPC 运行时注入）。
func newPeerCtx(t *testing.T, cert *x509.Certificate) context.Context {
	t.Helper()
	tlsInfo := credentials.TLSInfo{
		State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{cert},
		},
	}
	p := &peer.Peer{AuthInfo: tlsInfo}
	return peer.NewContext(context.Background(), p)
}

// ---------- 正常注册全流程 ----------

func TestEnroll_ValidKey_FullFlow(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "test-key-valid",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-alice",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	csrDER, _ := mustCSR(t, "node-csr-cn")

	resp, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "test-key-valid",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub-key-abc",
		CsrDer:        csrDER,
		Hostname:      "node1.local",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// 检查响应字段非空
	if resp.NodeId == "" {
		t.Error("NodeId should not be empty")
	}
	if resp.VirtualIp == "" {
		t.Error("VirtualIp should not be empty")
	}
	if len(resp.NodeCertDer) == 0 {
		t.Error("NodeCertDer should not be empty")
	}
	if len(resp.CaCertDer) == 0 {
		t.Error("CaCertDer should not be empty")
	}

	// 证书能被 CA 验证
	pool := x509.NewCertPool()
	pool.AddCert(caM.Cert())
	nodeCert, err := x509.ParseCertificate(resp.NodeCertDer)
	if err != nil {
		t.Fatalf("parse node cert: %v", err)
	}
	if _, err := nodeCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("node cert verify failed: %v", err)
	}

	// CN == nodeID
	if nodeCert.Subject.CommonName != resp.NodeId {
		t.Errorf("cert CN %q != nodeID %q", nodeCert.Subject.CommonName, resp.NodeId)
	}

	// 检查 IP 已分配（IPAM 内存 & store.Lease）
	leases, err := st.ListLeases()
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	found := false
	for _, l := range leases {
		if l.IP == resp.VirtualIp {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("VirtualIP %s not found in leases", resp.VirtualIp)
	}

	// 检查 Node 已建（User == key.Tag）
	node, err := st.GetNode(resp.NodeId)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.User != "user-alice" {
		t.Errorf("node.User = %q, want user-alice", node.User)
	}
	if node.WGPubKey != "wg-pub-key-abc" {
		t.Errorf("node.WGPubKey = %q, want wg-pub-key-abc", node.WGPubKey)
	}
	if node.Role != "node" {
		t.Errorf("node.Role = %q, want agent", node.Role)
	}

	// 一次性 key 已被消费（consumed=true），但非管理员吊销（revoked=false）。
	ek, err := st.GetEnrollKey("test-key-valid")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if !ek.Consumed {
		t.Error("one-shot key should be consumed after use")
	}
	if ek.Revoked {
		t.Error("消费不应设置 Revoked（吊销专属管理员语义）")
	}
}

// ---------- relay 角色 ----------

func TestEnroll_RelayRole(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "relay-key",
		Reusable:  true,
		ExpiresAt: &exp,
		Tag:       "relay-group",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	csrDER, _ := mustCSR(t, "relay-node")
	resp, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "relay-key",
		Role:          genv1.NodeRole_NODE_ROLE_RELAY,
		WgPublicKey:   "relay-wg-pub",
		CsrDer:        csrDER,
		Hostname:      "relay1.local",
	})
	if err != nil {
		t.Fatalf("Enroll relay: %v", err)
	}

	node, err := st.GetNode(resp.NodeId)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.Role != "node" {
		t.Errorf("node.Role = %q, want relay", node.Role)
	}

	// 可重用 key 既不吊销也不消费
	ek, err := st.GetEnrollKey("relay-key")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if ek.Revoked {
		t.Error("reusable key should NOT be revoked after use")
	}
	if ek.Consumed {
		t.Error("reusable key should NOT be consumed after use")
	}
}

// ---------- 无效 key ----------

func TestEnroll_InvalidKey(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	csrDER, _ := mustCSR(t, "x")
	_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "nonexistent-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub",
		CsrDer:        csrDER,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected NotFound, got %v", status.Code(err))
	}
}

// ---------- 已过期 key ----------

func TestEnroll_ExpiredKey(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	exp := time.Now().Add(-time.Hour) // 已过期
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "expired-key",
		Reusable:  false,
		ExpiresAt: &exp,
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	csrDER, _ := mustCSR(t, "x")
	_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "expired-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub",
		CsrDer:        csrDER,
	})
	if err == nil {
		t.Fatal("expected error for expired key")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// ---------- 已吊销 key ----------

func TestEnroll_RevokedKey(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "revoked-key",
		Reusable:  false,
		ExpiresAt: &exp,
		Revoked:   true,
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	csrDER, _ := mustCSR(t, "x")
	_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "revoked-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub",
		CsrDer:        csrDER,
	})
	if err == nil {
		t.Fatal("expected error for revoked key")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// ---------- 一次性 key 二次使用拒绝 ----------

func TestEnroll_OneShotKey_SecondUseRejected(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "oneshot-key",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-bob",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// 第一次成功
	csrDER1, _ := mustCSR(t, "node1")
	_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "oneshot-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub-1",
		CsrDer:        csrDER1,
		Hostname:      "n1.local",
	})
	if err != nil {
		t.Fatalf("first Enroll: %v", err)
	}

	// 第二次被拒绝（key 已吊销）
	csrDER2, _ := mustCSR(t, "node2")
	_, err = svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "oneshot-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub-2",
		CsrDer:        csrDER2,
		Hostname:      "n2.local",
	})
	if err == nil {
		t.Fatal("expected error for second use of one-shot key")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// ---------- 一次性 key 并发重放（S-1 回归） ----------

// TestEnroll_OneShotKey_Concurrent N 个 goroutine 并发用同一一次性 key Enroll，
// 断言恰好 1 个成功、其余被拒 PermissionDenied，且只建 1 个 Node、分配 1 个 IP。
// 验证原子消费消除 TOCTOU 重放（配合 -race 运行）。
func TestEnroll_OneShotKey_Concurrent(t *testing.T) {
	st := mustStore(t)
	// :memory: SQLite 多连接下各连接独立库，限制单连接保证并发一致视图。
	sqlDB, err := st.DB().DB()
	if err != nil {
		t.Fatalf("DB(): %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "concurrent-oneshot",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-race",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	const n = 16
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		successCnt int
		deniedCnt  int
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			csrDER, _, _ := pki.GenerateCSR(fmt.Sprintf("race-node-%d", i))
			<-start
			_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
				EnrollmentKey: "concurrent-oneshot",
				Role:          genv1.NodeRole_NODE_ROLE_AGENT,
				WgPublicKey:   fmt.Sprintf("wg-race-%d", i),
				CsrDer:        csrDER,
				Hostname:      fmt.Sprintf("race-%d.local", i),
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCnt++
				return
			}
			if status.Code(err) == codes.PermissionDenied {
				deniedCnt++
				return
			}
			t.Errorf("意外错误码 %v: %v", status.Code(err), err)
		}()
	}
	close(start)
	wg.Wait()

	if successCnt != 1 {
		t.Fatalf("恰好应有 1 个 Enroll 成功，实际 %d（denied=%d）", successCnt, deniedCnt)
	}
	if deniedCnt != n-1 {
		t.Fatalf("应有 %d 个被拒，实际 %d", n-1, deniedCnt)
	}

	// 仅建 1 个 Node
	nodes, err := st.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("应只建 1 个 Node，实际 %d", len(nodes))
	}
	// 仅分配 1 个 IP（lease）
	leases, err := st.ListLeases()
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("应只分配 1 个 IP，实际 %d", len(leases))
	}
}

// ---------- Renew ----------

func TestRenew_ValidMTLSContext(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	// 先注册一个节点
	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "renew-test-key",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-carol",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}
	csrDER, _ := mustCSR(t, "renew-test-node")
	resp, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "renew-test-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub-renew",
		CsrDer:        csrDER,
		Hostname:      "renew.local",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// 解析颁发的证书，构造 peer context
	issuedCert, err := x509.ParseCertificate(resp.NodeCertDer)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}

	ctx := newPeerCtx(t, issuedCert)

	// 提交新 CSR 续签
	newCSR, _ := mustCSR(t, "renew-test-node-new")
	renewResp, err := svc.Renew(ctx, &genv1.RenewRequest{CsrDer: newCSR})
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}

	if len(renewResp.NodeCertDer) == 0 {
		t.Error("Renew should return a new cert")
	}

	// 新证书能被 CA 验证
	pool := x509.NewCertPool()
	pool.AddCert(caM.Cert())
	newCert, err := x509.ParseCertificate(renewResp.NodeCertDer)
	if err != nil {
		t.Fatalf("parse renewed cert: %v", err)
	}
	if _, err := newCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("renewed cert verify failed: %v", err)
	}
	// CN 应保持 nodeID
	if newCert.Subject.CommonName != resp.NodeId {
		t.Errorf("renewed cert CN %q != nodeID %q", newCert.Subject.CommonName, resp.NodeId)
	}
}

// ---------- Renew 已吊销证书拒绝 ----------

// TestRenew_RevokedCertRejected 旧证书已吊销时 Renew 返回 PermissionDenied，
// 不签发新证书（封堵吊销节点凭旧证书自助续命的攻击链 #5）。
func TestRenew_RevokedCertRejected(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	// 先注册一个节点
	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "revoke-renew-key",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-dan",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}
	csrDER, _ := mustCSR(t, "revoke-renew-node")
	resp, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "revoke-renew-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-pub",
		CsrDer:        csrDER,
		Hostname:      "rr.local",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	issuedCert, err := x509.ParseCertificate(resp.NodeCertDer)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	// 吊销该节点的当前证书
	if err := st.RevokeCert(issuedCert.SerialNumber.Text(10)); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	// 持吊销证书续签 → PermissionDenied
	ctx := newPeerCtx(t, issuedCert)
	newCSR, _ := mustCSR(t, "revoke-renew-node-new")
	_, err = svc.Renew(ctx, &genv1.RenewRequest{CsrDer: newCSR})
	if err == nil {
		t.Fatal("expected error for revoked cert Renew")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v: %v", status.Code(err), err)
	}
}

// ---------- Renew 无 mTLS context ----------

func TestRenew_NoMTLSContext(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	csrDER, _ := mustCSR(t, "x")
	_, err := svc.Renew(context.Background(), &genv1.RenewRequest{CsrDer: csrDER})
	if err == nil {
		t.Fatal("expected error for Renew without mTLS context")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", status.Code(err))
	}
}

// ---------- enroll 失败路径补偿（#17 + #33） ----------

// TestEnroll_IssueFailure_KeyNotBurned 验证 #17：一次性 key 抢占后若签证书失败
// （非法 CSR），key 必须被补偿复位为可用而非永久烧毁；用同一 key 携合法 CSR 重试应成功。
func TestEnroll_IssueFailure_KeyNotBurned(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&store.EnrollKey{
		Key:       "burn-key",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-burn",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// 第一次：非法 CSR（随机字节）→ 签证书失败 → 应返回 Internal。
	badCSR := []byte("not-a-valid-csr-der-bytes")
	_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "burn-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-burn-1",
		CsrDer:        badCSR,
		Hostname:      "burn1.local",
	})
	if err == nil {
		t.Fatal("非法 CSR 应导致 Enroll 失败")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v: %v", status.Code(err), err)
	}

	// 关键断言：失败后一次性 key 不应被永久烧毁（consumed 应已补偿复位）。
	ek, err := st.GetEnrollKey("burn-key")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if ek.Consumed {
		t.Fatal("#17：签证书失败后一次性 key 被永久烧毁，应补偿复位")
	}

	// 用同一 key 携带合法 CSR 重试，应成功。
	goodCSR, _ := mustCSR(t, "burn-node-retry")
	resp, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "burn-key",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
		WgPublicKey:   "wg-burn-2",
		CsrDer:        goodCSR,
		Hostname:      "burn2.local",
	})
	if err != nil {
		t.Fatalf("补偿后重试 Enroll 应成功: %v", err)
	}
	if resp.NodeId == "" {
		t.Error("重试成功后 NodeId 不应为空")
	}

	// 重试成功后 key 才应被消费。
	ek2, err := st.GetEnrollKey("burn-key")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if !ek2.Consumed {
		t.Error("成功 Enroll 后一次性 key 应被消费")
	}
}

// TestEnroll_RevokeDuringEnroll_NotRevived 是本次修复的核心 enroll 端到端用例：
// 在抢占（Consume）成功后、补偿（Unconsume）之前，管理员吊销该 key；补偿必须只复位
// consumed、不复活吊销。通过 CreateNodeFn 在抢占后、补偿前精确注入管理员吊销，
// 模拟 TOCTOU 窗口。
func TestEnroll_RevokeDuringEnroll_NotRevived(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	if err := st.CreateEnrollKey(&store.EnrollKey{Key: "revive-key", Tag: "u-revive"}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// 注入：CreateNode 这一步（已在 Consume 成功之后）失败，且在失败前管理员吊销 key。
	// 失败触发 compensateKey → UnconsumeOneTimeKey，此时 key 已 revoked=true。
	svc.CreateNodeFn = func(_ *store.Node) error {
		if err := st.RevokeEnrollKey("revive-key"); err != nil {
			t.Errorf("RevokeEnrollKey: %v", err)
		}
		return fmt.Errorf("注入失败触发补偿")
	}

	csr, _ := mustCSR(t, "revive-node")
	_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "revive-key",
		CsrDer:        csr,
		Hostname:      "revive.local",
		WgPublicKey:   "wg-revive",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("期望 Internal，得到 %v", err)
	}

	// 核心断言：补偿后吊销保持。
	ek, err := st.GetEnrollKey("revive-key")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if !ek.Revoked {
		t.Fatal("补偿复活了管理员吊销的 key（Revoked 被错误复位）")
	}

	// 已吊销 key 再次入网仍被拒（PermissionDenied）。
	svc.CreateNodeFn = nil
	csr2, _ := mustCSR(t, "revive-node-2")
	_, err = svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "revive-key",
		CsrDer:        csr2,
		Hostname:      "revive2.local",
		WgPublicKey:   "wg-revive-2",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("已吊销 key 入网应被拒 PermissionDenied，得到 %v", err)
	}
}

// TestEnroll_CreateNodeFails_NoOrphanCert 验证 #33：CreateNode 失败时刚签发的证书
// 记录必须被补偿删除（无孤儿有效证书、不污染 CRL），IP 被释放，且一次性 key 不被烧毁。
func TestEnroll_CreateNodeFails_NoOrphanCert(t *testing.T) {
	st := mustStore(t)
	caM := mustCA(t, st)
	ipamA := mustIPAM(t, st)
	svc := enroll.NewService(st, caM, ipamA)

	// 注入 CreateNode 失败，模拟建节点记录这一步出错。
	svc.CreateNodeFn = func(_ *store.Node) error {
		return fmt.Errorf("注入的 CreateNode 失败")
	}

	// 准备一把有效的一次性 enrollment key。
	if err := st.CreateEnrollKey(&store.EnrollKey{Key: "k-33", Tag: "u1"}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}
	csr, _ := mustCSR(t, "ignored")

	_, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "k-33",
		CsrDer:        csr,
		Hostname:      "h1",
		WgPublicKey:   "pub1",
		Role:          genv1.NodeRole_NODE_ROLE_AGENT,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("期望 Internal，得到 err=%v", err)
	}

	// 断言 1（#33）：不应残留任何证书记录（孤儿证书）。
	certCnt, e := st.CountCerts()
	if e != nil {
		t.Fatalf("CountCerts: %v", e)
	}
	if certCnt != 0 {
		t.Fatalf("CreateNode 失败后仍残留 %d 条证书记录（孤儿证书）", certCnt)
	}

	// 断言 2（#33）：被吊销序列号列表应为空（证书是删除而非吊销，不污染 CRL）。
	revoked, e := st.RevokedSerials()
	if e != nil {
		t.Fatalf("RevokedSerials: %v", e)
	}
	if len(revoked) != 0 {
		t.Fatalf("不应有吊销记录，得到 %v", revoked)
	}

	// 断言 3（#17）：一次性 key 不应被烧毁（CreateNode 失败路径也要补偿复位）。
	ek, e := st.GetEnrollKey("k-33")
	if e != nil {
		t.Fatalf("GetEnrollKey: %v", e)
	}
	if ek.Consumed {
		t.Fatal("#17：CreateNode 失败后一次性 key 被永久烧毁，应补偿复位")
	}

	// 断言 4：IP 已释放 —— 紧接一次成功注册应能拿到 IP。
	svc.CreateNodeFn = nil // 恢复默认（走真实 s.st.CreateNode）
	if err := st.CreateEnrollKey(&store.EnrollKey{Key: "k-33b", Tag: "u1"}); err != nil {
		t.Fatalf("CreateEnrollKey2: %v", err)
	}
	csr2, _ := mustCSR(t, "ignored")
	resp, err := svc.Enroll(context.Background(), &genv1.EnrollRequest{
		EnrollmentKey: "k-33b", CsrDer: csr2, Hostname: "h2", WgPublicKey: "pub2",
		Role: genv1.NodeRole_NODE_ROLE_AGENT,
	})
	if err != nil {
		t.Fatalf("后续注册应成功: %v", err)
	}
	if resp.VirtualIp == "" {
		t.Fatal("后续注册未分配 IP（IP 未被释放）")
	}
}
