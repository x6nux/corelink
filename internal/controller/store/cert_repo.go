package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

func (s *Store) RecordCert(c *Cert) error {
	return s.db.Create(c).Error
}

func (s *Store) RevokeCert(serial string) error {
	now := time.Now()
	return s.db.Model(&Cert{}).Where("serial = ?", serial).
		Updates(map[string]any{"revoked": true, "revoked_at": now}).Error
}

// DeleteCert 物理删除证书记录（用于补偿：注册失败时清理刚签发但未交付的孤儿证书）。
// 与 RevokeCert 的区别：DeleteCert 不让序列号进入 CRL —— 该证书从未发给任何节点，
// 无需吊销，直接删除最干净。删除不存在的序列号视为幂等成功。
func (s *Store) DeleteCert(serial string) error {
	return s.db.Where("serial = ?", serial).Delete(&Cert{}).Error
}

// CountCerts 返回当前证书记录总数（含已吊销），供 enroll 失败补偿测试断言无孤儿证书。
func (s *Store) CountCerts() (int64, error) {
	var n int64
	err := s.db.Model(&Cert{}).Count(&n).Error
	return n, err
}

// RevokedSerials 返回所有已吊销证书序列号（供生成 CRL，§5.2）。
func (s *Store) RevokedSerials() ([]string, error) {
	var serials []string
	err := s.db.Model(&Cert{}).Where("revoked = ?", true).Pluck("serial", &serials).Error
	return serials, err
}

// IsCertRevoked 按单序列号查询吊销状态（供 Renew 吊销校验与 CRL 拦截器复用）。
// 不存在的序列号视为未吊销（false, nil）。
func (s *Store) IsCertRevoked(serial string) (bool, error) {
	var count int64
	err := s.db.Model(&Cert{}).Where("serial = ? AND revoked = ?", serial, true).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListActiveCertFingerprints 返回所有活跃（未吊销且指纹非空）证书的 nodeID → SHA-256 指纹映射。
// 每个 nodeID 仅保留最新签发的证书指纹（按 created_at DESC 取首条）。
func (s *Store) ListActiveCertFingerprints() (map[string]string, error) {
	var certs []Cert
	err := s.db.Where("revoked = ? AND fingerprint != ?", false, "").
		Order("created_at DESC").Find(&certs).Error
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(certs))
	for _, c := range certs {
		if _, exists := m[c.NodeID]; !exists {
			m[c.NodeID] = c.Fingerprint
		}
	}
	return m, nil
}

// GetCertFingerprint 返回 nodeID 最新未吊销证书的指纹。
// 若节点不存在或无有效证书，返回 ("", false, nil)；指纹为空串时同样返回 ok=false。
func (s *Store) GetCertFingerprint(nodeID string) (string, bool, error) {
	var c Cert
	err := s.db.Where("node_id = ? AND revoked = ?", nodeID, false).
		Order("created_at DESC").First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return c.Fingerprint, c.Fingerprint != "", nil
}
