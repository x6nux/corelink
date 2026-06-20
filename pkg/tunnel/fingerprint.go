package tunnel

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"
)

// CertFingerprint 返回证书 DER 的 SHA-256 十六进制小写指纹（§8.1）。
func CertFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// CASPKIHash 返回证书的 SubjectPublicKeyInfo 的 SHA-256，形如 "sha256:<hex>"（§A1）。
// 哈希 SPKI 而非整张证书：CA 续期/重编码（公钥不变）时哈希不变，仅换密钥才失效。
func CASPKIHash(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseCAHash 校验并归一化 CA SPKI 哈希：必须为 "sha256:" 前缀 + 解码后 32 字节。
// 归一化后非空。返回原前缀小写形式 "sha256:<hex小写>"。
func ParseCAHash(s string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(s, prefix) {
		return "", fmt.Errorf("tunnel: ca_hash 必须以 %q 开头: %q", prefix, s)
	}
	hexPart := strings.ToLower(strings.TrimPrefix(s, prefix))
	b, err := hex.DecodeString(hexPart)
	if err != nil {
		return "", fmt.Errorf("tunnel: ca_hash 含非 hex 字符 %q: %w", s, err)
	}
	if len(b) != 32 {
		return "", fmt.Errorf("tunnel: ca_hash 解码后须为 32 字节，实际 %d", len(b))
	}
	return prefix + hexPart, nil
}
