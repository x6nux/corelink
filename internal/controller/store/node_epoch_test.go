package store

import "testing"

// TestNode_MigrateBackfillsEpoch 验证升级路径：旧库（nodes 无 epoch 列）
// AutoMigrate 加列时，存量行的 epoch 必须回填为 0（而非 NULL）。
func TestNode_MigrateBackfillsEpoch(t *testing.T) {
	s, err := Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 模拟旧库：手建无 epoch 列的 nodes 旧表并插存量行
	if err := s.db.Exec(`CREATE TABLE "nodes" ("id" text PRIMARY KEY,"role" text,"hostname" text,"wg_pub_key" text UNIQUE,"virtual_ip" text UNIQUE,"user" text,"generation" integer NOT NULL DEFAULT 0,"created_at" datetime,"updated_at" datetime)`).Error; err != nil {
		t.Fatalf("建旧表: %v", err)
	}
	if err := s.db.Exec(`INSERT INTO "nodes" ("id","role","generation") VALUES ('old1','agent',3)`).Error; err != nil {
		t.Fatalf("插存量行: %v", err)
	}

	// AutoMigrate 加 epoch 列并回填默认值
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var cnt int64
	s.db.Raw(`SELECT COUNT(*) FROM nodes WHERE epoch IS NULL`).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("存量行 epoch 未回填，NULL 计数=%d", cnt)
	}
	var n Node
	if err := s.db.First(&n, "id = ?", "old1").Error; err != nil {
		t.Fatal(err)
	}
	if n.Epoch != 0 || n.Generation != 3 {
		t.Fatalf("回填错误: epoch=%d gen=%d", n.Epoch, n.Generation)
	}
}
