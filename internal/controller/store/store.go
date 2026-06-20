// Package store 提供 CoreLink controller 的持久化层（GORM 多库）。
package store

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite" // 纯 Go SQLite，无 CGO
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store 封装数据库句柄与仓储方法。
type Store struct {
	db *gorm.DB
}

// Open 按 dsn 前缀选择驱动：
//
//	sqlite://<path 或 :memory:>
//	postgres://...
//	mysql://user:pass@tcp(host:port)/db
func Open(dsn string) (*Store, error) {
	var dial gorm.Dialector
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		dial = sqlite.Open(strings.TrimPrefix(dsn, "sqlite://"))
	case strings.HasPrefix(dsn, "postgres://"):
		dial = postgres.Open(dsn)
	case strings.HasPrefix(dsn, "mysql://"):
		dial = mysql.Open(strings.TrimPrefix(dsn, "mysql://"))
	default:
		return nil, fmt.Errorf("store: 不支持的 dsn: %q", dsn)
	}
	db, err := gorm.Open(dial, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库失败: %w", err)
	}
	return &Store{db: db}, nil
}

// DB 暴露底层句柄（仅供仓储/迁移内部使用）。
func (s *Store) DB() *gorm.DB { return s.db }
