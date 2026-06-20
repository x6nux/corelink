package routepolicy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ResolveInput 是解析器的输入。
type ResolveInput struct {
	Aliases     []store.NodeAlias
	Routes      []store.PublishedRoute
	Discovered  []store.DiscoveredMapping
	DNSSettings *store.DNSSettings
	Now         time.Time
}

// ResolveOutput 是解析器的输出，按节点分组。
type ResolveOutput struct {
	PublishedPrefixes map[string][]string                 // ownerNodeID → []CIDR（所有访问方可见）
	EgressRules       map[string][]*genv1.EgressRule      // ownerNodeID → 出口规则
	DiscoveryConfigs  map[string][]*genv1.DiscoveryConfig // ownerNodeID → 发现配置
	DNSRecords        []*genv1.DNSRecord
	DNSConfig         *genv1.DNSConfig
}

// Resolve 将 store 数据转换为规范化配置视图。
func Resolve(in ResolveInput) *ResolveOutput {
	out := &ResolveOutput{
		PublishedPrefixes: make(map[string][]string),
		EgressRules:       make(map[string][]*genv1.EgressRule),
		DiscoveryConfigs:  make(map[string][]*genv1.DiscoveryConfig),
	}

	// DNS 记录：从别名生成
	for _, a := range in.Aliases {
		if !a.Enabled || a.TargetVIP == "" {
			continue
		}
		rt := "A"
		if addr, err := netip.ParseAddr(a.TargetVIP); err == nil && addr.Is6() {
			rt = "AAAA"
		}
		out.DNSRecords = append(out.DNSRecords, &genv1.DNSRecord{
			Fqdn:        a.FQDN,
			TargetIp:    a.TargetVIP,
			RecordType:  rt,
			OwnerNodeId: a.NodeID,
		})
	}
	sort.Slice(out.DNSRecords, func(i, j int) bool {
		return out.DNSRecords[i].Fqdn < out.DNSRecords[j].Fqdn
	})

	// DNS 配置
	if in.DNSSettings != nil && in.DNSSettings.Enabled {
		out.DNSConfig = buildDNSConfig(in.DNSSettings, out.DNSRecords)
	}

	// 发现映射的 winner 索引: routeID → []winner VIPIP/32
	winnersByRoute := buildWinnerIndex(in.Discovered)

	// 处理路由
	for _, r := range in.Routes {
		if !r.Enabled {
			continue
		}
		switch r.Kind {
		case "direct":
			if r.RouteCIDR != "" {
				out.PublishedPrefixes[r.NodeID] = append(out.PublishedPrefixes[r.NodeID], r.RouteCIDR)
				out.EgressRules[r.NodeID] = append(out.EgressRules[r.NodeID], &genv1.EgressRule{
					Kind:         "direct",
					VipPrefix:    r.RouteCIDR,
					TargetPrefix: r.RouteCIDR,
					Snat:         r.SNAT,
					Priority:     r.Priority,
				})
			}
		case "static_mapping":
			if r.VIPCIDR != "" {
				out.PublishedPrefixes[r.NodeID] = append(out.PublishedPrefixes[r.NodeID], r.VIPCIDR)
				out.EgressRules[r.NodeID] = append(out.EgressRules[r.NodeID], &genv1.EgressRule{
					Kind:         "static_mapping",
					VipPrefix:    r.VIPCIDR,
					TargetPrefix: r.TargetCIDR,
					Snat:         r.SNAT,
					Priority:     r.Priority,
				})
			}
		case "discovered_mapping":
			// 只发布 winner /32，不发布整个 VIP 池
			for _, vip32 := range winnersByRoute[r.ID] {
				out.PublishedPrefixes[r.NodeID] = append(out.PublishedPrefixes[r.NodeID], vip32)
			}
			// 出口规则按 winner /32 生成（提前计算 targets 避免循环内重复扫描）
			targets := discoveredTargetsByRoute(in.Discovered, r.ID)
			for i, vip32 := range winnersByRoute[r.ID] {
				if i >= len(targets) {
					break
				}
				out.EgressRules[r.NodeID] = append(out.EgressRules[r.NodeID], &genv1.EgressRule{
					Kind:         "discovered_mapping",
					VipPrefix:    vip32,
					TargetPrefix: targets[i],
					Snat:         r.SNAT,
					Priority:     r.Priority,
				})
			}
			// 发现配置：下发给出口 node
			out.DiscoveryConfigs[r.NodeID] = append(out.DiscoveryConfigs[r.NodeID], &genv1.DiscoveryConfig{
				RouteId:           uint32(r.ID),
				TargetCidr:        r.TargetCIDR,
				VipPoolCidr:       r.VIPCIDR,
				Mode:              defaultDiscoveryMode(r.DiscoveryMode),
				IntervalSeconds:   30,
				StaleAfterSeconds: 300,
				Priority:          r.Priority,
			})
		}
	}

	// 排序确保确定性
	for nodeID := range out.PublishedPrefixes {
		sort.Strings(out.PublishedPrefixes[nodeID])
	}

	return out
}

func buildWinnerIndex(discovered []store.DiscoveredMapping) map[uint][]string {
	m := make(map[uint][]string)
	for _, d := range discovered {
		if !d.Winner {
			continue
		}
		vip32 := fmt.Sprintf("%s/32", d.VIPIP)
		m[d.RouteID] = append(m[d.RouteID], vip32)
	}
	for rid := range m {
		sort.Strings(m[rid])
	}
	return m
}

func discoveredTargetsByRoute(discovered []store.DiscoveredMapping, routeID uint) []string {
	var targets []string
	type entry struct {
		vip, target string
	}
	var entries []entry
	for _, d := range discovered {
		if d.RouteID == routeID && d.Winner {
			entries = append(entries, entry{fmt.Sprintf("%s/32", d.VIPIP), fmt.Sprintf("%s/32", d.TargetIP)})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].vip < entries[j].vip })
	for _, e := range entries {
		targets = append(targets, e.target)
	}
	return targets
}

func buildDNSConfig(s *store.DNSSettings, records []*genv1.DNSRecord) *genv1.DNSConfig {
	cfg := &genv1.DNSConfig{
		Enabled:       s.Enabled,
		InterceptMode: s.InterceptMode,
		ListenAddr:    s.ListenAddr,
		ListenPort:    s.ListenPort,
		Records:       records,
	}
	if s.ZonesJSON != "" {
		if err := json.Unmarshal([]byte(s.ZonesJSON), &cfg.Zones); err != nil {
			slog.Warn("routepolicy: DNS zones JSON 解析失败", "err", err)
		}
	}
	if s.UpstreamsJSON != "" {
		if err := json.Unmarshal([]byte(s.UpstreamsJSON), &cfg.Upstreams); err != nil {
			slog.Warn("routepolicy: DNS upstreams JSON 解析失败", "err", err)
		}
	}
	if s.LANIfacesJSON != "" {
		if err := json.Unmarshal([]byte(s.LANIfacesJSON), &cfg.LanInterfaces); err != nil {
			slog.Warn("routepolicy: DNS LAN interfaces JSON 解析失败", "err", err)
		}
	}
	if s.LANCIDRsJSON != "" {
		if err := json.Unmarshal([]byte(s.LANCIDRsJSON), &cfg.LanCidrs); err != nil {
			slog.Warn("routepolicy: DNS LAN CIDRs JSON 解析失败", "err", err)
		}
	}
	return cfg
}

func defaultDiscoveryMode(mode string) string {
	if mode == "" {
		return "arp"
	}
	return mode
}
