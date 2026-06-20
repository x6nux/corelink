package pki

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// Marshal 把 CA 证书与私钥导出为 PEM 字节。
func (c *CA) Marshal() (certPEM, keyPEM []byte, err error) {
	keyDER, err := x509.MarshalECPrivateKey(c.key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// LoadCA 从 PEM 字节恢复 CA。
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, fmt.Errorf("pki: 证书 PEM 解析失败")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("pki: 私钥 PEM 解析失败")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key}, nil
}
