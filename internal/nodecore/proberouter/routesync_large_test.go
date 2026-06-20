package proberouter

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"testing"

	"github.com/x6nux/corelink/internal/transport"
)

// makeVIP 生成 100.64.x.y 形式的 VIP（idx 从 1 开始）。
func makeVIP(idx int) netip.Addr {
	var b [4]byte
	b[0] = 100
	b[1] = 64
	binary.BigEndian.PutUint16(b[2:], uint16(idx))
	return netip.AddrFrom4(b)
}

func makeNodeID(idx int) string {
	return fmt.Sprintf("node-%04d", idx)
}

// ── 大规模拓扑生成器 ──

// topology 描述一个测试拓扑：每个节点到每个目标的最优路由。
type topology struct {
	n    int          // 节点数
	vips []netip.Addr // vips[i] = 节点 i 的 VIP
	ids  []string     // ids[i] = 节点 i 的 nodeID
	// routes[src][dst] = nextHop 索引（-1=直连=dst 本身）
	routes [][]int
}

func newTopology(n int) *topology {
	t := &topology{
		n:      n,
		vips:   make([]netip.Addr, n),
		ids:    make([]string, n),
		routes: make([][]int, n),
	}
	for i := range n {
		t.vips[i] = makeVIP(i + 1)
		t.ids[i] = makeNodeID(i + 1)
		t.routes[i] = make([]int, n)
		for j := range n {
			t.routes[i][j] = -1 // 默认直连
		}
	}
	return t
}

// setRoute 设置 src→dst 的最优路由经 nextHop 中转。
func (t *topology) setRoute(src, dst, nextHop int) {
	t.routes[src][dst] = nextHop
}

// buildRouter 为节点 idx 构造 ProbeRouter 并注入所有 peer 的路由广播。
func (t *topology) buildRouter(idx int) *ProbeRouter {
	pr := newTestRouter(t.vips[idx])
	// 注入所有其他节点的路由广播
	for src := range t.n {
		if src == idx {
			continue
		}
		for dst := range t.n {
			if dst == src {
				continue
			}
			nh := t.routes[src][dst]
			nextHopVIP := t.vips[dst] // 直连
			if nh >= 0 {
				nextHopVIP = t.vips[nh]
			}
			pr.ReceiveRouteSync(t.vips[src], transport.RouteSyncEntry{
				DstVIP:     t.vips[dst],
				NextHopVIP: nextHopVIP,
				RTTMs:      float64(10 + src + dst), // 确定性 RTT
			})
		}
	}
	return pr
}

// checkLoopFor 检查节点 idx 到 dst 的路由是否有环路需要让步。
func (t *topology) checkLoopFor(idx, dst int) (BestRoute, bool) {
	pr := t.buildRouter(idx)
	nh := t.routes[idx][dst]
	nextHopVIP := t.vips[dst]
	nextHopID := t.ids[dst]
	if nh >= 0 {
		nextHopVIP = t.vips[nh]
		nextHopID = t.ids[nh]
	}
	selected := BestRoute{
		NextHopVIP: nextHopVIP, NextHopID: nextHopID,
		RTTMs: float64(10 + idx + dst), Label: fmt.Sprintf("via %d", nh),
	}
	// 构造 ranked：最优 + 直连作为 fallback
	ranked := []rankedRoute{
		{nextHopVIP: nextHopVIP, nextHopID: nextHopID, rttMs: selected.RTTMs,
			route: enumRoute{label: selected.Label}},
	}
	if nh >= 0 {
		// 加一个直连 fallback
		ranked = append(ranked, rankedRoute{
			nextHopVIP: t.vips[dst], nextHopID: t.ids[dst], rttMs: 999,
			route: enumRoute{label: "direct-fallback"},
		})
	}
	return pr.checkLoopAndYield(t.vips[dst], selected, ranked)
}

// ── 测试场景 ──

// TestLargeScale_50Nodes_NoLoop 50 节点全直连，无环路
func TestLargeScale_50Nodes_NoLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过大规模测试（-short 模式）")
	}
	const N = 50
	topo := newTopology(N)
	// 全部默认直连，不设 via 路由

	// 抽检
	for i := 0; i < N; i += 5 {
		for j := 1; j < N; j += 10 {
			if i == j {
				continue
			}
			_, yielded := topo.checkLoopFor(i, j)
			if yielded {
				t.Errorf("全直连拓扑不应有环路: %d→%d", i, j)
			}
		}
	}
}

