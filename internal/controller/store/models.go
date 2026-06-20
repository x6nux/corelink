package store

import "time"

// Node 注册节点（agent 或 relay）。
// ID 为顺序数字字符串（"100", "101", ...），兼顾可读性与全网引用一致性。
type Node struct {
	ID         string `gorm:"primaryKey"` // 顺序数字 ID（"100", "101"）
	Name       string `gorm:"index"`                // 用户可设的唯一名称（默认取 hostname）
	Remark     string                               // 备注
	Role       string `gorm:"index"`                // "agent" | "relay"
	Hostname   string                               // 系统主机名（注册时自动采集）
	WGPubKey   string                               // deprecated
	VirtualIP  string `gorm:"uniqueIndex"`           // /32
	User       string `gorm:"index"`                // ACL 用户归属
	Generation uint64 `gorm:"not null;default:0"`
	Epoch      uint64 `gorm:"not null;default:0"`
	// 定位信息（节点上报后持久化，controller 重启时恢复）
	GeoLat      float64 `gorm:"default:0"`
	GeoLon      float64 `gorm:"default:0"`
	GeoCity     string
	GeoCountry  string
	GeoAccuracy string
	GeoColIATA  string
	GeoColLat   float64 `gorm:"default:0"`
	GeoColLon   float64 `gorm:"default:0"`
	GeoCfRttMs  float64 `gorm:"default:0"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Lease 虚拟 IP 租约（§6.3）。
type Lease struct {
	IP        string `gorm:"primaryKey"` // 100.64.x.y/32
	NodeID    string `gorm:"index"`
	CreatedAt time.Time
}

// EnrollKey 注册密钥（§5.1）。
// 消费与吊销彻底分离：
//   - Consumed 表示一次性 key 已被消费/烧毁（入网成功或抢占成功后置位，补偿失败时复位）。
//   - Revoked  仅表示管理员主动吊销，绝不被消费/补偿逻辑触碰。
type EnrollKey struct {
	Key       string `gorm:"primaryKey"`
	Reusable  bool
	ExpiresAt *time.Time
	Tag       string
	Revoked   bool `gorm:"index"`                        // 管理员主动吊销
	Consumed  bool `gorm:"index;not null;default:false"` // 一次性 key 已被消费/烧毁（not null+default 确保升级旧库加列时存量行回填 false 而非 NULL）
	CreatedAt time.Time
}

// Cert 已签发的节点证书元数据（§5.2）。
type Cert struct {
	Serial    string `gorm:"primaryKey"` // 证书序列号（十进制字符串）
	NodeID    string `gorm:"index"`
	NotAfter  time.Time
	Revoked   bool `gorm:"index"`
	RevokedAt *time.Time
	CreatedAt time.Time
	// Fingerprint 是证书 SHA-256 指纹（tunnel.CertFingerprint 格式）；§4.0 mesh 邻居信任锚。
	Fingerprint string `gorm:"index"`
}

// RelayLink controller 定义的 relay 邻接拓扑（§7.2.2）。
// 注：邻接是无向的；(A,B) 与 (B,A) 的去重规约留待 S2（controller）决定，S1 不施加约束。
type RelayLink struct {
	ID         uint   `gorm:"primaryKey;autoIncrement"`
	RelayID    string `gorm:"index:idx_link,unique"`
	NeighborID string `gorm:"index:idx_link,unique"`
	CreatedAt  time.Time
}

// ACLPolicy 策略文件版本（§6.1）。
type ACLPolicy struct {
	Version   uint   `gorm:"primaryKey;autoIncrement"`
	Document  string // 原始策略 JSON
	Author    string
	CreatedAt time.Time
}

// CARoot 存储 CA 证书与 AES-GCM 加密的私钥（§5.2）。
type CARoot struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	CertPEM   []byte // CA 证书 PEM
	EncKeyPEM []byte // AES-256-GCM 加密后的私钥 PEM（nonce||ciphertext）
	CreatedAt time.Time
}

// RelayInfo relay 监听端点+协议（与 RelayLink 邻接拓扑分工）。
type RelayInfo struct {
	NodeID         string `gorm:"primaryKey"`
	TunnelEndpoint string // 外层 TLS 端点
	UDPEndpoint    string // UDP/QUIC 端点
	Protocols      string // 逗号分隔的协议列表
	Priority       uint
}

// QualityEdge 持久化的质量矩阵边（§3.6 E8）。
// 稀疏存储：只存探测过/保留的边；复合主键 (SrcNode,DstNode,IngressID)。
// 重启冷启动时加载，避免长期探测累积的质量矩阵重建。
type QualityEdge struct {
	SrcNode      string `gorm:"primaryKey"`           // 拨出方物理节点 ID
	DstNode      string `gorm:"primaryKey"`           // 目的物理节点 ID
	IngressID    string `gorm:"primaryKey"`           // 目的入口 ID
	RTTms        uint32 `gorm:"column:rtt_ms"`        // 往返时延（毫秒）；显式列名，避免 GORM 反直觉分词 RT_Tms。
	LossPermille uint32 `gorm:"column:loss_permille"` // 丢包率（千分比）
	UpdatedAt    time.Time
}

// TopoResult 持久化的拓扑优化结果（§3.6 E8）。
// 版本递增；BlobJSON 为 topology.Result 序列化字节（序列化语义见 topostore）。
type TopoResult struct {
	Version   uint64 `gorm:"primaryKey"` // 拓扑版本号（递增）
	BlobJSON  []byte // 序列化结果 blob
	CreatedAt time.Time
}

// IngressRow 持久化的每节点入口集（§3.6 E8）。
type IngressRow struct {
	NodeID    string `gorm:"primaryKey"` // 节点 ID
	BlobJSON  []byte // 序列化入口集 blob
	UpdatedAt time.Time
}

// AdminCredential 管理员凭据（用户名 + bcrypt 密码哈希）。
// 单行覆盖语义：Username 为主键，当前仅支持单管理员。
type AdminCredential struct {
	Username string `gorm:"primaryKey"`
	PassHash string // bcrypt 哈希
}

// SystemSecret 系统级密钥（生成一次，持久固定）。
// Key 为主键，Value 存原始字节。
type SystemSecret struct {
	Key   string `gorm:"primaryKey"`
	Value []byte
}

// NodeAlias 节点别名与域名绑定。
type NodeAlias struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	NodeID    string `gorm:"index"`
	Name      string `gorm:"index"`
	FQDN      string `gorm:"uniqueIndex"`
	Kind      string `gorm:"index"` // internal | external
	TargetVIP string
	Enabled   bool `gorm:"index;not null;default:true"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PublishedRoute 节点发布的可达网段。
type PublishedRoute struct {
	ID            uint   `gorm:"primaryKey;autoIncrement"`
	NodeID        string `gorm:"index"`
	Kind          string `gorm:"index"` // direct | static_mapping | discovered_mapping
	RouteCIDR     string `gorm:"index"`
	VIPCIDR       string `gorm:"index"`
	TargetCIDR    string `gorm:"index"`
	Priority      uint32 `gorm:"not null;default:100"`
	Metric        uint32 `gorm:"not null;default:100"`
	SNAT          bool   `gorm:"not null;default:true"`
	Enabled       bool   `gorm:"index;not null;default:true"`
	DiscoveryMode string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// DiscoveredMapping 节点上报的 ARP/邻居发现结果与 controller 仲裁状态。
type DiscoveredMapping struct {
	ID         uint   `gorm:"primaryKey;autoIncrement"`
	RouteID    uint   `gorm:"index"`
	NodeID     string `gorm:"index"`
	TargetIP   string `gorm:"index"`
	VIPIP      string `gorm:"index"`
	Priority   uint32
	ObservedAt time.Time
	StaleAfter time.Duration
	Winner     bool `gorm:"index"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DNSSettings 全局 DNS 配置（单行覆盖语义）。
type DNSSettings struct {
	ID            uint `gorm:"primaryKey;autoIncrement"`
	Enabled       bool `gorm:"not null;default:false"`
	ZonesJSON     string
	UpstreamsJSON string
	InterceptMode string
	ListenAddr    string
	ListenPort    uint32
	LANIfacesJSON string
	LANCIDRsJSON  string
	UpdatedAt     time.Time
}

// SplitRuleRow 分流规则持久化模型。
type SplitRuleRow struct {
	ID         uint   `gorm:"primaryKey;autoIncrement"`
	NodeID     string `gorm:"index"`
	Match      string
	Action     string
	ExitNodeID string
	SortOrder  uint32 `gorm:"not null;default:0"`
	Enabled    bool   `gorm:"index;not null;default:true"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// GeoIPMeta GeoIP 数据库元数据（dat 文件存文件系统）。
type GeoIPMeta struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	SHA256    string `gorm:"uniqueIndex"`
	FilePath  string
	FileSize  int64
	UpdatedAt time.Time
}
