//go:build integration

package store

import (
	"os"
	"testing"
)

// 通过环境变量提供 dsn，例如：
//
//	CORELINK_TEST_PG_DSN="postgres://user:pass@localhost:5432/corelink?sslmode=disable"
//	CORELINK_TEST_MYSQL_DSN="mysql://user:pass@tcp(localhost:3306)/corelink?parseTime=true"
func TestMultiDBMigrateAndCRUD(t *testing.T) {
	for _, env := range []string{"CORELINK_TEST_PG_DSN", "CORELINK_TEST_MYSQL_DSN"} {
		dsn := os.Getenv(env)
		if dsn == "" {
			t.Logf("跳过 %s（未设置）", env)
			continue
		}
		t.Run(env, func(t *testing.T) {
			s, err := Open(dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := s.Migrate(); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			n := &Node{ID: "it-n1", Role: "node", WGPubKey: "it-pk", VirtualIP: "100.64.9.9"}
			_ = s.DB().Where("id = ?", n.ID).Delete(&Node{})
			if err := s.CreateNode(n); err != nil {
				t.Fatalf("CreateNode: %v", err)
			}
			if _, err := s.BumpGeneration(n.ID); err != nil {
				t.Fatalf("BumpGeneration: %v", err)
			}
		})
	}
}
