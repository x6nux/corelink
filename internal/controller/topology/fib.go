package topology

import (
	"fmt"
	"slices"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/protobuf/proto"
)

// fib.go: 拓扑优化结果 → per-node FIB 表转换。
//
// computeFIB 将 Result.Baseline（per-pair K 基准路由）转换为每节点的 FIB 表（proto）。
// 流程：
//   1. 遍历 Baseline，按源节点分组，每条路由取第一跳作为 next-hop
//   2. 构建 per-source FIB 中间表（fibRouteEntry）
//   3. 调用 buildForwardingGraph + validateDAG 检测环路
//   4. 若有环路，调用 pruneGraphCycles 修剪
//   5. 转换为 proto FIB 表，条目按 prefix 排序保证确定性

// computeFIB 将拓扑优化结果转换为 per-node FIB 表。
//
//   - r: 拓扑优化结果（主要使用 Baseline 字段）
//   - nodeVIPs: 节点 ID → VIP 地址映射
//   - version: FIB 表版本号
//
// 返回 map[nodeID]*genv1.FIBTable。
func computeFIB(r *Result, nodeVIPs map[string]string, version uint64) map[string]*genv1.FIBTable {
	// 步骤 1：遍历 Baseline，按 src 分组构建 per-source FIB 中间表。
	perSrc := buildPerSourceFIB(r.Baseline, nodeVIPs)

	// 步骤 2：构建转发图并验证 DAG。
	graph := buildForwardingGraph(perSrc)
	if err := validateDAG(graph); err != nil {
		// 存在环路，修剪直到 DAG 合法。
		pruneGraphCycles(perSrc, graph)
	}

	// 步骤 3：转换为 proto FIB 表。
	return convertToProto(perSrc, version)
}

// buildPerSourceFIB 从 Baseline 构建 per-source FIB 中间表。
// 每条路由取第一跳（Hop[0]）的 Node 作为 peerID、Ingress 作为 ingressID。
// 目标节点的 VIP + "/32" 作为 prefix。
func buildPerSourceFIB(baseline map[RoutePair][][]Hop, nodeVIPs map[string]string) map[string][]fibRouteEntry {
	// 中间结构：perSrc[src][dstVIP] → []fibNextHop（聚合同一 prefix 的多个 next-hop）。
	type nhKey struct {
		peerID    string
		ingressID string
	}
	type dstAgg struct {
		prefix string
		seen   map[nhKey]struct{}
		hops   []fibNextHop
	}

	perSrcAgg := make(map[string]map[string]*dstAgg) // src → dstVIP → dstAgg

	for rp, routes := range baseline {
		dstVIP, ok := nodeVIPs[rp.Dst]
		if !ok {
			// 目标节点无 VIP 映射，跳过。
			continue
		}
		prefix := fmt.Sprintf("%s/32", dstVIP)

		for _, hops := range routes {
			if len(hops) == 0 {
				continue
			}
			firstHop := hops[0]
			key := nhKey{peerID: firstHop.Node, ingressID: firstHop.Ingress}

			if perSrcAgg[rp.Src] == nil {
				perSrcAgg[rp.Src] = make(map[string]*dstAgg)
			}
			agg := perSrcAgg[rp.Src][prefix]
			if agg == nil {
				agg = &dstAgg{
					prefix: prefix,
					seen:   make(map[nhKey]struct{}),
				}
				perSrcAgg[rp.Src][prefix] = agg
			}

			// (peerID, ingressID) 组合去重。
			if _, dup := agg.seen[key]; !dup {
				agg.seen[key] = struct{}{}
				agg.hops = append(agg.hops, fibNextHop{
					peerID:    firstHop.Node,
					weight:    100, // 固定权重，后续由质量矩阵驱动。
					ingressID: firstHop.Ingress,
				})
			}
		}
	}

	// 转换为 []fibRouteEntry。
	perSrc := make(map[string][]fibRouteEntry, len(perSrcAgg))
	for src, dstMap := range perSrcAgg {
		entries := make([]fibRouteEntry, 0, len(dstMap))
		for _, agg := range dstMap {
			// next-hop 按 (peerID, ingressID) 排序，保证确定性。
			slices.SortFunc(agg.hops, func(a, b fibNextHop) int {
				if a.peerID != b.peerID {
					if a.peerID < b.peerID {
						return -1
					}
					return 1
				}
				if a.ingressID < b.ingressID {
					return -1
				}
				if a.ingressID > b.ingressID {
					return 1
				}
				return 0
			})
			entries = append(entries, fibRouteEntry{
				prefix:   agg.prefix,
				nextHops: agg.hops,
			})
		}
		perSrc[src] = entries
	}

	return perSrc
}

