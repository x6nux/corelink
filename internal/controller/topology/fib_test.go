package topology

import (
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// nodeVIPs 测试用 VIP 映射。
var testVIPs = map[string]string{
	"A": "10.0.0.1",
	"B": "10.0.0.2",
}

// TestComputeFIB_SinglePath 单路径：A→relay-0→B，A 的 FIB 应包含 B 的 VIP 且 next-hop 为 relay-0。
func TestComputeFIB_SinglePath(t *testing.T) {
	r := &Result{
		Version: 1,
		Baseline: map[RoutePair][][]Hop{
			{Src: "A", Dst: "B"}: {
				// 一条路由：A → relay-0（ingress: r0-i）→ B（ingress: b-i）
				{
					{Node: "relay-0", Ingress: "r0-i"},
					{Node: "B", Ingress: "b-i"},
				},
			},
		},
	}

	tables := computeFIB(r, testVIPs, 42)

	// A 应有 FIB 表。
	tblA, ok := tables["A"]
	if !ok {
		t.Fatal("A 应有 FIB 表")
	}
	if tblA.Version != 42 {
		t.Fatalf("版本号应为 42，实际 %d", tblA.Version)
	}
	if len(tblA.Entries) != 1 {
		t.Fatalf("A 应有 1 条 FIB 条目，实际 %d", len(tblA.Entries))
	}

	entry := tblA.Entries[0]
	if entry.Prefix != "10.0.0.2/32" {
		t.Fatalf("前缀应为 10.0.0.2/32，实际 %q", entry.Prefix)
	}
	if len(entry.NextHops) != 1 {
		t.Fatalf("应有 1 个 next-hop，实际 %d", len(entry.NextHops))
	}
	nh := entry.NextHops[0]
	if nh.PeerId != "relay-0" {
		t.Fatalf("next-hop peerID 应为 relay-0，实际 %q", nh.PeerId)
	}
	if nh.IngressId != "r0-i" {
		t.Fatalf("next-hop ingressID 应为 r0-i，实际 %q", nh.IngressId)
	}
	if nh.Weight != 100 {
		t.Fatalf("权重应为 100，实际 %d", nh.Weight)
	}
}

// TestComputeFIB_ECMP 多路径 ECMP：A→{relay-0, relay-1}→B，A 的 FIB 应包含 2 个 next-hop。
func TestComputeFIB_ECMP(t *testing.T) {
	r := &Result{
		Version: 2,
		Baseline: map[RoutePair][][]Hop{
			{Src: "A", Dst: "B"}: {
				// 路由 1：A → relay-0 → B
				{
					{Node: "relay-0", Ingress: "r0-i"},
					{Node: "B", Ingress: "b-i"},
				},
				// 路由 2：A → relay-1 → B
				{
					{Node: "relay-1", Ingress: "r1-i"},
					{Node: "B", Ingress: "b-i"},
				},
			},
		},
	}

	tables := computeFIB(r, testVIPs, 99)

	tblA, ok := tables["A"]
	if !ok {
		t.Fatal("A 应有 FIB 表")
	}
	if len(tblA.Entries) != 1 {
		t.Fatalf("A 应有 1 条 FIB 条目，实际 %d", len(tblA.Entries))
	}

	entry := tblA.Entries[0]
	if entry.Prefix != "10.0.0.2/32" {
		t.Fatalf("前缀应为 10.0.0.2/32，实际 %q", entry.Prefix)
	}
	if len(entry.NextHops) != 2 {
		t.Fatalf("应有 2 个 next-hop，实际 %d", len(entry.NextHops))
	}

	// 验证两个 next-hop（按 peerID 排序后）。
	nhMap := make(map[string]*genv1.NextHopEntry, 2)
	for _, nh := range entry.NextHops {
		nhMap[nh.PeerId] = nh
	}
	if _, ok := nhMap["relay-0"]; !ok {
		t.Fatal("缺少 relay-0 next-hop")
	}
	if _, ok := nhMap["relay-1"]; !ok {
		t.Fatal("缺少 relay-1 next-hop")
	}
	for _, nh := range entry.NextHops {
		if nh.Weight != 100 {
			t.Fatalf("next-hop %s 权重应为 100，实际 %d", nh.PeerId, nh.Weight)
		}
	}
}

// TestComputeFIB_CycleRejected 环路被修剪：relay-0→relay-1→relay-0 的环路应被 pruneGraphCycles 消除。
func TestComputeFIB_CycleRejected(t *testing.T) {
	// 构造包含环路的 Baseline：
	//   relay-0→relay-1 路由的第一跳是 relay-1
	//   relay-1→relay-0 路由的第一跳是 relay-0
	// 这形成 relay-0→relay-1→relay-0 的环。
	vips := map[string]string{
		"relay-0": "10.0.0.10",
		"relay-1": "10.0.0.11",
	}
	r := &Result{
		Version: 3,
		Baseline: map[RoutePair][][]Hop{
			{Src: "relay-0", Dst: "relay-1"}: {
				{{Node: "relay-1", Ingress: "r1-i"}},
			},
			{Src: "relay-1", Dst: "relay-0"}: {
				{{Node: "relay-0", Ingress: "r0-i"}},
			},
		},
	}

	tables := computeFIB(r, vips, 7)

	// 环路被修剪后，至少有一个方向的路由被删除（环路不可能双向都保留）。
	// pruneGraphCycles 每次删除一条 back edge，直到 DAG 合法。
	totalEntries := 0
	for _, tbl := range tables {
		totalEntries += len(tbl.Entries)
	}
	// 原有 2 条条目（双向各 1），修剪后最多 1 条（单向保留）。
	if totalEntries > 1 {
		t.Fatalf("环路修剪后应最多保留 1 条条目，实际 %d", totalEntries)
	}
}

// TestComputeFIB_MissingVIP 目标节点无 VIP 映射时应被跳过。
func TestComputeFIB_MissingVIP(t *testing.T) {
	r := &Result{
		Version: 4,
		Baseline: map[RoutePair][][]Hop{
			{Src: "A", Dst: "C"}: { // C 不在 VIP 映射中
				{{Node: "relay-0", Ingress: "r0-i"}, {Node: "C", Ingress: "c-i"}},
			},
		},
	}

	tables := computeFIB(r, testVIPs, 10)

	// A 不应有任何 FIB 条目（因为 C 无 VIP）。
	if tblA, ok := tables["A"]; ok && len(tblA.Entries) > 0 {
		t.Fatalf("目标无 VIP 的路由应被跳过，但 A 有 %d 条条目", len(tblA.Entries))
	}
}

// TestComputeFIB_EntriesSortedByPrefix 验证条目按 prefix 字典序排列。
func TestComputeFIB_EntriesSortedByPrefix(t *testing.T) {
	vips := map[string]string{
		"A": "10.0.0.1",
		"B": "10.0.0.2",
		"C": "10.0.0.3",
	}
	r := &Result{
		Version: 5,
		Baseline: map[RoutePair][][]Hop{
			{Src: "A", Dst: "C"}: {
				{{Node: "relay-0", Ingress: "r0-i"}, {Node: "C", Ingress: "c-i"}},
			},
			{Src: "A", Dst: "B"}: {
				{{Node: "relay-0", Ingress: "r0-i"}, {Node: "B", Ingress: "b-i"}},
			},
		},
	}

	tables := computeFIB(r, vips, 20)

	tblA, ok := tables["A"]
	if !ok {
		t.Fatal("A 应有 FIB 表")
	}
	if len(tblA.Entries) != 2 {
		t.Fatalf("A 应有 2 条条目，实际 %d", len(tblA.Entries))
	}
	// B 的 VIP 10.0.0.2/32 < C 的 VIP 10.0.0.3/32，应排在前面。
	if tblA.Entries[0].Prefix != "10.0.0.2/32" {
		t.Fatalf("第一条应为 10.0.0.2/32，实际 %q", tblA.Entries[0].Prefix)
	}
	if tblA.Entries[1].Prefix != "10.0.0.3/32" {
		t.Fatalf("第二条应为 10.0.0.3/32，实际 %q", tblA.Entries[1].Prefix)
	}
}

// TestComputeFIB_DedupNextHops 同一 (src, dst) 对多条路由经同一 relay 时，next-hop 应去重。
func TestComputeFIB_DedupNextHops(t *testing.T) {
	r := &Result{
		Version: 6,
		Baseline: map[RoutePair][][]Hop{
			{Src: "A", Dst: "B"}: {
				// 两条路由都经 relay-0，但入口不同。
				{
					{Node: "relay-0", Ingress: "r0-i1"},
					{Node: "B", Ingress: "b-i"},
				},
				{
					{Node: "relay-0", Ingress: "r0-i2"},
					{Node: "B", Ingress: "b-i"},
				},
			},
		},
	}

	tables := computeFIB(r, testVIPs, 30)

	tblA := tables["A"]
	if tblA == nil {
		t.Fatal("A 应有 FIB 表")
	}
	if len(tblA.Entries) != 1 {
		t.Fatalf("应有 1 条条目，实际 %d", len(tblA.Entries))
	}
	// 两条路由的第一跳 peerID 相同（relay-0），但 ingress 不同，应保留为 2 个 next-hop。
	if len(tblA.Entries[0].NextHops) != 2 {
		t.Fatalf("同 peer 不同 ingress 应保留 2 个 next-hop，实际 %d", len(tblA.Entries[0].NextHops))
	}
}

func TestInjectPublishedPrefixes(t *testing.T) {
	r := &Result{
		Version: 1,
		Baseline: map[RoutePair][][]Hop{
			{Src: "A", Dst: "B"}: {
				{{Node: "relay-0", Ingress: "r0-i"}, {Node: "B", Ingress: "b-i"}},
			},
			{Src: "B", Dst: "A"}: {
				{{Node: "relay-0", Ingress: "r0-i"}, {Node: "A", Ingress: "a-i"}},
			},
		},
	}
	tables := computeFIB(r, testVIPs, 1)

	// B 发布了两个前缀
	published := map[string][]string{
		"B": {"10.0.2.0/24", "100.64.2.0/24"},
	}
	InjectPublishedPrefixes(tables, published, testVIPs)

	tblA := tables["A"]
	if tblA == nil {
		t.Fatal("A 的 FIB 表不应为空")
	}
	// A 应有：B 的 VIP/32 + 2 个 published prefixes = 3 条
	if len(tblA.Entries) != 3 {
		t.Fatalf("A 的 FIB 应有 3 条，实际 %d", len(tblA.Entries))
	}

	// 检查 published prefix 的 next-hop 与 B 的 VIP/32 一致
	var bVIPEntry, pubEntry *genv1.FIBEntry
	for _, e := range tblA.Entries {
		if e.Prefix == "10.0.0.2/32" {
			bVIPEntry = e
		}
		if e.Prefix == "10.0.2.0/24" {
			pubEntry = e
		}
	}
	if bVIPEntry == nil || pubEntry == nil {
		t.Fatal("应存在 B 的 VIP 条目和 published prefix 条目")
	}
	if len(pubEntry.NextHops) != len(bVIPEntry.NextHops) {
		t.Fatalf("published prefix next-hop 应与 VIP 一致: %d vs %d", len(pubEntry.NextHops), len(bVIPEntry.NextHops))
	}

	// B 的 FIB 不应包含自己的 published prefixes
	tblB := tables["B"]
	if tblB == nil {
		t.Fatal("B 的 FIB 表不应为空")
	}
	for _, e := range tblB.Entries {
		if e.Prefix == "10.0.2.0/24" || e.Prefix == "100.64.2.0/24" {
			t.Fatalf("B 的 FIB 不应包含自己的 published prefix: %s", e.Prefix)
		}
	}
}
