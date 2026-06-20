package ingress

import (
	"fmt"
	"sort"
	"strings"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// sourcePriority 定义 confidence 相等时的来源优先级（值越大越优先）。
// 顺序：NETIF > OBSERVED > UPNP > STUN > CONFIG > URL。
//   - NETIF：本机真实持有的地址，最可信；
//   - OBSERVED：controller 实际观察到的源地址；
//   - UPNP：自助打洞主动建立，可达性较好；
//   - STUN：反射地址，需 keepalive 维持；
//   - CONFIG：静态声明，可能滞后；
//   - URL：仅确认出口 IP、无端口，最弱。
func sourcePriority(s genv1.IngressSource) int {
	switch s {
	case genv1.IngressSource_INGRESS_SOURCE_NETIF:
		return 6
	case genv1.IngressSource_INGRESS_SOURCE_OBSERVED:
		return 5
	case genv1.IngressSource_INGRESS_SOURCE_UPNP:
		return 4
	case genv1.IngressSource_INGRESS_SOURCE_STUN:
		return 3
	case genv1.IngressSource_INGRESS_SOURCE_CONFIG:
		return 2
	case genv1.IngressSource_INGRESS_SOURCE_URL:
		return 1
	default:
		return 0
	}
}

// dedupKey 去重键 = (host, port, kind)。
//   - kind 纳入键，确保 CDN（hostname）入口不与同 host 的直连 IP 入口合并。
//   - 同 host:port:kind 但不同 source 视为同一物理入口的重复观测。
type dedupKey struct {
	host string
	port uint32
	kind genv1.IngressKind
}

// betterThan 报告候选 cand 是否应取代当前 cur（用于冲突解决）。
// 先比 confidence（高者胜），相等时比 source 优先级。
func betterThan(cand, cur *genv1.Ingress) bool {
	if cand.GetConfidence() != cur.GetConfidence() {
		return cand.GetConfidence() > cur.GetConfidence()
	}
	return sourcePriority(cand.GetSource()) > sourcePriority(cur.GetSource())
}

// Merge 对候选入口去重 + 冲突解决，输出确定性排序的结果。
//
//   - 去重键 = (host, port, kind)；CDN 入口因 kind 不同而独立保留。
//   - 同键冲突取 confidence 最高者（保留其 source/nat_type 等全部字段）；
//     confidence 相等时按 source 优先级（NETIF>OBSERVED>UPNP>STUN>CONFIG>URL）确定性选取。
//   - 结果按 (confidence desc, host asc, port asc, kind asc) 稳定排序，便于 golden 测试。
//
// nil 条目被跳过。输入为空时返回空切片（非 nil 调用方无需判空）。
func Merge(ingresses []*genv1.Ingress) []*genv1.Ingress {
	best := make(map[dedupKey]*genv1.Ingress, len(ingresses))
	for _, ing := range ingresses {
		if ing == nil {
			continue
		}
		key := dedupKey{host: ing.GetHost(), port: ing.GetPort(), kind: ing.GetKind()}
		cur, ok := best[key]
		if !ok || betterThan(ing, cur) {
			best[key] = ing
		}
	}

	out := make([]*genv1.Ingress, 0, len(best))
	for _, ing := range best {
		// 确保每个入口有唯一 Id（空时按 source-host-port-kind 生成确定性 Id）。
		if ing.GetId() == "" {
			ing.Id = fmt.Sprintf("%s-%s-%d-%s",
				strings.ToLower(ing.GetSource().String()),
				ing.GetHost(), ing.GetPort(),
				strings.ToLower(ing.GetKind().String()))
		}
		out = append(out, ing)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.GetConfidence() != b.GetConfidence() {
			return a.GetConfidence() > b.GetConfidence() // confidence 降序
		}
		if a.GetHost() != b.GetHost() {
			return a.GetHost() < b.GetHost()
		}
		if a.GetPort() != b.GetPort() {
			return a.GetPort() < b.GetPort()
		}
		return a.GetKind() < b.GetKind()
	})

	return out
}
