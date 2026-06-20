package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	agentconfig "github.com/x6nux/corelink/internal/nodecore/config"
	"github.com/x6nux/corelink/internal/nodecore/keystore"
	"github.com/x6nux/corelink/internal/pki"
	"github.com/x6nux/corelink/pkg/tunnel"
)

func TestBuildMTLSFromIdentity_PinsCA(t *testing.T) {
	ca, err := pki.NewCA("NodeMTLSTestCA")
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert().Raw})

	// 节点自身证书（CA 签的 client 证书）。
	nkey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ncsr, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "node-1"}}, nkey)
	ncertDER, err := ca.IssueFromCSR(ncsr, "node-1", pki.NodeRoleNode, time.Hour)
	if err != nil {
		t.Fatalf("IssueFromCSR: %v", err)
	}
	ncertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ncertDER})
	nkeyDER, _ := x509.MarshalECPrivateKey(nkey)
	nkeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: nkeyDER})

	id := &keystore.Identity{
		NodeCertPEM: ncertPEM,
		NodeKeyPEM:  nkeyPEM,
		CACertPEM:   caPEM,
		VirtualIP:   "10.0.0.1/32",
		NodeID:      "node-1",
	}
	// 信任锚来自 token 下发的 ca_hash（cfg.ControllerCAHash），node 不依赖本地 CA 证书：
	// 完全用 controller 握手出示的完整证书链 + ca_hash 验证。
	cfg := &agentconfig.Config{
		ControllerMTLSAddr: "ctrl:7444",
		ControllerCAHash:   tunnel.CASPKIHash(ca.Cert()),
	}

	tlsCfg, err := buildMTLSFromIdentity(id, cfg)
	if err != nil {
		t.Fatalf("buildMTLSFromIdentity: %v", err)
	}
	if tlsCfg.VerifyPeerCertificate == nil {
		t.Fatal("应挂 VerifyPeerCertificate（CA 哈希钉扎）而非无脑信任")
	}

	// server 出示同 CA 签的链应通过。
	skey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	scsr, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "ctrl"}}, skey)
	scertDER, _ := ca.IssueFromCSR(scsr, "ctrl", pki.NodeRoleNode, time.Hour, pki.WithServerAuth())
	if err := tlsCfg.VerifyPeerCertificate([][]byte{scertDER, ca.Cert().Raw}, nil); err != nil {
		t.Errorf("同 CA 链应通过: %v", err)
	}

	// 无关 CA 的链应被拒。
	other, _ := pki.NewCA("Other")
	ocsr, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "evil"}}, skey)
	ocertDER, _ := other.IssueFromCSR(ocsr, "evil", pki.NodeRoleNode, time.Hour, pki.WithServerAuth())
	if err := tlsCfg.VerifyPeerCertificate([][]byte{ocertDER, other.Cert().Raw}, nil); err == nil {
		t.Error("无关 CA 链应被拒绝")
	}
}
