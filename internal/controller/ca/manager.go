// Package ca 管理 CoreLink controller 内层 CA 的生命周期（§5.2）。
package ca

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/pki"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// ErrDecryptFailed 私钥解密失败（密钥不匹配）。
var ErrDecryptFailed = errors.New("ca: 解密 CA 私钥失败")

// Manager 封装 CA 实例及其依赖的 store。
type Manager struct {
	ca  *pki.CA
	st  *store.Store
	enc []byte // 32字节 AES-256-GCM 密钥，用于加密/解密存储中的私钥

	// Notify 证书签发/吊销后触发全网配置刷新；接口类型避免 import configsvc。
	Notify interface{ RecomputeAndNotify(nodeIDs ...string) }
}

// Cert 返回 CA 证书（用于构建验证池）。
func (m *Manager) Cert() *x509.Certificate {
	return m.ca.Cert()
}

// EnsureCA 从 store 加载或首次生成 CA（幂等）。
//
//   - 无记录：NewCA → Marshal → AES-GCM 加密私钥 → SaveCARoot
//   - 有记录：GetCARoot → 解密 → LoadCA
func EnsureCA(st *store.Store, subject string, encKey []byte) (*Manager, error) {
	if len(encKey) != 32 {
		return nil, fmt.Errorf("ca: encKey 必须为 32 字节，实际 %d", len(encKey))
	}

	root, err := st.GetCARoot()
	if errors.Is(err, store.ErrNotFound) {
		// 首次：生成新 CA
		ca, err := pki.NewCA(subject)
		if err != nil {
			return nil, fmt.Errorf("ca: 生成 CA 失败: %w", err)
		}
		certPEM, keyPEM, err := ca.Marshal()
		if err != nil {
			return nil, fmt.Errorf("ca: Marshal 失败: %w", err)
		}
		encKeyPEM, err := aesGCMEncrypt(encKey, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("ca: 加密私钥失败: %w", err)
		}
		if err := st.SaveCARoot(certPEM, encKeyPEM); err != nil {
			return nil, fmt.Errorf("ca: SaveCARoot 失败: %w", err)
		}
		return &Manager{ca: ca, st: st, enc: encKey}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ca: GetCARoot 失败: %w", err)
	}

	// 已有记录：解密并加载
	keyPEM, err := aesGCMDecrypt(encKey, root.EncKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptFailed, err)
	}
	ca, err := pki.LoadCA(root.CertPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("ca: LoadCA 失败: %w", err)
	}
	return &Manager{ca: ca, st: st, enc: encKey}, nil
}

// Issue 签发 client 节点证书（仅 ClientAuth、不复制 CSR SAN）并记录序列号到 store。
// 用于 enroll/Renew 等来自不受信任节点的 CSR 路径。
func (m *Manager) Issue(csrDER []byte, nodeID string, role string, ttl time.Duration) ([]byte, error) {
	return m.issue(csrDER, nodeID, role, ttl)
}

// IssueServer 签发 server 证书（附加 ServerAuth、复制 CSR SAN）并记录序列号到 store。
// 仅供 controller 自身控制 CSR 的服务端证书使用（如 controller server 证书）。
func (m *Manager) IssueServer(csrDER []byte, nodeID string, role string, ttl time.Duration) ([]byte, error) {
	return m.issue(csrDER, nodeID, role, ttl, pki.WithServerAuth())
}

// issue 是 Issue/IssueServer 的内部实现，opts 透传给底层 CA。
func (m *Manager) issue(csrDER []byte, nodeID string, role string, ttl time.Duration, opts ...pki.IssueOption) ([]byte, error) {
	certDER, err := m.ca.IssueFromCSR(csrDER, nodeID, pki.NodeRole(role), ttl, opts...)
	if err != nil {
		return nil, fmt.Errorf("ca: 签发证书失败: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("ca: 解析已签发证书失败: %w", err)
	}
	serial := cert.SerialNumber.Text(10)
	// 与 mesh verifyPeer 同源同格式，确保 pin 匹配。
	fp := tunnel.CertFingerprint(cert)
	if err := m.st.RecordCert(&store.Cert{
		Serial:      serial,
		NodeID:      nodeID,
		NotAfter:    cert.NotAfter,
		Fingerprint: fp,
	}); err != nil {
		return nil, fmt.Errorf("ca: 记录证书失败: %w", err)
	}
	go m.notifyAll()
	return certDER, nil
}

// Renew 用新 CSR 续签节点证书，旧序列号吊销。
func (m *Manager) Renew(oldSerial string, csrDER []byte, nodeID string, role string, ttl time.Duration) ([]byte, error) {
	certDER, err := m.Issue(csrDER, nodeID, role, ttl)
	if err != nil {
		return nil, err
	}
	// 吊销旧证书（忽略不存在的序列号）
	if oldSerial != "" {
		if err := m.st.RevokeCert(oldSerial); err != nil {
			return nil, fmt.Errorf("ca: 吊销旧证书失败: %w", err)
		}
	}
	return certDER, nil
}

// Revoke 吊销指定序列号的证书。
func (m *Manager) Revoke(serial string) error {
	if err := m.st.RevokeCert(serial); err != nil {
		return err
	}
	go m.notifyAll()
	return nil
}

// notifyAll 通知全网节点刷新配置（指纹变更）。
func (m *Manager) notifyAll() {
	if m.Notify == nil {
		return
	}
	nodes, err := m.st.ListNodes()
	if err != nil {
		return
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	if len(ids) > 0 {
		m.Notify.RecomputeAndNotify(ids...)
	}
}

// CurrentCRL 生成当前 CRL（含所有已吊销序列号）。
// store.RevokedSerials() 返回 []string，转为 []*big.Int，非法字符串跳过。
func (m *Manager) CurrentCRL(validFor time.Duration) ([]byte, error) {
	serials, err := m.st.RevokedSerials()
	if err != nil {
		return nil, fmt.Errorf("ca: 读取吊销序列号失败: %w", err)
	}
	bigSerials := make([]*big.Int, 0, len(serials))
	for _, s := range serials {
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			continue // 非法序列号字符串，跳过
		}
		bigSerials = append(bigSerials, n)
	}
	return m.ca.BuildCRL(bigSerials, validFor)
}

// ---------- AES-256-GCM 辅助函数 ----------

// aesGCMEncrypt 使用 key（32字节）AES-256-GCM 加密 plaintext，
// 输出格式：nonce(12字节) || ciphertext。
func aesGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// aesGCMDecrypt 解密 aesGCMEncrypt 产生的字节（nonce||ciphertext）。
func aesGCMDecrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("数据长度 %d < nonce 长度 %d", len(data), nonceSize)
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
