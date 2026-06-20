package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// ---------- NodeAlias ----------

func (s *Store) CreateNodeAlias(a *NodeAlias) error {
	return s.db.Create(a).Error
}

func (s *Store) ListNodeAliases() ([]NodeAlias, error) {
	var out []NodeAlias
	err := s.db.Order("node_id, name").Find(&out).Error
	return out, err
}

func (s *Store) ListNodeAliasesByNode(nodeID string) ([]NodeAlias, error) {
	var out []NodeAlias
	err := s.db.Where("node_id = ?", nodeID).Order("name").Find(&out).Error
	return out, err
}

func (s *Store) GetNodeAlias(id uint) (*NodeAlias, error) {
	var a NodeAlias
	err := s.db.First(&a, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) DeleteNodeAlias(id uint) error {
	return s.db.Delete(&NodeAlias{}, id).Error
}

// ---------- PublishedRoute ----------

func (s *Store) CreatePublishedRoute(r *PublishedRoute) error {
	return s.db.Create(r).Error
}

func (s *Store) ListPublishedRoutes() ([]PublishedRoute, error) {
	var out []PublishedRoute
	err := s.db.Where("enabled = ?", true).Order("node_id, kind, priority").Find(&out).Error
	return out, err
}

func (s *Store) ListAllPublishedRoutes() ([]PublishedRoute, error) {
	var out []PublishedRoute
	err := s.db.Order("node_id, kind, priority").Find(&out).Error
	return out, err
}

func (s *Store) ListPublishedRoutesByNode(nodeID string) ([]PublishedRoute, error) {
	var out []PublishedRoute
	err := s.db.Where("node_id = ?", nodeID).Order("kind, priority").Find(&out).Error
	return out, err
}

func (s *Store) GetPublishedRoute(id uint) (*PublishedRoute, error) {
	var r PublishedRoute
	err := s.db.First(&r, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) SetPublishedRouteEnabled(id uint, enabled bool) error {
	return s.db.Model(&PublishedRoute{}).Where("id = ?", id).
		UpdateColumn("enabled", enabled).Error
}

func (s *Store) DeletePublishedRoute(id uint) error {
	return s.db.Delete(&PublishedRoute{}, id).Error
}

// ---------- DNSSettings ----------

func (s *Store) UpsertDNSSettings(d *DNSSettings) error {
	d.UpdatedAt = time.Now()
	return s.db.Save(d).Error
}

func (s *Store) GetDNSSettings() (*DNSSettings, error) {
	var d DNSSettings
	err := s.db.First(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ---------- DiscoveredMapping ----------

func (s *Store) UpsertDiscoveredMapping(m *DiscoveredMapping) error {
	m.UpdatedAt = time.Now()
	existing := &DiscoveredMapping{}
	err := s.db.Where("route_id = ? AND node_id = ? AND target_ip = ?",
		m.RouteID, m.NodeID, m.TargetIP).First(existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(m).Error
	}
	if err != nil {
		return err
	}
	m.ID = existing.ID
	return s.db.Save(m).Error
}

func (s *Store) ListFreshDiscoveredMappings(now time.Time) ([]DiscoveredMapping, error) {
	var all []DiscoveredMapping
	err := s.db.Order("route_id, priority, observed_at DESC, node_id").Find(&all).Error
	if err != nil {
		return nil, err
	}
	var out []DiscoveredMapping
	for _, m := range all {
		if m.ObservedAt.Add(m.StaleAfter).After(now) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Store) ListDiscoveredMappingsByRoute(routeID uint) ([]DiscoveredMapping, error) {
	var out []DiscoveredMapping
	err := s.db.Where("route_id = ?", routeID).
		Order("priority, observed_at DESC, node_id").Find(&out).Error
	return out, err
}

func (s *Store) SetDiscoveredMappingWinner(id uint, winner bool) error {
	return s.db.Model(&DiscoveredMapping{}).Where("id = ?", id).
		UpdateColumn("winner", winner).Error
}
