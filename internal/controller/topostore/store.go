// Package topostore 持久化质量矩阵（稀疏+时间戳）+ 拓扑结果，使 controller 重启
// 后加载即服务（规格 §3.6 E8）：避免长期探测累积的质量矩阵重建与重启拓扑抖动。
//
// 本包只做字节存取：拓扑结果以 []byte blob 形式存/取，不关心 blob 内部结构。
// Result↔blob 的序列化由本包的 MarshalResult/UnmarshalResult 提供（处理 RoutePair
// 作 JSON map key 的限制），供调用方（Task2.7）复用。
package topostore

import (
	"encoding/json"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/controller/topology"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 陈旧老化默认阈值（导出供调用方复用）。
//
//   - DefaultStaleTTL：龄期 ≤ 此值的边为 fresh（高置信）；超过但 ≤ hardTTL 为
//     stale（仍可用，低置信）。
//   - DefaultHardTTL：龄期超过此值的边视为不可用，应被 PruneStale 删除。
const (
	DefaultStaleTTL = 10 * time.Minute
	DefaultHardTTL  = 30 * time.Minute
)

// QualityEdgeRecord 是质量矩阵边的存取记录（与 store.QualityEdge 一一对应）。
type QualityEdgeRecord struct {
	Src          string
	Dst          string
	Ingress      string
	RTTms        uint32
	LossPermille uint32
	UpdatedAt    time.Time
}

// IngressRecord 是每节点入口集的存取记录（与 store.IngressRow 一一对应）。
type IngressRecord struct {
	NodeID    string
	Blob      []byte
	UpdatedAt time.Time
}

// TopoStore 封装质量矩阵 / 拓扑结果 / 入口集的持久化。
type TopoStore struct {
	db *gorm.DB
}

// New 用底层 gorm.DB 构造 TopoStore（与既有仓储一致；调用方可传 store.DB()）。
func New(db *gorm.DB) *TopoStore { return &TopoStore{db: db} }

// SaveQuality 批量 upsert 质量边（同主键 (Src,Dst,Ingress) 覆盖更新）。
func (s *TopoStore) SaveQuality(edges []QualityEdgeRecord) error {
	if len(edges) == 0 {
		return nil
	}
	rows := make([]store.QualityEdge, len(edges))
	for i, e := range edges {
		rows[i] = store.QualityEdge{
			SrcNode:      e.Src,
			DstNode:      e.Dst,
			IngressID:    e.Ingress,
			RTTms:        e.RTTms,
			LossPermille: e.LossPermille,
			UpdatedAt:    e.UpdatedAt,
		}
	}
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "src_node"}, {Name: "dst_node"}, {Name: "ingress_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"rtt_ms", "loss_permille", "updated_at"}),
	}).Create(&rows).Error
}

// LoadQuality 读取全部质量边。稀疏：未存的边不出现。
func (s *TopoStore) LoadQuality() ([]QualityEdgeRecord, error) {
	var rows []store.QualityEdge
	if err := s.db.Order("src_node, dst_node, ingress_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]QualityEdgeRecord, len(rows))
	for i, r := range rows {
		out[i] = QualityEdgeRecord{
			Src:          r.SrcNode,
			Dst:          r.DstNode,
			Ingress:      r.IngressID,
			RTTms:        r.RTTms,
			LossPermille: r.LossPermille,
			UpdatedAt:    r.UpdatedAt,
		}
	}
	return out, nil
}

// SaveResult 存拓扑结果 blob。同版本幂等：OnConflict 更新 blob/时间戳。
func (s *TopoStore) SaveResult(version uint64, blob []byte) error {
	row := store.TopoResult{Version: version, BlobJSON: blob, CreatedAt: time.Now()}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "version"}},
		DoUpdates: clause.AssignmentColumns([]string{"blob_json", "created_at"}),
	}).Create(&row).Error
}

// LoadLatestResult 读取最大 version 的拓扑结果。无结果时 ok=false。
func (s *TopoStore) LoadLatestResult() (version uint64, blob []byte, ok bool, err error) {
	var row store.TopoResult
	res := s.db.Order("version DESC").Limit(1).Find(&row)
	if res.Error != nil {
		return 0, nil, false, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, nil, false, nil
	}
	return row.Version, row.BlobJSON, true, nil
}

