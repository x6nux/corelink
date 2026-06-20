package nodestore

import "time"

// RouteCache ProbeRouter 选路结果缓存（重启后立即恢复路由）。
type RouteCache struct {
	DstVIP     string  `gorm:"primaryKey"` // 目标 VIP（如 "100.64.0.3"）
	DstNodeID  string  // 目标 nodeID（preferredRelay 恢复用）
	NextHopID  string  // 最优 NextHop nodeID
	NextHopVIP string  // 最优 NextHop VIP
	RTTMs      float64 // 延迟（ms）
	Label      string  // 路由描述（如 "direct" / "via 101(.3)"）
	UpdatedAt  time.Time
}

// ThroughputSample 带宽采样记录（Probe 测速或实际流量统计）。
type ThroughputSample struct {
	ID             uint      `gorm:"primaryKey;autoIncrement"`
	RouteKey       string    `gorm:"index:idx_route_time"` // 路由标识（如 "direct:100.64.0.3"）
	ThroughputMbps float64   // 吞吐量（MB/s）
	Source         string    // "probe" = Probe 填充测速 / "actual" = 实际流量统计
	SampledAt      time.Time `gorm:"index:idx_route_time"`
}

// ReportCache 上报缓存（Controller 离线时缓存，恢复后 flush）。
type ReportCache struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	Type      string    // "edge_event" / "quality_report" / "ingress"
	Payload   []byte    `gorm:"type:blob"` // proto 序列化
	CreatedAt time.Time `gorm:"index"`
}

// ConfigCache NodeConfig 缓存（Controller 不可达时离线启动）。
type ConfigCache struct {
	Key        string `gorm:"primaryKey"` // 固定 "latest"
	Generation uint64
	Data       []byte `gorm:"type:blob"` // proto 序列化
	UpdatedAt  time.Time
}