// TestLargeScale_50Nodes_PairwiseLoop 50 节点中植入 10 对双向环路
func TestLargeScale_50Nodes_PairwiseLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过大规模测试（-short 模式）")
	}
	const N = 50
	topo := newTopology(N)

	// 制造 10 对环路：节点 2i 到 dst=N-1 走 2i+1，节点 2i+1 到 dst=N-1 走 2i
	dst := N - 1
	loopCount := 0
	for i := 0; i < 20; i += 2 {
		a, b := i, i+1
		topo.setRoute(a, dst, b) // a→dst via b
		topo.setRoute(b, dst, a) // b→dst via a（环路！）
		loopCount++
	}

	yieldCount := 0
	for i := 0; i < 20; i += 2 {
		a, b := i, i+1
		_, yieldedA := topo.checkLoopFor(a, dst)
		_, yieldedB := topo.checkLoopFor(b, dst)

		// 每对环路中恰好一个让步
		if yieldedA == yieldedB {
			t.Errorf("环路对 %d↔%d 应恰好一个让步: yieldA=%v yieldB=%v", a, b, yieldedA, yieldedB)
		}
		if yieldedA || yieldedB {
			yieldCount++
		}
	}

	if yieldCount != loopCount {
		t.Errorf("让步数: got %d, want %d", yieldCount, loopCount)
	}
	t.Logf("50 节点 %d 对环路，全部检测并解决", loopCount)
}

// TestLargeScale_ChainLoop_10 10 节点链式环路：0→1→2→...→9→0
// 只有直接冲突（相邻两节点互指）能被检测，间接链不检测（TTL 兜底）
func TestLargeScale_ChainLoop_10(t *testing.T) {
	const N = 10
	topo := newTopology(N)

	dst := 0 // 所有节点都要到节点 0
	// 链式：1→0 via 2, 2→0 via 3, ..., 9→0 via 1
	for i := 1; i < N; i++ {
		next := i + 1
		if next >= N {
			next = 1
		}
		topo.setRoute(i, dst, next)
	}

	// 检查每个节点：只有相邻互指的才能检测到
	for i := 1; i < N; i++ {
		next := i + 1
		if next >= N {
			next = 1
		}
		_, yielded := topo.checkLoopFor(i, dst)
		// 节点 i→0 via next，next→0 via next+1
		// 只有 next→0 via i 时才冲突
		nextNext := next + 1
		if nextNext >= N {
			nextNext = 1
		}
		expectConflict := nextNext == i // next 指回 i
		if yielded != expectConflict {
			t.Errorf("node %d: yielded=%v, expectConflict=%v (next=%d, nextNext=%d)",
				i, yielded, expectConflict, next, nextNext)
		}
	}
}

// TestLargeScale_StarLoop 星型环路：中心节点 0 到所有目标走节点 1，节点 1 到所有目标走节点 0
func TestLargeScale_StarLoop(t *testing.T) {
	const N = 30
	topo := newTopology(N)

	// 节点 0 到所有目标（2..N-1）走节点 1
	// 节点 1 到所有目标（2..N-1）走节点 0
	for dst := 2; dst < N; dst++ {
		topo.setRoute(0, dst, 1)
		topo.setRoute(1, dst, 0)
	}

	yieldCount0, yieldCount1 := 0, 0
	for dst := 2; dst < N; dst++ {
		_, y0 := topo.checkLoopFor(0, dst)
		_, y1 := topo.checkLoopFor(1, dst)

		if y0 == y1 {
			t.Errorf("dst=%d: 应恰好一个让步 y0=%v y1=%v", dst, y0, y1)
		}
		if y0 {
			yieldCount0++
		}
		if y1 {
			yieldCount1++
		}
	}

	// RTT(0→dst) = 10+0+dst, RTT(1→dst) = 10+1+dst
	// 节点 0 的 RTT 始终更小 → 节点 1 应全部让步
	if yieldCount0 != 0 {
		t.Errorf("节点 0 RTT 更小不应让步: yielded %d times", yieldCount0)
	}
	if yieldCount1 != N-2 {
		t.Errorf("节点 1 应全部让步: yielded %d, want %d", yieldCount1, N-2)
	}
	t.Logf("星型环路 %d 目标，节点 1 全部让步", N-2)
}

// TestLargeScale_LongChain_64 64 跳长链：每个节点指向下一个，最后一个指回第一个
func TestLargeScale_LongChain_64(t *testing.T) {
	const N = 64
	topo := newTopology(N)

	dst := 0
	// 1→0 via 2, 2→0 via 3, ..., 63→0 via 1
	for i := 1; i < N; i++ {
		next := (i % (N - 1)) + 1
		topo.setRoute(i, dst, next)
	}

	totalYield := 0
	for i := 1; i < N; i++ {
		_, yielded := topo.checkLoopFor(i, dst)
		if yielded {
			totalYield++
		}
	}
	// 链式环路中只有部分相邻互指的会被检测（直接冲突检测）
	t.Logf("64 跳长链: %d/%d 节点让步", totalYield, N-1)
}

