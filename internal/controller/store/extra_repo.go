package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// ---------- Lease ----------

// ListLeases 返回所有有效租约（供 IPAM 重建位图）。
func (s *Store) ListLeases() ([]Lease, error) {
	var leases []Lease
	err := s.db.Find(&leases).Error
	return leases, err
}

// ---------- Node ----------

// ListNodes 返回所有节点（供 ACL 快照枚举）。
func (s *Store) ListNodes() ([]Node, error) {
	var nodes []Node
	err := s.db.Find(&nodes).Error
	return nodes, err
}

// ---------- EnrollKey ----------

// GetEnrollKey 按 key 查找注册密钥；不存在时返回 ErrNotFound。
func (s *Store) GetEnrollKey(key string) (*EnrollKey, error) {
	var ek EnrollKey
	err := s.db.First(&ek, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ek, nil
}

// CreateEnrollKey 创建注册密钥。
func (s *Store) CreateEnrollKey(ek *EnrollKey) error {
	return s.db.Create(ek).Error
}

// RevokeEnrollKey 吊销注册密钥（一次性 key 用后调用）。
func (s *Store) RevokeEnrollKey(key string) error {
	return s.db.Model(&EnrollKey{}).Where("key = ?", key).
		UpdateColumn("revoked", true).Error
}

// ConsumeOneTimeKey 原子抢占一次性 key：UPDATE ... WHERE key=? AND revoked=false AND consumed=false SET consumed=true。
// 返回 (true, nil) 表示本次调用成功抢占（恰好更新 1 行），其余调用返回 (false, nil)。
// 用于消除一次性 enrollment key 的 TOCTOU 并发重放：检查与置位在单条 UPDATE 中原子完成。
// 守卫 revoked=false 确保管理员吊销的 key 无法被抢占；守卫 consumed=false 防并发/二次消费。
// 仅操作 consumed 位，绝不触碰 revoked（吊销专属管理员语义）。
func (s *Store) ConsumeOneTimeKey(key string) (bool, error) {
	res := s.db.Model(&EnrollKey{}).
		Where("key = ? AND revoked = ? AND consumed = ?", key, false, false).
		UpdateColumn("consumed", true)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// UnconsumeOneTimeKey 补偿性复位一次性 key 的 consumed 状态（撤销本次抢占的烧毁）。
// 仅在 enroll 关键路径于 ConsumeOneTimeKey 成功之后、后续步骤（分配 IP/签证书/建节点）
// 失败时调用，使一次性 key 不被永久烧毁（bug #17）。
// 守卫 reusable=false 避免误改可重用 key；关键守卫 revoked=false：若补偿前管理员已吊销
// （revoked=true），该 UPDATE 不匹配任何行、不复位，吊销得以保持（修复 TOCTOU 复活吊销）。
// 仅复位 consumed 位，绝不触碰 revoked。
func (s *Store) UnconsumeOneTimeKey(key string) error {
	return s.db.Model(&EnrollKey{}).
		Where("key = ? AND reusable = ? AND revoked = ?", key, false, false).
		UpdateColumn("consumed", false).Error
}

// ---------- ACLPolicy ----------

// SaveACLPolicy 保存一条策略版本，返回含自增 Version 的记录。
func (s *Store) SaveACLPolicy(doc, author string) (*ACLPolicy, error) {
	p := &ACLPolicy{
		Document:  doc,
		Author:    author,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(p).Error; err != nil {
		return nil, err
	}
	return p, nil
}

// GetLatestACLPolicy 返回最新版本策略；无记录时返回空策略（Version=0），不报错。
func (s *Store) GetLatestACLPolicy() (*ACLPolicy, error) {
	var p ACLPolicy
	err := s.db.Order("version desc").First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &ACLPolicy{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ---------- CARoot ----------

// SaveCARoot 持久化 CA 根（覆盖已有记录）。
func (s *Store) SaveCARoot(certPEM, encKeyPEM []byte) error {
	// 仅保留一条记录：先删后插（SQLite/Postgres 均兼容）。
	if err := s.db.Where("1 = 1").Delete(&CARoot{}).Error; err != nil {
		return err
	}
	return s.db.Create(&CARoot{CertPEM: certPEM, EncKeyPEM: encKeyPEM}).Error
}

// GetCARoot 返回唯一 CA 根记录；不存在时返回 ErrNotFound。
func (s *Store) GetCARoot() (*CARoot, error) {
	var r CARoot
	err := s.db.First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ---------- RelayInfo ----------

// UpsertRelayInfo 插入或更新 relay 端点信息。
func (s *Store) UpsertRelayInfo(info *RelayInfo) error {
	return s.db.Save(info).Error
}

// ListRelayInfo 返回所有 relay 端点信息。
func (s *Store) ListRelayInfo() ([]RelayInfo, error) {
	var infos []RelayInfo
	err := s.db.Find(&infos).Error
	return infos, err
}

// ---------- RelayLink ----------

// SetRelayLinks 以全量替换方式设置节点 relayID 的邻接关系。
// neighborIDs 为空时清空邻接。
func (s *Store) SetRelayLinks(relayID string, neighborIDs []string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 删除该 relay 的所有旧链接。
		if err := tx.Where("relay_id = ?", relayID).Delete(&RelayLink{}).Error; err != nil {
			return err
		}
		for _, nid := range neighborIDs {
			if err := tx.Create(&RelayLink{RelayID: relayID, NeighborID: nid}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListRelayLinks 返回所有邻接关系。
func (s *Store) ListRelayLinks() ([]RelayLink, error) {
	var links []RelayLink
	err := s.db.Find(&links).Error
	return links, err
}