// convertToProto 将 per-source FIB 中间表转换为 proto FIB 表。
// 每个 src 对应一个 FIBTable，条目按 prefix 字典序排列。
func convertToProto(perSrc map[string][]fibRouteEntry, version uint64) map[string]*genv1.FIBTable {
	result := make(map[string]*genv1.FIBTable, len(perSrc))

	for src, entries := range perSrc {
		// 按 prefix 排序（确定性输出）。
		slices.SortFunc(entries, func(a, b fibRouteEntry) int {
			if a.prefix < b.prefix {
				return -1
			}
			if a.prefix > b.prefix {
				return 1
			}
			return 0
		})

		protoEntries := make([]*genv1.FIBEntry, 0, len(entries))
		for _, entry := range entries {
			nextHops := make([]*genv1.NextHopEntry, 0, len(entry.nextHops))
			for _, nh := range entry.nextHops {
				nextHops = append(nextHops, &genv1.NextHopEntry{
					PeerId:    nh.peerID,
					Weight:    nh.weight,
					IngressId: nh.ingressID,
				})
			}
			protoEntries = append(protoEntries, &genv1.FIBEntry{
				Prefix:   entry.prefix,
				NextHops: nextHops,
			})
		}

		result[src] = &genv1.FIBTable{
			Version: version,
			Entries: protoEntries,
		}
	}

	return result
}

// InjectPublishedPrefixes 将 published prefixes 注入已有 FIB 表。
// 对每个 src 节点，如果该 src 有到 owner VIP/32 的 FIB 条目，
// 则为每个 published prefix 复制相同的 next-hop（piggyback 策略）。
func InjectPublishedPrefixes(fibs map[string]*genv1.FIBTable, publishedPrefixes map[string][]string, nodeVIPs map[string]string) {
	if len(publishedPrefixes) == 0 {
		return
	}

	for src, fib := range fibs {
		// 建立 prefix → FIBEntry 索引
		entryByPrefix := make(map[string]*genv1.FIBEntry, len(fib.Entries))
		for _, e := range fib.Entries {
			entryByPrefix[e.Prefix] = e
		}

		for ownerID, prefixes := range publishedPrefixes {
			if ownerID == src {
				continue // 不给自己注入自己的 published prefixes
			}
			ownerVIP, ok := nodeVIPs[ownerID]
			if !ok {
				continue
			}
			ownerPrefix := fmt.Sprintf("%s/32", ownerVIP)
			ownerEntry, ok := entryByPrefix[ownerPrefix]
			if !ok {
				continue
			}
			for _, prefix := range prefixes {
				if _, exists := entryByPrefix[prefix]; exists {
					continue // 已存在，跳过
				}
				// 深拷贝 NextHops，避免多 FIBEntry 共享同一 slice 导致交叉污染。
				nhs := make([]*genv1.NextHopEntry, len(ownerEntry.NextHops))
				for j, nh := range ownerEntry.NextHops {
					nhs[j] = proto.Clone(nh).(*genv1.NextHopEntry)
				}
				newEntry := &genv1.FIBEntry{
					Prefix:   prefix,
					NextHops: nhs,
				}
				fib.Entries = append(fib.Entries, newEntry)
				entryByPrefix[prefix] = newEntry
			}
		}

		// 重新排序保证确定性
		slices.SortFunc(fib.Entries, func(a, b *genv1.FIBEntry) int {
			if a.Prefix < b.Prefix {
				return -1
			}
			if a.Prefix > b.Prefix {
				return 1
			}
			return 0
		})
	}
}