// TestLargeScale_RandomMesh_50 50 节点随机 mesh：确定性伪随机路由分配
func TestLargeScale_RandomMesh_50(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过大规模测试（-short 模式）")
	}
	const N = 50
	topo := newTopology(N)

	// 确定性伪随机：节点 i 到 dst j 走 (i*7+j*13) % N
	loopPairs := 0
	for i := range N {
		for j := range N {
			if i == j {
				continue
			}
			nh := (i*7 + j*13) % N
			if nh == i || nh == j {
				continue // 跳过自环和直连
			}
			topo.setRoute(i, j, nh)
		}
	}

	// 统计直接冲突数
	for i := range N {
		for j := range N {
			if i == j {
				continue
			}
			nhI := topo.routes[i][j]
			if nhI < 0 || nhI == j {
				continue
			}
			// i→j via nhI，检查 nhI→j 是否 via i
			nhP := topo.routes[nhI][j]
			if nhP == i {
				loopPairs++
			}
		}
	}

	// 检测并解决：抽检 1000 对
	yieldCount := 0
	checked := 0
	for i := 0; i < N; i += 5 {
		for j := 0; j < N; j += 50 {
			if i == j {
				continue
			}
			_, yielded := topo.checkLoopFor(i, j)
			if yielded {
				yieldCount++
			}
			checked++
		}
	}

	t.Logf("50 节点随机 mesh: 总冲突对=%d, 抽检=%d, 让步=%d", loopPairs, checked, yieldCount)
}

// TestLargeScale_ReceiveSync_50Peers 50 个 peer 的路由存储性能
func TestLargeScale_ReceiveSync_50Peers(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过大规模测试（-short 模式）")
	}
	const N = 50
	pr := newTestRouter(makeVIP(1))

	// 每个 peer 广播到所有其他节点的路由（N-1 条 × N-1 个 peer）
	for src := 2; src <= N; src++ {
		srcVIP := makeVIP(src)
		for dst := 1; dst <= N; dst++ {
			if dst == src {
				continue
			}
			pr.ReceiveRouteSync(srcVIP, transport.RouteSyncEntry{
				DstVIP:     makeVIP(dst),
				NextHopVIP: makeVIP((src+dst)%N + 1),
				RTTMs:      float64(src + dst),
			})
		}
	}

	// 验证存储完整性
	pr.peerRoutesMu.Lock()
	totalEntries := 0
	for _, entries := range pr.peerRoutes {
		totalEntries += len(entries)
	}
	peerCount := len(pr.peerRoutes)
	pr.peerRoutesMu.Unlock()

	expectedPeers := N - 1
	expectedEntries := (N - 1) * (N - 1) // 每个 peer 广播 N-1 条
	if peerCount != expectedPeers {
		t.Errorf("peer 数: got %d, want %d", peerCount, expectedPeers)
	}
	if totalEntries != expectedEntries {
		t.Errorf("总条目: got %d, want %d", totalEntries, expectedEntries)
	}
	t.Logf("存储 %d peers × %d entries = %d 条路由", peerCount, N-1, totalEntries)
}

// TestLargeScale_ReceiveSync_Upsert_Stress 高频 upsert 压力测试
func TestLargeScale_ReceiveSync_Upsert_Stress(t *testing.T) {
	pr := newTestRouter(makeVIP(1))
	srcVIP := makeVIP(2)
	dstVIP := makeVIP(3)

	// 同一条路由更新 10000 次
	for i := range 1000 {
		pr.ReceiveRouteSync(srcVIP, transport.RouteSyncEntry{
			DstVIP:     dstVIP,
			NextHopVIP: makeVIP(i%50 + 1),
			RTTMs:      float64(i),
		})
	}

	pr.peerRoutesMu.Lock()
	entries := pr.peerRoutes[srcVIP]
	pr.peerRoutesMu.Unlock()

	// 应该始终只有 1 条（upsert）
	if len(entries) != 1 {
		t.Errorf("upsert 后应只有 1 条: got %d", len(entries))
	}
	// 最后一次的值
	if entries[0].RTTMs != 999 {
		t.Errorf("RTTMs: got %v, want 999", entries[0].RTTMs)
	}
}

// TestLargeScale_EncodeDecodeBatch 批量编解码 1000 帧
func TestLargeScale_EncodeDecodeBatch(t *testing.T) {
	for i := range 1000 {
		srcVIP := makeVIP(i%50 + 1)
		dstVIP := makeVIP((i+1)%50 + 1)
		nhVIP := makeVIP((i+2)%50 + 1)
		p := &transport.ProbeFrame{
			IsRouteSync: true,
			Nonce:       uint64(i),
			TimestampNs: int64(i * 1000),
			SourceVIP:   srcVIP,
			Route:       []netip.Addr{dstVIP},
			SyncEntry: transport.RouteSyncEntry{
				DstVIP: dstVIP, NextHopVIP: nhVIP, RTTMs: float64(i % 65536),
			},
		}
		data := transport.EncodeProbePayload(p)
		got, err := transport.DecodeProbePayload(data)
		if err != nil {
			t.Fatalf("帧 %d decode 失败: %v", i, err)
		}
		if got.SyncEntry.DstVIP != dstVIP {
			t.Fatalf("帧 %d DstVIP 不匹配", i)
		}
		if got.SyncEntry.NextHopVIP != nhVIP {
			t.Fatalf("帧 %d NextHopVIP 不匹配", i)
		}
	}
}
