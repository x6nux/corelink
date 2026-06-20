package flowtrack

import (
	"sync"
	"sync/atomic"
	"time"
)

// Config 定义 Tracker 的配置参数。
type Config struct {
	TCPTimeout  time.Duration // TCP 流超时，默认 5 分钟
	UDPTimeout  time.Duration // UDP 流超时，默认 30 秒
	ICMPTimeout time.Duration // ICMP 流超时，默认 5 秒
	MaxFlows    int           // 最大流数量，默认 65536
	Shards      int           // 分片数量，默认 256
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		TCPTimeout:  5 * time.Minute,
		UDPTimeout:  30 * time.Second,
		ICMPTimeout: 5 * time.Second,
		MaxFlows:    65536,
		Shards:      256,
	}
}

// tcpClosingTimeout 是 TCP FlowClosing 状态的超时（简化版 2*MSL）。
const tcpClosingTimeout = 10 * time.Second

// shard 是一个分片，持有一组 Flow 并用独立锁保护。
type shard struct {
	mu    sync.Mutex
	flows map[FlowKey]*Flow
}

// Tracker 是分段锁流表，支持五元组流追踪、过期 GC 和并发安全访问。
type Tracker struct {
	cfg        Config
	shards     []shard
	totalFlows atomic.Int64 // 原子计数器，避免遍历分片时的锁竞争
}

// NewTracker 创建一个新的流追踪器。
func NewTracker(cfg Config) *Tracker {
	if cfg.Shards <= 0 {
		cfg.Shards = 256
	}
	if cfg.MaxFlows <= 0 {
		cfg.MaxFlows = 65536
	}
	if cfg.TCPTimeout <= 0 {
		cfg.TCPTimeout = 5 * time.Minute
	}
	if cfg.UDPTimeout <= 0 {
		cfg.UDPTimeout = 30 * time.Second
	}
	if cfg.ICMPTimeout <= 0 {
		cfg.ICMPTimeout = 5 * time.Second
	}

	shards := make([]shard, cfg.Shards)
	for i := range shards {
		shards[i].flows = make(map[FlowKey]*Flow)
	}

	return &Tracker{
		cfg:    cfg,
		shards: shards,
	}
}

// Track 处理一个原始 IP 包，返回关联的 Flow 和是否为新流。
// 如果包解析失败返回 (nil, false)。
// 如果流表已满（超过 MaxFlows）返回 (nil, false)，调用方应跳过 DPI 直接 L3 路由。
func (t *Tracker) Track(pkt []byte) (*Flow, bool) {
	key, _, err := ParsePacket(pkt)
	if err != nil {
		return nil, false
	}

	h := key.Hash()
	idx := int(h % uint64(len(t.shards)))
	s := &t.shards[idx]

	s.mu.Lock()
	defer s.mu.Unlock()

	// 查找现有流
	if f, ok := s.flows[key]; ok {
		f.mu.Lock()
		f.LastSeen = time.Now()
		f.Bytes += uint64(len(pkt))
		f.Packets++
		f.mu.Unlock()
		return f, false
	}

	// 检查流表是否已满（原子读取，无需加锁其他分片）
	if int(t.totalFlows.Load()) >= t.cfg.MaxFlows {
		return nil, false
	}

	// 创建新流
	now := time.Now()
	f := &Flow{
		Key:       key,
		State:     FlowNew,
		CreatedAt: now,
		LastSeen:  now,
		Bytes:     uint64(len(pkt)),
		Packets:   1,
	}
	s.flows[key] = f
	t.totalFlows.Add(1)
	return f, true
}

// Expire 遍历所有分片，删除超时的流。
// TCP FlowClosing 状态使用 10 秒超时（简化版 2*MSL）。
func (t *Tracker) Expire() {
	now := time.Now()

	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()

		for key, f := range s.flows {
			f.mu.Lock()
			elapsed := now.Sub(f.LastSeen)
			shouldDelete := false

			switch f.Key.Proto {
			case 6: // TCP
				if f.State == FlowClosing {
					shouldDelete = elapsed > tcpClosingTimeout
				} else {
					shouldDelete = elapsed > t.cfg.TCPTimeout
				}
			case 17: // UDP
				shouldDelete = elapsed > t.cfg.UDPTimeout
			case 1: // ICMP
				shouldDelete = elapsed > t.cfg.ICMPTimeout
			default:
				shouldDelete = elapsed > t.cfg.UDPTimeout
			}

			f.mu.Unlock()

			if shouldDelete {
				delete(s.flows, key)
				t.totalFlows.Add(-1)
			}
		}

		s.mu.Unlock()
	}
}

// Count 返回当前流表中的流数量。
func (t *Tracker) Count() int {
	return int(t.totalFlows.Load())
}
