// Package featureflag 提供并发安全的 feature flag 管理。
package featureflag

import "sync"

// 已知的 feature flag 名称常量。
const (
	VIPRouting  = "vip_routing"
	TLS0RTT     = "tls_0rtt"
	SplitTunnel = "split_tunnel"
)

// Flags 是并发安全的 feature flag 集合。
type Flags struct {
	mu    sync.RWMutex
	flags map[string]bool
}

// New 创建一个所有 flag 默认关闭的实例。
func New() *Flags { return &Flags{flags: make(map[string]bool)} }

// FromMap 从配置 map 创建 Flags 实例。
func FromMap(m map[string]bool) *Flags {
	f := New()
	f.mu.Lock()
	for k, v := range m {
		f.flags[k] = v
	}
	f.mu.Unlock()
	return f
}

// Enabled 返回指定 flag 是否启用，未设置的 flag 视为关闭。
func (f *Flags) Enabled(name string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.flags[name]
}

// Set 设置指定 flag 的开关状态。
func (f *Flags) Set(name string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flags[name] = on
}
