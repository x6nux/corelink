package store

import (
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"
)

// ErrNotFound 统一的"记录不存在"错误。
var ErrNotFound = errors.New("store: 记录不存在")

func (s *Store) CreateNode(n *Node) error {
	return s.db.Create(n).Error
}

func (s *Store) GetNode(id string) (*Node, error) {
	var n Node
	err := s.db.First(&n, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// GetNodeByName 按唯一名称查找节点。
func (s *Store) GetNodeByName(name string) (*Node, error) {
	var n Node
	err := s.db.First(&n, "name = ?", name).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &n, err
}

// ResolveNode 模糊解析节点：先按 ID 精确匹配，再按 Name 匹配，再按 VirtualIP 匹配。
func (s *Store) ResolveNode(hint string) (*Node, error) {
	// 1. 按 ID
	n, err := s.GetNode(hint)
	if err == nil {
		return n, nil
	}
	// 2. 按 Name
	n, err = s.GetNodeByName(hint)
	if err == nil {
		return n, nil
	}
	// 3. 按 VirtualIP
	var node Node
	if err := s.db.First(&node, "virtual_ip = ?", hint).Error; err == nil {
		return &node, nil
	}
	return nil, fmt.Errorf("无法解析节点: %q", hint)
}

// NextNodeID 返回下一个顺序节点 ID（当前最大值 +1，最小 100）。
func (s *Store) NextNodeID() (string, error) {
	var maxIDStr string
	err := s.db.Model(&Node{}).Select("COALESCE(MAX(CAST(id AS INTEGER)), 99)").Scan(&maxIDStr).Error
	if err != nil {
		return "", fmt.Errorf("查询最大 ID 失败: %w", err)
	}
	maxID, _ := strconv.Atoi(maxIDStr)
	if maxID < 99 {
		maxID = 99
	}
	return strconv.Itoa(maxID + 1), nil
}

// UpdateNodeMeta 更新节点的 Name 和 Remark。
func (s *Store) UpdateNodeMeta(id, name, remark string) error {
	updates := map[string]any{}
	if name != "" {
		updates["name"] = name
	}
	if remark != "" {
		updates["remark"] = remark
	}
	if len(updates) == 0 {
		return nil
	}
	return s.db.Model(&Node{}).Where("id = ?", id).Updates(updates).Error
}

// UpdateNodeGeo 持久化节点定位信息。
func (s *Store) UpdateNodeGeo(id string, lat, lon float64, city, country, accuracy, colIATA string, colLat, colLon, cfRttMs float64) error {
	return s.db.Model(&Node{}).Where("id = ?", id).Updates(map[string]any{
		"geo_lat":       lat,
		"geo_lon":       lon,
		"geo_city":      city,
		"geo_country":   country,
		"geo_accuracy":  accuracy,
		"geo_col_iata":  colIATA,
		"geo_col_lat":   colLat,
		"geo_col_lon":   colLon,
		"geo_cf_rtt_ms": cfRttMs,
	}).Error
}

// BumpGeneration 原子自增并返回新值（§5.3 per-node 单调；
// 串行化由 controller 子规格的重算队列保证，此处提供原子自增原语）。
func (s *Store) BumpGeneration(nodeID string) (uint64, error) {
	var newGen uint64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Node{}).Where("id = ?", nodeID).
			UpdateColumn("generation", gorm.Expr("generation + 1")).Error; err != nil {
			return err
		}
		var n Node
		if err := tx.Select("generation").First(&n, "id = ?", nodeID).Error; err != nil {
			return err
		}
		newGen = n.Generation
		return nil
	})
	return newGen, err
}

func (s *Store) AllocateLease(ip, nodeID string) error {
	return s.db.Create(&Lease{IP: ip, NodeID: nodeID}).Error
}

func (s *Store) ReleaseLease(ip string) error {
	return s.db.Delete(&Lease{}, "ip = ?", ip).Error
}
