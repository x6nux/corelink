// Package keystore 提供 agent/relay 节点的本地密钥与身份持久化（S3-P1）。
//
// 文件布局（DataDir 下，权限 0600）：
//
//	wg_private.key   — WireGuard X25519 私钥（base64，32 字节）
//	wg_public.key    — WireGuard X25519 公钥（base64，32 字节）
//	node_key.pem     — 节点身份 ECDSA 私钥（PEM PKCS#8）
//	node_cert.pem    — 节点证书（PEM）
//	ca_cert.pem      — CA 证书（PEM）
//	identity.json    — { "node_id", "virtual_ip" }
package keystore

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/curve25519"
)

const (
	fileWGPriv   = "wg_private.key"
	fileWGPub    = "wg_public.key"
	fileNodeKey  = "node_key.pem"
	fileNodeCert = "node_cert.pem"
	fileCACert   = "ca_cert.pem"
	fileIdentity = "identity.json"
	fileMode     = 0600
)

// identityMeta 保存非证书的身份元数据。
type identityMeta struct {
	NodeID    string `json:"node_id"`
	VirtualIP string `json:"virtual_ip"`
}

// KeyStore 持久化 WG 密钥对与节点身份（证书/私钥/CA/IP/nodeID）到本地文件系统。
type KeyStore struct {
	dir string
}

// New 构造 KeyStore，dataDir 必须已存在（由调用方负责创建）。
func New(dataDir string) *KeyStore {
	return &KeyStore{dir: dataDir}
}

// ─────────────────────── WireGuard 密钥 ───────────────────────

// HasWGKey 返回 WG 密钥对是否完整（私钥与公钥都存在）。
func (ks *KeyStore) HasWGKey() bool {
	for _, f := range []string{fileWGPriv, fileWGPub} {
		if _, err := os.Stat(ks.path(f)); err != nil {
			return false
		}
	}
	return true
}

// EnsureWGKey 若 WG 密钥对不完整则生成/补写并持久化，幂等。
//
// 自愈：若私钥已存在但公钥缺失（如上次写完私钥即崩溃），
// 由私钥重算补写公钥而非重新生成密钥对（curve25519 为决定性运算，
// 重算结果与原公钥一致，确保对端已知的公钥不被更换）。
func (ks *KeyStore) EnsureWGKey() error {
	privPath := ks.path(fileWGPriv)
	pubPath := ks.path(fileWGPub)

	_, privErr := os.Stat(privPath)
	_, pubErr := os.Stat(pubPath)
	if privErr == nil && pubErr == nil {
		return nil // 私钥+公钥都在，幂等直接返回
	}

	// 私钥在但公钥缺：由私钥重算补写公钥，不重新生成密钥对
	if privErr == nil && pubErr != nil {
		privB64, err := readFile(privPath)
		if err != nil {
			return fmt.Errorf("keystore: 读取 WG 私钥失败: %w", err)
		}
		privBytes, err := base64.StdEncoding.DecodeString(string(privB64))
		if err != nil {
			return fmt.Errorf("keystore: 解码 WG 私钥失败: %w", err)
		}
		pubBytes, err := curve25519.X25519(privBytes, curve25519.Basepoint)
		if err != nil {
			return fmt.Errorf("keystore: 重算 WG 公钥失败: %w", err)
		}
		return writeFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pubBytes)))
	}

	// 私钥不存在：全新生成密钥对（公钥可能存在的残留会被覆盖）
	// 生成 X25519 私钥（32 字节随机数，clamp 由 curve25519 处理）
	privBytes := make([]byte, 32)
	if _, err := io.ReadFull(randReader, privBytes); err != nil {
		return fmt.Errorf("keystore: 生成 WG 私钥失败: %w", err)
	}
	// clamp（RFC 7748）
	privBytes[0] &= 248
	privBytes[31] &= 127
	privBytes[31] |= 64

	// 计算对应公钥
	pubBytes, err := curve25519.X25519(privBytes, curve25519.Basepoint)
	if err != nil {
		return fmt.Errorf("keystore: 计算 WG 公钥失败: %w", err)
	}

	privB64 := base64.StdEncoding.EncodeToString(privBytes)
	pubB64 := base64.StdEncoding.EncodeToString(pubBytes)

	// 先写私钥再写公钥；若公钥写入崩溃，下次 EnsureWGKey 会经上面的
	// 「私钥在公钥缺」分支自愈，故顺序安全。
	if err := writeFile(privPath, []byte(privB64)); err != nil {
		return err
	}
	return writeFile(pubPath, []byte(pubB64))
}

// WGPublicKey 返回 WG 公钥 base64 字符串；若密钥不存在则返回错误。
func (ks *KeyStore) WGPublicKey() (string, error) {
	data, err := readFile(ks.path(fileWGPub))
	if err != nil {
		return "", fmt.Errorf("keystore: 读取 WG 公钥失败（未调用 EnsureWGKey？）: %w", err)
	}
	return string(data), nil
}

