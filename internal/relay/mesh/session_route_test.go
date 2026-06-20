package mesh

import (
	"fmt"
	"testing"
	"time"
)

// mkPath 构造一条形如 src→...→dst 的 Hop 序列（仅用于测试，Ingress 取 "in:"+node）。
func mkPath(nodes ...string) []Hop {
	hops := make([]Hop, 0, len(nodes))
	for _, n := range nodes {
		hops = append(hops, Hop{Node: n, Ingress: "in:" + n})
	}
	return hops
}

// pathKeyOf 暴露内部环 key 计算，便于断言"路径内容相同"。
func pathKeyOf(p []Hop) string { return pathRingKey(p) }

func TestSessionRouter_PickDeterministic(t *testing.T) {
	s := NewSessionRouter()
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
	}
	s.SetBaseline(1, base)

	// 同 sessionID 多次 Pick 同一条（无抖动）。
	got, ok := s.Pick("sess-1")
	if !ok {
		t.Fatalf("Pick 应成功")
	}
	for i := 0; i < 20; i++ {
		again, ok := s.Pick("sess-1")
		if !ok || pathRingKey(again) != pathRingKey(got) {
			t.Fatalf("同 session 多次 Pick 不稳定: %v vs %v", again, got)
		}
	}
}

func TestSessionRouter_LoadSpread(t *testing.T) {
	s := NewSessionRouter()
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
	}
	s.SetBaseline(1, base)

	counts := map[string]int{}
	for i := 0; i < 300; i++ {
		p, ok := s.Pick(fmt.Sprintf("sess-%d", i))
		if !ok {
			t.Fatalf("Pick 失败")
		}
		counts[pathRingKey(p)]++
	}
	// 三条路径都应被命中（散布），无空桶。
	if len(counts) != 3 {
		t.Fatalf("会话未散布到全部 3 条路径，命中桶=%d: %v", len(counts), counts)
	}
	for k, c := range counts {
		if c == 0 {
			t.Fatalf("路径 %s 命中 0 次", k)
		}
	}
}

func TestSessionRouter_EmptyBaseline(t *testing.T) {
	s := NewSessionRouter()
	if _, ok := s.Pick("x"); ok {
		t.Fatalf("空 baseline Pick 应返回 false")
	}
	s.SetBaseline(1, nil)
	if _, ok := s.Pick("x"); ok {
		t.Fatalf("nil baseline Pick 应返回 false")
	}
}

// TestSessionRouter_MinimalRemap：baseline 从 3 条变 4 条，断言大多数会话仍映射到
// 内容相同的路径（一致性哈希环特性）。对比 hash%K 会大面积重映射。
func TestSessionRouter_MinimalRemap(t *testing.T) {
	base3 := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
	}
	base4 := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
		mkPath("A", "E", "Z"), // 新增一条
	}

	const N = 1000
	s := NewSessionRouter()
	s.SetBaseline(1, base3)
	before := make(map[string]string, N)
	for i := 0; i < N; i++ {
		sid := fmt.Sprintf("sess-%d", i)
		p, ok := s.Pick(sid)
		if !ok {
			t.Fatalf("Pick 失败")
		}
		before[sid] = pathRingKey(p)
	}

	// 拓扑重算下发新 K 集（4 条）。用新路由器消除粘滞状态影响，纯测环映射。
	s2 := NewSessionRouter()
	s2.SetBaseline(2, base4)
	remapped := 0
	for i := 0; i < N; i++ {
		sid := fmt.Sprintf("sess-%d", i)
		p, _ := s2.Pick(sid)
		if pathRingKey(p) != before[sid] {
			remapped++
		}
	}

	// 一致性哈希环：新增一条路径只应抢走环上相邻弧段的会话。
	// 理论重映射 ~ 1/4。断言远小于 hash%K 的 ~75%（用 50% 作宽松上界）。
	frac := float64(remapped) / float64(N)
	t.Logf("一致性哈希环重映射比例=%.3f (remapped=%d/%d)", frac, remapped, N)
	if frac > 0.5 {
		t.Fatalf("重映射比例 %.3f 过高，疑似非一致性哈希环（应 <<0.75）", frac)
	}
}

