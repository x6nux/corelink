package store

import (
	"errors"

	"gorm.io/gorm"
)

// ---------- SplitRuleRow ----------

func (s *Store) CreateSplitRule(r *SplitRuleRow) error {
	return s.db.Create(r).Error
}

func (s *Store) ListSplitRules() ([]SplitRuleRow, error) {
	var out []SplitRuleRow
	err := s.db.Order("sort_order, id").Find(&out).Error
	return out, err
}

func (s *Store) ListSplitRulesByNode(nodeID string) ([]SplitRuleRow, error) {
	var out []SplitRuleRow
	err := s.db.Where("node_id = ? OR node_id = ''", nodeID).Order("sort_order, id").Find(&out).Error
	return out, err
}

func (s *Store) DeleteSplitRule(id uint) error {
	return s.db.Delete(&SplitRuleRow{}, id).Error
}

func (s *Store) SetSplitRuleEnabled(id uint, enabled bool) error {
	return s.db.Model(&SplitRuleRow{}).Where("id = ?", id).Update("enabled", enabled).Error
}

func (s *Store) ReorderSplitRule(id uint, newOrder uint32) error {
	return s.db.Model(&SplitRuleRow{}).Where("id = ?", id).Update("sort_order", newOrder).Error
}

// ---------- GeoIPMeta ----------

func (s *Store) UpsertGeoIPMeta(m *GeoIPMeta) error {
	return s.db.Save(m).Error
}

func (s *Store) GetLatestGeoIPMeta() (*GeoIPMeta, error) {
	var m GeoIPMeta
	err := s.db.Order("updated_at DESC").First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &m, err
}
