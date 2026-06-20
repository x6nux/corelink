package store

import (
	"testing"
	"time"
)

// 测试 models.go 中定义的模型结构体字段与默认值。

func TestNodeFields(t *testing.T) {
	// 验证 Node 结构体字段赋值正确。
	now := time.Now()
	n := Node{
		ID:         "node-1",
		Role:       "node",
		Hostname:   "host-a",
		WGPubKey:   "pk-123",
		VirtualIP:  "100.64.0.1/32",
		User:       "alice",
		Generation: 7,
		Epoch:      3,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if n.ID != "node-1" || n.Role != "node" {
		t.Errorf("ID=%q Role=%q", n.ID, n.Role)
	}
	if n.Hostname != "host-a" || n.User != "alice" {
		t.Errorf("Hostname=%q User=%q", n.Hostname, n.User)
	}
	if n.WGPubKey != "pk-123" || n.VirtualIP != "100.64.0.1/32" {
		t.Errorf("WGPubKey=%q VirtualIP=%q", n.WGPubKey, n.VirtualIP)
	}
	if n.Generation != 7 || n.Epoch != 3 {
		t.Errorf("Generation=%d Epoch=%d", n.Generation, n.Epoch)
	}
}

func TestNodeZeroValue(t *testing.T) {
	// 零值 Node 的 Generation 和 Epoch 应为 0。
	var n Node
	if n.Generation != 0 {
		t.Errorf("零值 Generation = %d, 期望 0", n.Generation)
	}
	if n.Epoch != 0 {
		t.Errorf("零值 Epoch = %d, 期望 0", n.Epoch)
	}
}

func TestLeaseFields(t *testing.T) {
	// 验证 Lease 结构体字段赋值。
	l := Lease{IP: "100.64.0.5", NodeID: "n1"}
	if l.IP != "100.64.0.5" || l.NodeID != "n1" {
		t.Errorf("IP=%q NodeID=%q", l.IP, l.NodeID)
	}
}

func TestEnrollKeyConsumedAndRevoked(t *testing.T) {
	// 验证 EnrollKey 的 Consumed 与 Revoked 彻底分离。
	tests := []struct {
		name     string
		consumed bool
		revoked  bool
	}{
		{"均为 false", false, false},
		{"仅 consumed", true, false},
		{"仅 revoked", false, true},
		{"均为 true", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ek := EnrollKey{
				Key:      "k-test",
				Consumed: tt.consumed,
				Revoked:  tt.revoked,
			}
			if ek.Consumed != tt.consumed {
				t.Errorf("Consumed = %v, 期望 %v", ek.Consumed, tt.consumed)
			}
			if ek.Revoked != tt.revoked {
				t.Errorf("Revoked = %v, 期望 %v", ek.Revoked, tt.revoked)
			}
		})
	}
}

func TestEnrollKeyExpiresAtNilable(t *testing.T) {
	// 验证 ExpiresAt 可为 nil（永不过期）或有值。
	ek := EnrollKey{Key: "k-no-exp"}
	if ek.ExpiresAt != nil {
		t.Error("未设置时 ExpiresAt 应为 nil")
	}
	exp := time.Now().Add(time.Hour)
	ek.ExpiresAt = &exp
	if ek.ExpiresAt == nil || !ek.ExpiresAt.Equal(exp) {
		t.Error("设置后 ExpiresAt 应匹配")
	}
}

func TestCertFingerprintField(t *testing.T) {
	// 验证 Cert 的 Fingerprint 字段（mesh 邻居信任锚）。
	c := Cert{
		Serial:      "12345",
		NodeID:      "n1",
		Fingerprint: "sha256:aabbccdd",
	}
	if c.Fingerprint != "sha256:aabbccdd" {
		t.Errorf("Fingerprint = %q", c.Fingerprint)
	}
}

func TestCertRevokedAtNilable(t *testing.T) {
	// RevokedAt 为 nil 表示未吊销。
	c := Cert{Serial: "999", Revoked: false}
	if c.RevokedAt != nil {
		t.Error("未吊销时 RevokedAt 应为 nil")
	}
	now := time.Now()
	c.Revoked = true
	c.RevokedAt = &now
	if c.RevokedAt == nil {
		t.Error("吊销后 RevokedAt 不应为 nil")
	}
}

func TestRelayLinkFields(t *testing.T) {
	// 验证 RelayLink 字段赋值。
	rl := RelayLink{RelayID: "r1", NeighborID: "r2"}
	if rl.RelayID != "r1" || rl.NeighborID != "r2" {
		t.Errorf("RelayID=%q NeighborID=%q", rl.RelayID, rl.NeighborID)
	}
}