// TestSessionRouter_StickyStateMachine 覆盖 §4.4 (a)(b)(c) 三态。
func TestSessionRouter_StickyStateMachine(t *testing.T) {
	s := NewSessionRouter()
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
	}
	s.SetBaseline(1, base)

	// (a) 正常态：Pick 确定原哈希目标。
	orig, ok := s.Pick("sess-x")
	if !ok {
		t.Fatalf("Pick 失败")
	}
	origKey := pathRingKey(orig)

	// (b) 劣化偏移态：Degrade → 切到另一条可用路径，记原哈希目标，进入偏移态。
	s.Degrade("sess-x")
	shifted, ok := s.Pick("sess-x")
	if !ok {
		t.Fatalf("Degrade 后 Pick 失败")
	}
	if pathRingKey(shifted) == origKey {
		t.Fatalf("Degrade 后应切到不同路径，仍是原路径")
	}
	shiftedKey := pathRingKey(shifted)
	// 粘滞：后续 Pick 仍返回偏移路径，不抖动。
	for i := 0; i < 10; i++ {
		p, _ := s.Pick("sess-x")
		if pathRingKey(p) != shiftedKey {
			t.Fatalf("偏移态不粘滞，发生抖动: %v", p)
		}
	}

	// (c1) 拓扑重算后原哈希目标仍在 → 回归原目标。
	s.SetBaseline(2, base) // 同内容，原目标仍存在且可用。
	back, _ := s.Pick("sess-x")
	if pathRingKey(back) != origKey {
		t.Fatalf("原目标仍在应回归原目标，得到 %v 期望 %s", back, origKey)
	}

	// 再次进入偏移态，验证 (c2)。
	s.Degrade("sess-x")
	shifted2, _ := s.Pick("sess-x")
	_ = shifted2

	// (c2) 拓扑重算后原哈希目标消失 → 按新 K 集重映射（不应仍指向已消失的原目标）。
	baseNoOrig := make([][]Hop, 0, 2)
	for _, p := range base {
		if pathRingKey(p) != origKey {
			baseNoOrig = append(baseNoOrig, p)
		}
	}
	s.SetBaseline(3, baseNoOrig)
	remapped, ok := s.Pick("sess-x")
	if !ok {
		t.Fatalf("重映射 Pick 失败")
	}
	if pathRingKey(remapped) == origKey {
		t.Fatalf("原目标已消失，不应仍指向原目标")
	}
	// 重映射结果必须在新 baseline 内。
	inNew := false
	for _, p := range baseNoOrig {
		if pathRingKey(p) == pathRingKey(remapped) {
			inNew = true
		}
	}
	if !inNew {
		t.Fatalf("重映射结果不在新 baseline 内: %v", remapped)
	}
}

// TestSessionRouter_DegradeSkipsUnavailable：Degrade 跳过不可用路径。
func TestSessionRouter_DegradeSkipsUnavailable(t *testing.T) {
	s := NewSessionRouter()
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
	}
	s.SetBaseline(1, base)

	orig, _ := s.Pick("s")
	origIdx := -1
	for i, p := range base {
		if pathRingKey(p) == pathRingKey(orig) {
			origIdx = i
		}
	}
	// 标记除原路径外仅留一条可用：把原路径之外的一条标不可用，
	// Degrade 应切到剩余那条可用路径。
	// 先把所有非原路径里挑一条标不可用。
	unavail := (origIdx + 1) % 3
	s.MarkUnavailable("s", unavail)

	s.Degrade("s")
	got, ok := s.Pick("s")
	if !ok {
		t.Fatalf("Degrade 后应有可用偏移路径")
	}
	if pathRingKey(got) == pathRingKey(base[unavail]) {
		t.Fatalf("Degrade 不应切到不可用路径 idx=%d", unavail)
	}
	if pathRingKey(got) == pathRingKey(orig) {
		t.Fatalf("Degrade 应离开原路径")
	}
}

// TestSessionRouter_DegradeDisperses：多个会话从同一路径 Degrade 应分散到不同后继路径。
//
// 偏移目标以「会话自身环落点」为起点顺时针寻找，而非「当前路径首 vnode」的固定环邻居；
// 故同一 idx 上的不同会话偏移后应散布到多条后继路径（≥2 条），而非全挤一条。
func TestSessionRouter_DegradeDisperses(t *testing.T) {
	// 4 条路径，给偏移留出多个后继选择。
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
		mkPath("A", "E", "Z"),
	}
	s := NewSessionRouter()
	s.SetBaseline(1, base)

	// 找出一组初始落到同一路径 idx 的会话（同源偏移场景）。
	const N = 2000
	byIdx := map[int][]string{}
	for i := 0; i < N; i++ {
		sid := fmt.Sprintf("disp-%d", i)
		p, ok := s.Pick(sid)
		if !ok {
			t.Fatalf("Pick 失败")
		}
		idx := -1
		for j, bp := range base {
			if pathRingKey(bp) == pathRingKey(p) {
				idx = j
			}
		}
		byIdx[idx] = append(byIdx[idx], sid)
	}

	// 选会话数最多的那条原路径作为同源偏移群体。
	var srcIdx, maxN = -1, 0
	for idx, sids := range byIdx {
		if len(sids) > maxN {
			maxN, srcIdx = len(sids), idx
		}
	}
	if srcIdx < 0 || maxN < 50 {
		t.Fatalf("未找到足够大的同源会话群体 (maxN=%d)", maxN)
	}

	// 对该群体逐个 Degrade，统计偏移后落到的后继路径分布。
	shiftedTo := map[int]int{}
	for _, sid := range byIdx[srcIdx] {
		s.Degrade(sid)
		p, _ := s.Pick(sid)
		idx := -1
		for j, bp := range base {
			if pathRingKey(bp) == pathRingKey(p) {
				idx = j
			}
		}
		if idx == srcIdx {
			t.Fatalf("会话 %s 偏移后仍在原路径 idx=%d", sid, srcIdx)
		}
		shiftedTo[idx]++
	}

	t.Logf("同源 idx=%d 群体(%d 会话)偏移分布=%v", srcIdx, maxN, shiftedTo)
	// 一致性哈希环：以会话自身落点为起点，偏移应分散到多条后继（≥2），而非全挤一条。
	if len(shiftedTo) < 2 {
		t.Fatalf("同源偏移未分散，全挤到一条后继路径: %v（疑似用固定环邻居而非会话落点）", shiftedTo)
	}
}

