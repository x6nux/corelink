package store

import "testing"

// ─── Migrate 表创建完整性测试 ────────────────────────────────────────────────

// TestMigrateCreatesAllTables 验证 Migrate 创建所有已声明的模型表。
func TestMigrateCreatesAllTables(t *testing.T) {
	s := newMemStore(t)
	// 基础表（阶段 1-2）。
	baseTables := []string{
		"nodes", "leases", "enroll_keys", "certs",
		"relay_links", "acl_policies", "ca_roots", "relay_infos",
	}
	// 扩展表（阶段 3+）。
	extTables := []string{
		"quality_edges", "topo_results", "ingress_rows",
	}
	for _, tbl := range append(baseTables, extTables...) {
		if !s.DB().Migrator().HasTable(tbl) {
			t.Errorf("Migrate 后缺少表: %s", tbl)
		}
	}
}

// TestMigrateIdempotent 验证多次调用 Migrate 不报错（幂等）。
func TestMigrateIdempotent(t *testing.T) {
	s, err := Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("第一次 Migrate: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("第二次 Migrate: %v", err)
	}
}

// ─── 模型列完整性测试 ────────────────────────────────────────────────────────

func TestNodeHasAllColumns(t *testing.T) {
	s := newMemStore(t)
	columns := []string{"id", "role", "hostname", "wg_pub_key", "virtual_ip", "user", "generation", "epoch"}
	for _, col := range columns {
		if !s.DB().Migrator().HasColumn(&Node{}, col) {
			t.Errorf("Node 缺少列: %s", col)
		}
	}
}

func TestQualityEdgeHasAllColumns(t *testing.T) {
	s := newMemStore(t)
	columns := []string{"src_node", "dst_node", "ingress_id", "rtt_ms", "loss_permille"}
	for _, col := range columns {
		if !s.DB().Migrator().HasColumn(&QualityEdge{}, col) {
			t.Errorf("QualityEdge 缺少列: %s", col)
		}
	}
}

func TestTopoResultHasAllColumns(t *testing.T) {
	s := newMemStore(t)
	columns := []string{"version", "blob_json"}
	for _, col := range columns {
		if !s.DB().Migrator().HasColumn(&TopoResult{}, col) {
			t.Errorf("TopoResult 缺少列: %s", col)
		}
	}
}

func TestIngressRowHasAllColumns(t *testing.T) {
	s := newMemStore(t)
	columns := []string{"node_id", "blob_json"}
	for _, col := range columns {
		if !s.DB().Migrator().HasColumn(&IngressRow{}, col) {
			t.Errorf("IngressRow 缺少列: %s", col)
		}
	}
}

// ─── QualityEdge CRUD 基本操作（验证 Migrate 的复合主键正确）────────────────

func TestQualityEdgeCRUD(t *testing.T) {
	s := newMemStore(t)
	edge := QualityEdge{
		SrcNode: "src1", DstNode: "dst1", IngressID: "ing1",
		RTTms: 10, LossPermille: 5,
	}
	if err := s.db.Create(&edge).Error; err != nil {
		t.Fatalf("Create QualityEdge: %v", err)
	}
	// 复合主键查询。
	var got QualityEdge
	err := s.db.First(&got, "src_node = ? AND dst_node = ? AND ingress_id = ?", "src1", "dst1", "ing1").Error
	if err != nil {
		t.Fatalf("查询 QualityEdge: %v", err)
	}
	if got.RTTms != 10 || got.LossPermille != 5 {
		t.Errorf("QualityEdge 数据错误: rtt=%d loss=%d", got.RTTms, got.LossPermille)
	}
}