func TestACLPolicyFields(t *testing.T) {
	// 验证 ACLPolicy 字段。
	p := ACLPolicy{
		Version:  5,
		Document: `{"acls":[]}`,
		Author:   "admin",
	}
	if p.Version != 5 || p.Author != "admin" {
		t.Errorf("Version=%d Author=%q", p.Version, p.Author)
	}
	if p.Document != `{"acls":[]}` {
		t.Errorf("Document = %q", p.Document)
	}
}

func TestCARootFields(t *testing.T) {
	// 验证 CARoot 字段（PEM + 加密私钥 PEM）。
	ca := CARoot{
		CertPEM:   []byte("---CA CERT---"),
		EncKeyPEM: []byte("---ENC KEY---"),
	}
	if string(ca.CertPEM) != "---CA CERT---" {
		t.Errorf("CertPEM = %q", ca.CertPEM)
	}
	if string(ca.EncKeyPEM) != "---ENC KEY---" {
		t.Errorf("EncKeyPEM = %q", ca.EncKeyPEM)
	}
}

func TestRelayInfoFields(t *testing.T) {
	// 验证 RelayInfo 端点字段。
	ri := RelayInfo{
		NodeID:         "r1",
		TunnelEndpoint: "r1.example.com:443",
		UDPEndpoint:    "r1.example.com:3478",
		Protocols:      "TLS_RAW,WEBSOCKET",
		Priority:       10,
	}
	if ri.NodeID != "r1" || ri.Priority != 10 {
		t.Errorf("NodeID=%q Priority=%d", ri.NodeID, ri.Priority)
	}
	if ri.TunnelEndpoint != "r1.example.com:443" {
		t.Errorf("TunnelEndpoint = %q", ri.TunnelEndpoint)
	}
	if ri.Protocols != "TLS_RAW,WEBSOCKET" {
		t.Errorf("Protocols = %q", ri.Protocols)
	}
}

func TestQualityEdgeCompositeKey(t *testing.T) {
	// 验证 QualityEdge 复合主键字段。
	qe := QualityEdge{
		SrcNode:      "n1",
		DstNode:      "n2",
		IngressID:    "ingress-0",
		RTTms:        15,
		LossPermille: 5,
	}
	if qe.SrcNode != "n1" || qe.DstNode != "n2" || qe.IngressID != "ingress-0" {
		t.Errorf("复合键: src=%q dst=%q ingress=%q", qe.SrcNode, qe.DstNode, qe.IngressID)
	}
	if qe.RTTms != 15 || qe.LossPermille != 5 {
		t.Errorf("RTTms=%d LossPermille=%d", qe.RTTms, qe.LossPermille)
	}
}

func TestTopoResultFields(t *testing.T) {
	// 验证 TopoResult 字段。
	tr := TopoResult{Version: 42, BlobJSON: []byte(`{"routes":[]}`)}
	if tr.Version != 42 {
		t.Errorf("Version = %d", tr.Version)
	}
	if string(tr.BlobJSON) != `{"routes":[]}` {
		t.Errorf("BlobJSON = %q", tr.BlobJSON)
	}
}

func TestIngressRowFields(t *testing.T) {
	// 验证 IngressRow 字段。
	ir := IngressRow{NodeID: "n1", BlobJSON: []byte(`["ingress-1"]`)}
	if ir.NodeID != "n1" {
		t.Errorf("NodeID = %q", ir.NodeID)
	}
}

func TestModelGORMPersistence(t *testing.T) {
	// 验证所有模型可通过 Migrate 写入内存 SQLite 并读取。
	st := newMemStore(t)

	// Node CRUD
	n := &Node{ID: "model-n1", Role: "node", WGPubKey: "mpk1", VirtualIP: "100.64.99.1/32", User: "bob"}
	if err := st.CreateNode(n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	got, err := st.GetNode("model-n1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.User != "bob" || got.Role != "node" {
		t.Errorf("Node 字段不匹配: %+v", got)
	}

	// EnrollKey CRUD
	ek := &EnrollKey{Key: "model-k1", Tag: "test", Reusable: false}
	if err := st.CreateEnrollKey(ek); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}
	gotKey, err := st.GetEnrollKey("model-k1")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if gotKey.Tag != "test" || gotKey.Reusable {
		t.Errorf("EnrollKey 字段不匹配: %+v", gotKey)
	}
}
