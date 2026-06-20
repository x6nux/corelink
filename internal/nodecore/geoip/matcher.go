package geoip

import (
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
)

// Matcher 提供 IP → 国家代码的查询能力。
// Phase 1 仅索引 IPv4 前缀，IPv6 暂跳过。
type Matcher struct {
	mu      sync.RWMutex
	v4table map[string][]netip.Prefix // 国家代码(小写) → IPv4 前缀列表
	codes   []string                  // 所有已知国家代码（小写，已排序）
}

// LoadFile 从磁盘加载 geoip.dat 文件并构建 Matcher。
func LoadFile(path string) (*Matcher, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: 读取文件 %s: %w", path, err)
	}
	return LoadBytes(data)
}

// LoadBytes 从原始字节构建 Matcher。
func LoadBytes(data []byte) (*Matcher, error) {
	list, err := parseGeoIPList(data)
	if err != nil {
		return nil, fmt.Errorf("geoip: 解析数据: %w", err)
	}

	m := &Matcher{
		v4table: make(map[string][]netip.Prefix),
	}

	for _, entry := range list.Entry {
		code := strings.ToLower(entry.CountryCode)
		for _, c := range entry.CIDR {
			pfx, ok := cidrToPrefix(c)
			if !ok {
				continue
			}
			// Phase 1: 仅索引 IPv4
			if !pfx.Addr().Is4() {
				continue
			}
			m.v4table[code] = append(m.v4table[code], pfx)
		}
	}

	// 收集并排序国家代码
	m.codes = make([]string, 0, len(m.v4table))
	for code := range m.v4table {
		m.codes = append(m.codes, code)
	}
	sort.Strings(m.codes)

	return m, nil
}

// LookupCIDRs 返回指定国家代码的所有 IPv4 前缀。
// code 不区分大小写，未找到返回 nil。
func (m *Matcher) LookupCIDRs(code string) []netip.Prefix {
	m.mu.RLock()
	defer m.mu.RUnlock()

	prefixes := m.v4table[strings.ToLower(code)]
	if len(prefixes) == 0 {
		return nil
	}

	// 返回副本，避免调用方修改内部状态
	result := make([]netip.Prefix, len(prefixes))
	copy(result, prefixes)
	return result
}

// LookupIP 返回 IP 地址所属的国家代码（小写），未找到返回空字符串。
// 注意: Phase 1 为 O(N) 线性扫描，仅适合 CLI 查询场景，非逐包路径。
func (m *Matcher) LookupIP(ip netip.Addr) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 仅支持 IPv4
	if !ip.Is4() {
		return ""
	}

	for _, code := range m.codes {
		for _, pfx := range m.v4table[code] {
			if pfx.Contains(ip) {
				return code
			}
		}
	}
	return ""
}

// Codes 返回所有已知的国家代码（小写，已排序）。
func (m *Matcher) Codes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]string, len(m.codes))
	copy(result, m.codes)
	return result
}