// WGPrivateKey 返回 WG 私钥 base64 字符串；若密钥不存在则返回错误。
func (ks *KeyStore) WGPrivateKey() (string, error) {
	data, err := readFile(ks.path(fileWGPriv))
	if err != nil {
		return "", fmt.Errorf("keystore: 读取 WG 私钥失败（未调用 EnsureWGKey？）: %w", err)
	}
	return string(data), nil
}

// ─────────────────────── 节点身份 ───────────────────────

// HasIdentity 返回节点是否已有身份（证书+私钥+CA+元数据）。
func (ks *KeyStore) HasIdentity() bool {
	for _, f := range []string{fileNodeKey, fileNodeCert, fileCACert, fileIdentity} {
		if _, err := os.Stat(ks.path(f)); err != nil {
			return false
		}
	}
	return true
}

// SaveIdentity 将注册结果写入持久化文件。
//   - nodeCertDER: 节点证书（DER 字节）
//   - nodeKey: 由 pki.GenerateCSR 返回的 ECDSA 私钥
//   - caCertDER: CA 证书（DER 字节）
//   - virtualIP: 分配的虚拟 IP（字符串，如 "10.0.0.1/32"）
//   - nodeID: 节点 ID
func (ks *KeyStore) SaveIdentity(nodeCertDER []byte, nodeKey *ecdsa.PrivateKey, caCertDER []byte, virtualIP, nodeID string) error {
	// 节点证书 PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: nodeCertDER})
	if err := writeFile(ks.path(fileNodeCert), certPEM); err != nil {
		return err
	}
	// 节点私钥 PEM（PKCS#8）
	keyDER, err := x509.MarshalPKCS8PrivateKey(nodeKey)
	if err != nil {
		return fmt.Errorf("keystore: 序列化节点私钥失败: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeFile(ks.path(fileNodeKey), keyPEM); err != nil {
		return err
	}
	// CA 证书 PEM
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	if err := writeFile(ks.path(fileCACert), caPEM); err != nil {
		return err
	}
	// 身份元数据
	meta := identityMeta{NodeID: nodeID, VirtualIP: virtualIP}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("keystore: 序列化身份元数据失败: %w", err)
	}
	return writeFile(ks.path(fileIdentity), metaBytes)
}

// Identity 节点身份数据（LoadIdentity 的返回值）。
type Identity struct {
	NodeCertPEM []byte
	NodeKeyPEM  []byte
	CACertPEM   []byte
	VirtualIP   string
	NodeID      string
}

// LoadIdentity 从磁盘加载节点身份；若身份不存在则返回 ErrNoIdentity。
func (ks *KeyStore) LoadIdentity() (*Identity, error) {
	if !ks.HasIdentity() {
		return nil, ErrNoIdentity
	}
	certPEM, err := readFile(ks.path(fileNodeCert))
	if err != nil {
		return nil, fmt.Errorf("keystore: 读节点证书失败: %w", err)
	}
	keyPEM, err := readFile(ks.path(fileNodeKey))
	if err != nil {
		return nil, fmt.Errorf("keystore: 读节点私钥失败: %w", err)
	}
	caPEM, err := readFile(ks.path(fileCACert))
	if err != nil {
		return nil, fmt.Errorf("keystore: 读 CA 证书失败: %w", err)
	}
	metaBytes, err := readFile(ks.path(fileIdentity))
	if err != nil {
		return nil, fmt.Errorf("keystore: 读身份元数据失败: %w", err)
	}
	var meta identityMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("keystore: 解析身份元数据失败: %w", err)
	}
	return &Identity{
		NodeCertPEM: certPEM,
		NodeKeyPEM:  keyPEM,
		CACertPEM:   caPEM,
		VirtualIP:   meta.VirtualIP,
		NodeID:      meta.NodeID,
	}, nil
}

// NodeCertKeyPair 构造 tls.Certificate，供后续 mTLS 使用。
func (ks *KeyStore) NodeCertKeyPair() (tls.Certificate, error) {
	id, err := ks.LoadIdentity()
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(id.NodeCertPEM, id.NodeKeyPEM)
}

// ─────────────────────── 内部辅助 ───────────────────────

// ErrNoIdentity 节点尚未完成注册。
var ErrNoIdentity = errors.New("keystore: 节点身份不存在（未注册）")

func (ks *KeyStore) path(name string) string {
	return filepath.Join(ks.dir, name)
}

func writeFile(path string, data []byte) error {
	// 先写临时文件再原子重命名，避免部分写损坏。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, fileMode); err != nil {
		return fmt.Errorf("keystore: 写临时文件 %q 失败: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("keystore: 重命名 %q→%q 失败: %w", tmp, path, err)
	}
	return nil
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// randReader 用于生成随机字节，测试时可替换。
var randReader io.Reader = cryptoRandReader{}

type cryptoRandReader struct{}

func (cryptoRandReader) Read(b []byte) (int, error) {
	return randRead(b)
}
