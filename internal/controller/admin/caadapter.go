package admin

import (
	"encoding/pem"
	"errors"

	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// CAAdapter 把 *ca.Manager 适配为 CAIface，并提供 CA 公钥(SPKI)哈希作为信任锚。
type CAAdapter struct {
	mgr *ca.Manager
}

// NewCAAdapter 构造 CA 适配器。
func NewCAAdapter(mgr *ca.Manager) *CAAdapter {
	return &CAAdapter{mgr: mgr}
}

// Revoke 吊销指定序列号证书。
func (c *CAAdapter) Revoke(serial string) error {
	return c.mgr.Revoke(serial)
}

// CACertPEM 返回 CA 证书的 PEM 编码。
func (c *CAAdapter) CACertPEM() ([]byte, error) {
	cert := c.mgr.Cert()
	if cert == nil {
		return nil, errors.New("admin: CA 证书不可用")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

// CAPublicKeyHash 返回 CA 证书的 SPKI SHA-256 哈希（"sha256:<hex>"）。
// 它是控制面信任锚：CA 服务端证书可任意轮换，只要 CA 不换密钥此值不变。
func (c *CAAdapter) CAPublicKeyHash() (string, error) {
	cert := c.mgr.Cert()
	if cert == nil {
		return "", errors.New("admin: CA 证书不可用")
	}
	return tunnel.CASPKIHash(cert), nil
}
