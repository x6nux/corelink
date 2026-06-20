package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// TLSMode 外层 TLS 信任模式（§8.1）。
type TLSMode string

const (
	TLSModeACME   TLSMode = "acme"   // 公网可信，标准链校验
	TLSModePinned TLSMode = "pinned" // CA SPKI 哈希钉扎
)

// TLSOptions 外层 TLS 参数。
type TLSOptions struct {
	Mode         TLSMode
	ServerName   string   // SNI / acme 模式校验用
	PinnedCAHash string   // Mode=pinned 时必填（client 侧）：CA SPKI 哈希 "sha256:<hex>"
	ACMEDomains  []string // Mode=acme 且 server 侧
	ACMECacheDir string
}

// GenerateSelfSigned 生成一张自签 ECDSA 证书（server 侧 pinned 模式用）。
// 若 host 是合法 IP 地址，自动加入 IPAddresses SAN；否则加入 DNSNames SAN。
func GenerateSelfSigned(host string) (tls.Certificate, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	// 根据 host 类型选择合适的 SAN 字段（Go 1.15+ 要求 SAN 不能仅依赖 CN）
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf, nil
}

// caPinnedVerifier 返回 VerifyPeerCertificate 回调（§A7）：遍历对端出示的链 rawCerts，
// 逐张计算 SPKI 哈希，定位命中 wantHash 的那张 CA；以其为唯一 root 构 CertPool，
// 对 leaf（rawCerts[0]）调 x509.Verify（KeyUsages: ExtKeyUsageAny）。
// 链中无 SPKI 命中、或验签失败 → 拒绝。不校验 hostname/SAN（方案 A）。
func caPinnedVerifier(wantHash string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("tunnel: 对端未提供证书")
		}
		// 解析链中所有证书。
		certs := make([]*x509.Certificate, 0, len(rawCerts))
		for i, der := range rawCerts {
			c, err := x509.ParseCertificate(der)
			if err != nil {
				return fmt.Errorf("tunnel: 解析链中第 %d 张证书失败: %w", i, err)
			}
			certs = append(certs, c)
		}
		// 在链中按 SPKI 哈希定位命中 wantHash 的 CA。
		var pinnedCA *x509.Certificate
		for _, c := range certs {
			if CASPKIHash(c) == wantHash {
				pinnedCA = c
				break
			}
		}
		if pinnedCA == nil {
			return fmt.Errorf("tunnel: 出示的证书链中无 SPKI 哈希命中 %s 的 CA——拒绝连接", wantHash)
		}
		// 以命中的 CA 为唯一 root，校验 leaf。
		roots := x509.NewCertPool()
		roots.AddCert(pinnedCA)
		leaf := certs[0]
		opts := x509.VerifyOptions{
			Roots:     roots,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		}
		if _, err := leaf.Verify(opts); err != nil {
			return fmt.Errorf("tunnel: 钉扎 CA 验链失败: %w", err)
		}
		return nil
	}
}

// CAPinnedVerifier 是 caPinnedVerifier 的导出包装，供 mTLS 客户端（cmd 层）
// 直接挂到 tls.Config.VerifyPeerCertificate：遍历对端链按 SPKI 定位 wantHash
// 对应的 CA，以其为唯一 root 验 leaf，验签成功即信任（不校验 hostname）。
func CAPinnedVerifier(wantHash string) func([][]byte, [][]*x509.Certificate) error {
	return caPinnedVerifier(wantHash)
}

// ClientTLSConfig 按模式构造客户端 tls.Config。
func ClientTLSConfig(o *TLSOptions) (*tls.Config, error) {
	switch o.Mode {
	case TLSModeACME:
		return &tls.Config{ServerName: o.ServerName, MinVersion: tls.VersionTLS12}, nil
	case TLSModePinned:
		if o.PinnedCAHash == "" {
			return nil, errors.New("tunnel: pinned 模式必须提供 PinnedCAHash")
		}
		// fail-fast：非法 CA 哈希立即返回，不拖到握手期才在 verifier 内暴露。
		want, err := ParseCAHash(o.PinnedCAHash)
		if err != nil {
			return nil, fmt.Errorf("tunnel: PinnedCAHash 非法: %w", err)
		}
		return &tls.Config{
			ServerName:            o.ServerName,
			MinVersion:            tls.VersionTLS12,
			InsecureSkipVerify:    true, // 关闭默认链校验（含 hostname），改用 CA 钉扎
			VerifyPeerCertificate: caPinnedVerifier(want),
		}, nil
	default:
		return nil, fmt.Errorf("tunnel: 未知 TLS 模式 %q", o.Mode)
	}
}

// ServerTLSConfig 按模式构造服务端 tls.Config（§8.1）。
//   - Mode=acme：通过 autocert.Manager 自动申请/续签 Let's Encrypt 证书，
//     返回 Manager.TLSConfig()（含 GetCertificate 钩子）。
//   - Mode=pinned：生成自签 ECDSA 证书并注入，供 tlsListener/wss 服务端复用。
func ServerTLSConfig(o *TLSOptions) (*tls.Config, error) {
	switch o.Mode {
	case TLSModeACME:
		cacheDir := o.ACMECacheDir
		if cacheDir == "" {
			// 优先使用跨平台的用户缓存目录（Linux: ~/.cache, macOS: ~/Library/Caches,
			// Windows: %LocalAppData%），硬编码 /var/cache 仅作最终兜底。
			if d, err := os.UserCacheDir(); err == nil {
				cacheDir = d + "/corelink/acme"
			} else {
				cacheDir = "/var/cache/corelink/acme"
			}
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(o.ACMEDomains...),
			Cache:      autocert.DirCache(cacheDir),
		}
		return m.TLSConfig(), nil
	case TLSModePinned:
		name := o.ServerName
		if name == "" {
			name = "127.0.0.1"
		}
		srvCert, _, err := GenerateSelfSigned(name)
		if err != nil {
			return nil, fmt.Errorf("tunnel: ServerTLSConfig(pinned) 生成自签证书失败: %w", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{srvCert},
			MinVersion:   tls.VersionTLS12,
		}, nil
	default:
		return nil, fmt.Errorf("tunnel: 未知 TLS 模式 %q", o.Mode)
	}
}
