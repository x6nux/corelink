// Package nodestore 提供 Node 侧 SQLite 持久化层（路由缓存、测速数据、配置缓存）。
package nodestore

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store 是 Node 侧持久化存储。
type Store struct {
	db *gorm.DB
}

// Open 打开 SQLite 数据库并初始化表结构。
// path 为数据库文件路径（如 "/var/lib/corelink/node.db"）或 ":memory:"。
func Open(path string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("nodestore: 打开数据库失败: %w", err)
	}
	// WAL 模式：并发读写安全 + 性能
	sqlDB, _ := db.DB()
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			slog.Warn("nodestore: PRAGMA 设置失败", "pragma", pragma, "err", err)
		}
	}
	// AutoMigrate
	if err := db.AutoMigrate(&RouteCache{}, &ThroughputSample{}, &ConfigCache{}, &ReportCache{}); err != nil {
		return nil, fmt.Errorf("nodestore: 迁移失败: %w", err)
	}
	slog.Info("nodestore: 数据库已打开", "path", path)
	return &Store{db: db}, nil
}

// ─── RouteCache ─────────────────────────────────────────────────

// SaveRoutes 批量保存路由缓存（upsert）。
func (s *Store) SaveRoutes(routes []RouteCache) error {
	for i := range routes {
		routes[i].UpdatedAt = time.Now()
	}
	return s.db.Save(&routes).Error
}

// LoadRoutes 读取全部路由缓存。
func (s *Store) LoadRoutes() ([]RouteCache, error) {
	var routes []RouteCache
	err := s.db.Find(&routes).Error
	return routes, err
}

// ─── ThroughputSample ───────────────────────────────────────────

// SaveThroughput 写入一条带宽采样。
func (s *Store) SaveThroughput(sample ThroughputSample) error {
	if sample.SampledAt.IsZero() {
		sample.SampledAt = time.Now()
	}
	return s.db.Create(&sample).Error
}

// QueryThroughput 查询指定路由 since 以来的采样。
func (s *Store) QueryThroughput(routeKey string, since time.Time) ([]ThroughputSample, error) {
	var samples []ThroughputSample
	err := s.db.Where("route_key = ? AND sampled_at > ?", routeKey, since).
		Order("sampled_at DESC").Find(&samples).Error
	return samples, err
}

// PruneThroughput 清理过期采样（保留 retention 时间内的数据）。
func (s *Store) PruneThroughput(retention time.Duration) error {
	cutoff := time.Now().Add(-retention)
	return s.db.Where("sampled_at < ?", cutoff).Delete(&ThroughputSample{}).Error
}

// ─── ReportCache ────────────────────────────────────────────────

// SaveReport 缓存一条上报数据（Controller 离线时）。
func (s *Store) SaveReport(typ string, payload []byte) error {
	return s.db.Create(&ReportCache{Type: typ, Payload: payload, CreatedAt: time.Now()}).Error
}

// FlushReports 读取并删除所有缓存的上报数据（按 created_at 升序，保证时序）。
// 返回记录列表，调用方逐条上报成功后应调 DeleteReports 删除。
func (s *Store) FlushReports() ([]ReportCache, error) {
	var reports []ReportCache
	err := s.db.Order("created_at ASC").Find(&reports).Error
	return reports, err
}

// DeleteReports 按 ID 列表删除已上报的缓存记录。
func (s *Store) DeleteReports(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.Where("id IN ?", ids).Delete(&ReportCache{}).Error
}

// PruneReports 清理过期上报缓存（默认 7 天）。
func (s *Store) PruneReports(retention time.Duration) error {
	cutoff := time.Now().Add(-retention)
	return s.db.Where("created_at < ?", cutoff).Delete(&ReportCache{}).Error
}

// ─── ConfigCache ────────────────────────────────────────────────

// SaveConfig 缓存最新 NodeConfig（proto 序列化）。
func (s *Store) SaveConfig(gen uint64, data []byte) error {
	c := ConfigCache{Key: "latest", Generation: gen, Data: data, UpdatedAt: time.Now()}
	return s.db.Save(&c).Error
}

// LoadConfig 读取缓存的 NodeConfig。无缓存返回 nil, nil。
func (s *Store) LoadConfig() (*ConfigCache, error) {
	var c ConfigCache
	err := s.db.First(&c, "key = ?", "latest").Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
