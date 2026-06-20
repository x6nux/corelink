package store

import (
	"errors"

	"gorm.io/gorm"
)

// 管理面所需的列举/删除仓储方法（S6）。

// GetAdminCredential 读取管理员凭据。不存在返回 nil, nil。
func (s *Store) GetAdminCredential(username string) (*AdminCredential, error) {
	var cred AdminCredential
	err := s.db.Where("username = ?", username).First(&cred).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &cred, nil
}

// UpsertAdminCredential 创建或更新管理员凭据（密码哈希）。
func (s *Store) UpsertAdminCredential(cred *AdminCredential) error {
	return s.db.Save(cred).Error
}

// GetSystemSecret 读取系统密钥。不存在返回 nil, nil。
func (s *Store) GetSystemSecret(key string) ([]byte, error) {
	var sec SystemSecret
	err := s.db.Where("`key` = ?", key).First(&sec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return sec.Value, nil
}

// SetSystemSecret 写入系统密钥（创建或覆盖）。
func (s *Store) SetSystemSecret(key string, value []byte) error {
	return s.db.Save(&SystemSecret{Key: key, Value: value}).Error
}

// ListEnrollKeys 返回所有注册密钥（含已吊销，供管理面展示）。
func (s *Store) ListEnrollKeys() ([]EnrollKey, error) {
	var keys []EnrollKey
	err := s.db.Order("created_at desc").Find(&keys).Error
	return keys, err
}

// ListACLPolicies 返回所有 ACL 策略版本（按版本降序，最新在前）。
func (s *Store) ListACLPolicies() ([]ACLPolicy, error) {
	var pols []ACLPolicy
	err := s.db.Order("version desc").Find(&pols).Error
	return pols, err
}

// ListCerts 返回所有已签发证书元数据（含吊销状态）。
func (s *Store) ListCerts() ([]Cert, error) {
	var certs []Cert
	err := s.db.Order("created_at desc").Find(&certs).Error
	return certs, err
}

// ListCertsByNode 返回指定节点的所有证书（用于删除节点时吊销其全部证书）。
func (s *Store) ListCertsByNode(nodeID string) ([]Cert, error) {
	var certs []Cert
	err := s.db.Where("node_id = ?", nodeID).Find(&certs).Error
	return certs, err
}

// GetLeasesByNode 返回指定节点持有的所有租约（用于删除节点时回收 IP）。
func (s *Store) GetLeasesByNode(nodeID string) ([]Lease, error) {
	var leases []Lease
	err := s.db.Where("node_id = ?", nodeID).Find(&leases).Error
	return leases, err
}

// DeleteNode 删除节点记录（不存在时不报错，幂等）。
// 注意：证书吊销 / IP 回收由上层管理逻辑显式编排，本方法仅删 Node 行。
func (s *Store) DeleteNode(id string) error {
	return s.db.Delete(&Node{}, "id = ?", id).Error
}
