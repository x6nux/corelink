package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
)

// GenerateCSR 生成一对 ECDSA 密钥与 CSR（DER），返回 csrDER 与私钥。
// dnsSANs 可选，传入后将包含在 CSR 的 SubjectAltName 扩展中（server 证书使用）。
func GenerateCSR(commonName string, dnsSANs ...string) ([]byte, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: commonName},
		DNSNames: dnsSANs,
	}
	// 若 commonName 看起来是 IP，也加入 IPAddresses SAN
	if ip := net.ParseIP(commonName); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, err
	}
	return der, key, nil
}
