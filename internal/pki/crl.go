package pki

import (
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"time"
)

// BuildCRL 用 CA 私钥签发一张 CRL，包含给定被吊销序列号。
func (c *CA) BuildCRL(revokedSerials []*big.Int, validFor time.Duration) ([]byte, error) {
	now := time.Now()
	entries := make([]x509.RevocationListEntry, 0, len(revokedSerials))
	for _, s := range revokedSerials {
		entries = append(entries, x509.RevocationListEntry{
			SerialNumber:   s,
			RevocationTime: now,
		})
	}
	tmpl := &x509.RevocationList{
		Number:                    big.NewInt(now.UnixNano()),
		ThisUpdate:                now,
		NextUpdate:                now.Add(validFor),
		RevokedCertificateEntries: entries,
	}
	return x509.CreateRevocationList(rand.Reader, tmpl, c.cert, c.key)
}

// IsRevoked 解析 CRL 并判断某序列号是否被吊销。
func IsRevoked(crlDER []byte, serial *big.Int) (bool, error) {
	crl, err := x509.ParseRevocationList(crlDER)
	if err != nil {
		return false, err
	}
	for _, e := range crl.RevokedCertificateEntries {
		if e.SerialNumber.Cmp(serial) == 0 {
			return true, nil
		}
	}
	return false, nil
}