// TestSessionRouter_DegradeNoAlternative：除当前外全不可用时 Degrade 无可切，
// 应保持当前路径且不进入偏移态（§4.4：无可用偏移交上层全断兜底处理）。
func TestSessionRouter_DegradeNoAlternative(t *testing.T) {
	s := NewSessionRouter()
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
	}
	s.SetBaseline(1, base)

	orig, ok := s.Pick("s")
	if !ok {
		t.Fatalf("Pick 失败")
	}
	// 把所有非当前路径标不可用，Degrade 无处可去。
	for i, p := range base {
		if pathRingKey(p) != pathRingKey(orig) {
			s.MarkUnavailable("s", i)
		}
	}
	s.Degrade("s")

	// 无可用偏移：保持当前（原）路径。
	got, ok := s.Pick("s")
	if !ok {
		t.Fatalf("无可用偏移时 Pick 仍应返回当前路径")
	}
	if pathRingKey(got) != pathRingKey(orig) {
		t.Fatalf("无可用偏移应保持原路径 %s，得到 %s", pathRingKey(orig), pathRingKey(got))
	}
	// 不应进入偏移态（curIdx 未变）。
	st := s.sessions["s"]
	if st == nil {
		t.Fatalf("会话状态丢失")
	}
	if st.shifted {
		t.Fatalf("无可用偏移不应置 shifted=true")
	}
}

// TestSessionRouter_SweepStale 验证 SweepStale 清理超时会话状态。
func TestSessionRouter_SweepStale(t *testing.T) {
	s := NewSessionRouter()
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
	}
	s.SetBaseline(1, base)

	// Pick 3 个会话，SweepStale(0) 应全部清理。
	s.Pick("sess-1")
	s.Pick("sess-2")
	s.Pick("sess-3")
	if len(s.sessions) != 3 {
		t.Fatalf("应有 3 个会话, got %d", len(s.sessions))
	}
	swept := s.SweepStale(0)
	if swept != 3 {
		t.Fatalf("SweepStale(0) 应清理 3 个, got %d", swept)
	}
	if len(s.sessions) != 0 {
		t.Fatalf("SweepStale(0) 后应无会话, got %d", len(s.sessions))
	}

	// Pick 2 个会话，睡眠一小段后再 Pick 1 个，SweepStale(5min) 只应清理前 2 个。
	s.Pick("old-1")
	s.Pick("old-2")
	// 手动把前两个会话的 lastAccess 设为很早以前。
	past := time.Now().Add(-10 * time.Minute).UnixNano()
	s.mu.Lock()
	s.sessions["old-1"].lastAccess = past
	s.sessions["old-2"].lastAccess = past
	s.mu.Unlock()

	s.Pick("new-1") // 最近访问
	if len(s.sessions) != 3 {
		t.Fatalf("应有 3 个会话, got %d", len(s.sessions))
	}
	swept = s.SweepStale(5 * time.Minute)
	if swept != 2 {
		t.Fatalf("SweepStale(5min) 应清理 2 个过期会话, got %d", swept)
	}
	if len(s.sessions) != 1 {
		t.Fatalf("应剩 1 个会话, got %d", len(s.sessions))
	}
	if s.sessions["new-1"] == nil {
		t.Fatal("new-1 不应被清理")
	}
}

func TestSessionRouter_ConcurrentSafe(t *testing.T) {
	s := NewSessionRouter()
	base := [][]Hop{
		mkPath("A", "B", "Z"),
		mkPath("A", "C", "Z"),
		mkPath("A", "D", "Z"),
	}
	s.SetBaseline(1, base)

	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			for i := 0; i < 200; i++ {
				sid := fmt.Sprintf("g%d-s%d", g, i%10)
				s.Pick(sid)
				if i%3 == 0 {
					s.Degrade(sid)
				}
				if i%5 == 0 {
					s.MarkUnavailable(sid, i%3)
				}
				if i%50 == 0 {
					s.SetBaseline(uint64(i), base)
				}
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}