// SaveIngressSets 批量 upsert 每节点入口集（同 NodeID 覆盖）。
func (s *TopoStore) SaveIngressSets(rows []IngressRecord) error {
	if len(rows) == 0 {
		return nil
	}
	models := make([]store.IngressRow, len(rows))
	for i, r := range rows {
		models[i] = store.IngressRow{NodeID: r.NodeID, BlobJSON: r.Blob, UpdatedAt: r.UpdatedAt}
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"blob_json", "updated_at"}),
	}).Create(&models).Error
}

// LoadIngressSets 读取全部每节点入口集（按 NodeID 升序）。
func (s *TopoStore) LoadIngressSets() ([]IngressRecord, error) {
	var rows []store.IngressRow
	if err := s.db.Order("node_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]IngressRecord, len(rows))
	for i, r := range rows {
		out[i] = IngressRecord{NodeID: r.NodeID, Blob: r.BlobJSON, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

// PruneStale 删除 UpdatedAt 早于 now-hardTTL 的质量边，返回删除数。
func (s *TopoStore) PruneStale(now time.Time, hardTTL time.Duration) (int, error) {
	cutoff := now.Add(-hardTTL)
	res := s.db.Where("updated_at < ?", cutoff).Delete(&store.QualityEdge{})
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// ClassifyStale 纯函数：按 staleTTL 把质量边分类为 fresh / stale。
//
//   - fresh：龄期（now-UpdatedAt）≤ staleTTL，高置信。
//   - stale：龄期 > staleTTL 但仍可用（低置信）。注意此函数不施加 hardTTL
//     上限——超 hardTTL 的边由 PruneStale 删除，调用方应先 Prune 再 Classify，
//     或自行确保输入已去除超硬 TTL 的边。
func ClassifyStale(records []QualityEdgeRecord, now time.Time, staleTTL time.Duration) (fresh, stale []QualityEdgeRecord) {
	for _, r := range records {
		if now.Sub(r.UpdatedAt) <= staleTTL {
			fresh = append(fresh, r)
		} else {
			stale = append(stale, r)
		}
	}
	return fresh, stale
}

// ---- Result 序列化（处理 RoutePair 作 JSON map key 的限制）----

// baselineEntry 把 Baseline 的 (RoutePair → K 路由) 摊平为可 JSON 化的条目。
// Go JSON 不支持 struct 作 map key，故 Baseline 转 slice 再序列化。
type baselineEntry struct {
	Pair   topology.RoutePair
	Routes [][]topology.Hop
}

// resultBlob 是 topology.Result 的可 JSON 序列化镜像。
type resultBlob struct {
	Version   uint64
	Roles     map[string]topology.Role
	Neighbors map[string][]topology.NeighborSpec
	Baseline  []baselineEntry
	ProbeSets map[string][]topology.ProbeTarget
}

// MarshalResult 把 topology.Result 序列化为 JSON blob（重启加载即服务的关键）。
// 处理 Baseline 的 RoutePair map key：摊平为 slice 后 JSON 化，不丢字段。
func MarshalResult(r topology.Result) ([]byte, error) {
	b := resultBlob{
		Version:   r.Version,
		Roles:     r.Roles,
		Neighbors: r.Neighbors,
		ProbeSets: r.ProbeSets,
	}
	if r.Baseline != nil {
		b.Baseline = make([]baselineEntry, 0, len(r.Baseline))
		for pair, routes := range r.Baseline {
			b.Baseline = append(b.Baseline, baselineEntry{Pair: pair, Routes: routes})
		}
	}
	return json.Marshal(b)
}

// UnmarshalResult 把 MarshalResult 产出的 JSON blob 还原为 topology.Result。
// round-trip 后 reflect.DeepEqual 相等（守护重启加载不丢字段）。
func UnmarshalResult(data []byte) (topology.Result, error) {
	var b resultBlob
	if err := json.Unmarshal(data, &b); err != nil {
		return topology.Result{}, err
	}
	r := topology.Result{
		Version:   b.Version,
		Roles:     b.Roles,
		Neighbors: b.Neighbors,
		ProbeSets: b.ProbeSets,
	}
	if b.Baseline != nil {
		r.Baseline = make(map[topology.RoutePair][][]topology.Hop, len(b.Baseline))
		for _, e := range b.Baseline {
			r.Baseline[e.Pair] = e.Routes
		}
	}
	return r, nil
}
