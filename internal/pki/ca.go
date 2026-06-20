// Package pki 提供 CoreLink controller 内层 CA 的密码学原语（§5.2）。
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"
)

// NodeRole 写入证书的角色标识（放入 Subject.OrganizationalUnit）。
type NodeRole string

const NodeRoleNode NodeRole = "node"

// CA 内层证书颁发机构。
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// NewCA 生成一张自签根 CA（ECDSA P-256，10 年）。
func NewCA(commonName string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return &CA{cert: cert, key: key}, nil
}

// Cert 返回 CA 证书（供构建校验池）。
func (c *CA) Cert() *x509.Certificate { return c.cert }

// issueOptions 控制证书签发的安全相关行为。
type issueOptions struct {
	serverAuth bool // 是否赋予 ExtKeyUsageServerAuth 并从 CSR 复制 SAN
}

// IssueOption 调整 IssueFromCSR 的签发行为。
type IssueOption func(*issueOptions)

// WithServerAuth 让签发的证书携带 ServerAuth 用途，并复制 CSR 中的 DNS/IP SAN。
// 仅用于由 controller 自身控制 CSR 的 server 证书（如 controller/relay 服务端证书），
// 切勿用于来自不受信任节点的 enroll CSR——否则任意节点可塞 SAN 换取可冒充端点的证书。
func WithServerAuth() IssueOption {
	return func(o *issueOptions) { o.serverAuth = true }
}

// IssueFromCSR 用 CSR 签发证书，角色写入 OU，有效期 ttl。
//
// 安全默认（无 opts）：签发 client 证书——ExtKeyUsage 仅 ClientAuth，
// 且**不复制** CSR 中的 DNS/IP SAN。这用于 enroll 节点证书，防止不受信任节点
// 通过 CSR SAN 换取可冒充端点的 server 证书。
//
// 传入 WithServerAuth() 时签发 server 证书：附加 ServerAuth 用途并复制 CSR SAN，
// 仅供 controller 自身控制 CSR 的场景使用。
func (c *CA) IssueFromCSR(csrDER []byte, nodeID string, role NodeRole, ttl time.Duration, opts ...IssueOption) ([]byte, error) {
	var o issueOptions
	for _, opt := range opts {
		opt(&o)
	}

	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, fmt.Errorf("pki: 解析 CSR 失败: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: CSR 签名校验失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	extKeyUsage := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if o.serverAuth {
		extKeyUsage = append(extKeyUsage, x509.ExtKeyUsageServerAuth)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         nodeID,
			OrganizationalUnit: []string{string(role)},
		},
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: extKeyUsage,
	}
	// 仅 server 证书才从 CSR 复制 SAN（client 证书无条件忽略 CSR SAN）。
	if o.serverAuth {
		tmpl.DNSNames = csr.DNSNames
		tmpl.IPAddresses = csr.IPAddresses
	}
	return x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
}
