package proberouter

import (
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/nodestore"
)

// ThroughputTracker 管理每条路由的吞吐量评分（EWMA 衰减 + 智能调度）。
type ThroughputTracker struct {
	mu       sync.Mutex
	entries  map[string]*throughputEntry // routeKey → entry
	halfLife time.Duration
	store    *nodestore.Store
}

type throughputEntry struct {
	mu     sync.Mutex
	score  float64   // EWMA 吞吐量（MB/s）
	alpha  float64   // EWMA 系数
	lastAt time.Time // 最后更新时间
}

// NewThroughputTracker 创建 ThroughputTracker，从 DB 加载历史数据。
func NewThroughputTracker(store *nodestore.Store, halfLife time.Duration) *ThroughputTracker {
	if halfLife == 0 {
		halfLife = 2 * time.Hour
	}
	tt := &ThroughputTracker{
		entries:  make(map[string]*throughputEntry),
		halfLife: halfLife,
		store:    store,
	}
	// 从 DB 加载最近的采样恢复 score
	if store != nil {
		tt.loadFromDB()
	}
	return tt
}

// Record 记录一次吞吐量采样（实际流量或 Probe 测速）。
func (tt *ThroughputTracker) Record(routeKey string, throughputMbps float64, source string) {
	tt.mu.Lock()
	e, ok := tt.entries[routeKey]
	if !ok {
		e = &throughputEntry{alpha: 0.3, lastAt: time.Now()}
		tt.entries[routeKey] = e
	}
	tt.mu.Unlock()

	e.mu.Lock()
	// EWMA 更新
	if e.score == 0 {
		e.score = throughputMbps
	} else {
		e.score = e.alpha*throughputMbps + (1-e.alpha)*e.score
	}
	e.lastAt = time.Now()
	e.mu.Unlock()

	// 持久化
	if tt.store != nil {
		tt.store.SaveThroughput(nodestore.ThroughputSample{
			RouteKey:       routeKey,
			ThroughputMbps: throughputMbps,
			Source:         source,
			SampledAt:      time.Now(),
		})
	}
	slog.Debug("throughput: 记录采样", "route", routeKey, "mbps", throughputMbps, "source", source)
}

// GetScore 获取路由的当前吞吐评分（含时间衰减）。
func (tt *ThroughputTracker) GetScore(routeKey string) float64 {
	tt.mu.Lock()
	e, ok := tt.entries[routeKey]
	tt.mu.Unlock()
	if !ok {
		return 0
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	return tt.decayedScore(e)
}

// NeedsProbe 判断该路由是否需要 Probe 填充测速（score 衰减后低于阈值）。
func (tt *ThroughputTracker) NeedsProbe(routeKey string) bool {
	const threshold = 0.1 // MB/s
	return tt.GetScore(routeKey) < threshold
}

// decayedScore 计算经时间衰减后的 score（调用方须持有 e.mu）。
func (tt *ThroughputTracker) decayedScore(e *throughputEntry) float64 {
	elapsed := time.Since(e.lastAt)
	decay := math.Pow(2, -float64(elapsed)/float64(tt.halfLife))
	return e.score * decay
}

// Prune 清理 DB 中过期的采样数据。
func (tt *ThroughputTracker) Prune() {
	if tt.store != nil {
		tt.store.PruneThroughput(7 * 24 * time.Hour)
	}
}

// loadFromDB 从 DB 加载最近采样恢复初始 score。
func (tt *ThroughputTracker) loadFromDB() {
	since := time.Now().Add(-tt.halfLife * 2) // 加载 2 个半衰期内的数据
	samples, err := tt.store.QueryThroughput("", since)
	if err != nil || len(samples) == 0 {
		return
	}
	// 按 routeKey 分组取最新
	latest := make(map[string]nodestore.ThroughputSample)
	for _, s := range samples {
		if prev, ok := latest[s.RouteKey]; !ok || s.SampledAt.After(prev.SampledAt) {
			latest[s.RouteKey] = s
		}
	}
	for key, s := range latest {
		tt.entries[key] = &throughputEntry{
			score:  s.ThroughputMbps,
			alpha:  0.3,
			lastAt: s.SampledAt,
		}
	}
	slog.Info("throughput: 从 DB 恢复", "entries", len(latest))
}
