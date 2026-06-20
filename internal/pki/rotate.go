package pki

import "time"

// Renew 用新 CSR 为同一身份续签（新序列号、新有效期）。
// 默认沿用 IssueFromCSR 的安全默认（client 证书：仅 ClientAuth、不复制 SAN）；
// 可通过 opts 传 WithServerAuth() 续签 server 证书。
// 旧证书的失效由调用方（controller）决定：等到期或显式吊销（见 crl.go）。
func (c *CA) Renew(csrDER []byte, nodeID string, role NodeRole, ttl time.Duration, opts ...IssueOption) ([]byte, error) {
	return c.IssueFromCSR(csrDER, nodeID, role, ttl, opts...)
}
