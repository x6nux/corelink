package mesh

import (
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

// session_route.go: node 侧会话选路（规格 §4.2/§4.4）。
//
// 节点收到 controller 下发的 K 条基准路由（[][]Hop，到某固定 dst），用一致性哈希环
// 把每个会话固定到其中一条；当前路径劣化时切到环上下一条可用路径并记下"原哈希目标"
// （粘滞偏移态，不因抖动反复切）；拓扑重算（SetBaseline 新版本）后若原哈希目标仍在
// 新 baseline 中且可用则回归，否则按新 K 集重新映射。
//
// 一般态不本地算路：直接用下发的 K 路由。仅基准全断时才调 kpaths.EscapePath 兜底。
//
// 确定性：一致性哈希环 + 粘滞状态机均可复现（FNV-1a 内容哈希，golden 可断言）。

// Hop 是 node 侧路径中的一跳：到达 Node 这个节点，使用 Ingress 入口。
//
// 与 envelope.Hop / controller topology.Hop 语义对应；本包独立定义以避免对
// envelope 包的依赖（Task3.3 转发时负责 mesh.Hop ↔ envelope.Hop 转换）。
type Hop struct {
	Node    string // 该跳要到达的节点 ID
	Ingress string // 连接该节点所用的入口 ID
}

// pathRingKey 是一条路径在一致性哈希环上的定位 key（也用作路径内容标识）。
//
// 用路径内容（各跳 Node∶Ingress 拼接）的 FNV-1a 字符串作 key，保证：
//   - 确定性：同内容路径恒得同 key。
//   - 内容匹配：SetBaseline 跨版本判断"原哈希目标是否仍在"按 key 比较即可。
func pathRingKey(p []Hop) string {
	h := fnv.New64a()
	for _, hop := range p {
		// 用 \x1f 分隔字段、\x1e 分隔跳，避免拼接歧义。
		_, _ = h.Write([]byte(hop.Node))
		_, _ = h.Write([]byte{0x1f})
		_, _ = h.Write([]byte(hop.Ingress))
		_, _ = h.Write([]byte{0x1e})
	}
	// 以十六进制串作为稳定 key。
	return uitoa16(h.Sum64())
}

// uitoa16 把 uint64 转为定长 16 位十六进制串（确定性、无依赖）。
func uitoa16(v uint64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = digits[v&0xf]
		v >>= 4
	}
	return string(b[:])
}

// hash64 计算字符串在环上的 64 位定位点（用于会话与路径虚拟节点）。
//
// FNV-1a 逐字节累积，对"共享前缀 + 顺序/相近后缀"的输入（如 sess-0、sess-1…）
// 其输出会挤在一个窄带，无法散满环 → 少量会话严重偏斜。故在 FNV 之后再过一道
// splitmix64 finalizer（强雪崩整数混合），把任意相近的 FNV 值打散到全 64 位空间，
// 保证确定性的同时让聚集输入也均匀落点。
func hash64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return mix64(h.Sum64())
}

// mix64 是 splitmix64 的 finalizer：强雪崩整数混合，确定性且无依赖。
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// ringEntry 是环上一个槽位：某条 baseline 路径在环上的定位点。
type ringEntry struct {
	point uint64 // 环上位置（路径内容 hash）
	idx   int    // 对应 baseline 路径索引
}

// sessionState 是单个会话的粘滞状态（§4.4 (a)(b)(c)）。
//
//   - origKey：原哈希目标路径的内容 key（一致性哈希环选定，正常态即返回它）。
//   - curIdx：当前实际使用的 baseline 路径索引。
//   - shifted：是否处于偏移态（Degrade 后为 true，回归/重映射后为 false）。
type sessionState struct {
	origKey    string
	curIdx     int
	shifted    bool
	lastAccess int64 // unix nano，Pick/Degrade 时更新
}

// SessionRouter 持 controller 下发的 K 基准路由，按会话做一致性哈希环选路 + 粘滞偏移。
//
// 并发安全（单 mutex 保护全部状态）。
type SessionRouter struct {
	mu       sync.Mutex
	version  uint64         // 拓扑版本
	baseline [][]Hop        // K 条基准路由（到某固定 dst）
	ring     []ringEntry    // baseline 在环上的有序定位点（按 point 升序）
	keyToIdx map[string]int // 路径内容 key → baseline 索引（内容匹配/回归用）
	sessions map[string]*sessionState
	// unavail：每会话的不可用路径索引集（MarkUnavailable 设，SetBaseline 清）。
	// 真实劣化信号由 Task3.4 源中转接入。
	unavail map[string]map[int]bool
}

// NewSessionRouter 创建一个空的会话路由器（需先 SetBaseline 才能选路）。
func NewSessionRouter() *SessionRouter {
	return &SessionRouter{
		keyToIdx: make(map[string]int),
		sessions: make(map[string]*sessionState),
		unavail:  make(map[string]map[int]bool),
	}
}

// SetBaseline 用 controller 下发的新 K 基准路由全量替换当前 baseline（拓扑更新）。
//
// 同时重建一致性哈希环，并对已有会话执行粘滞回归/重映射（§4.4 (c)）：
//   - 偏移态会话：若原哈希目标路径在新 baseline 中仍存在（按内容 key 匹配）→ 回归原目标，
//     退出偏移态；否则按新 K 集一致性哈希环重新映射，origKey 更新为新选定目标。
//   - 正常态会话：若其原哈希目标仍存在则保持；否则按新环重映射。
//   - 清空所有会话的不可用路径集（旧索引对新 baseline 无意义）。
func (s *SessionRouter) SetBaseline(version uint64, baseline [][]Hop) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.version = version
	s.baseline = baseline
	s.ring = buildRing(baseline)
	s.keyToIdx = make(map[string]int, len(baseline))
	for i, p := range baseline {
		s.keyToIdx[pathRingKey(p)] = i
	}
	// 旧不可用索引对新 baseline 无意义，全清。
	s.unavail = make(map[string]map[int]bool)

	// 对已有会话执行回归/重映射。
	for sid, st := range s.sessions {
		if idx, ok := s.keyToIdx[st.origKey]; ok {
			// 原哈希目标仍存在 → 回归原目标，退出偏移态。
			st.curIdx = idx
			st.shifted = false
		} else {
			// 原目标消失 → 按新 K 集重新映射。
			delete(s.sessions, sid)
		}
	}
}

// ringVnodes 是每条路径在一致性哈希环上的虚拟节点数。
//
// 一致性哈希在节点（这里是路径）很少时，环上弧段会严重不均（个别路径分不到会话，
// 且 K 变化时重映射比例偏高）。标准做法是为每条路径放多个虚拟节点（不同 replica
// 的 hash 散布全环），使各路径占据的弧段更均匀、负载更平衡、K 变化时重映射更局部。
// 160 是工业界（如 Ketama）常用经验值。
const ringVnodes = 160

// buildRing 把 baseline 各路径以虚拟节点形式放到一致性哈希环上，按位置升序排列。
//
// 每条路径放 ringVnodes 个虚拟节点（key = pathKey + "#" + replica 的 hash）。
// 内容相同的路径（key 相同）只放一次（去重，保留首个索引），避免重复占环。
func buildRing(baseline [][]Hop) []ringEntry {
	seenKey := make(map[string]bool, len(baseline))
	seenPt := make(map[uint64]bool, len(baseline)*ringVnodes)
	ring := make([]ringEntry, 0, len(baseline)*ringVnodes)
	for i, p := range baseline {
		key := pathRingKey(p)
		if seenKey[key] {
			continue // 内容相同的路径只占一组虚拟节点。
		}
		seenKey[key] = true
		for r := range ringVnodes {
			// replica 号放在前缀：FNV-1a 逐字节累积，若 replica 作后缀则各 replica
			// 仅扰动末几步混合、结果挤在同一窄带（高位被共享 key 前缀主导），导致
			// 虚拟节点不散布。把 replica 放最前，使首字节即不同，整串雪崩散满全环。
			pt := hash64(uitoa16(uint64(r)) + "#" + key)
			if seenPt[pt] {
				continue // 极少数 hash 碰撞，跳过。
			}
			seenPt[pt] = true
			ring = append(ring, ringEntry{point: pt, idx: i})
		}
	}
	sort.Slice(ring, func(a, b int) bool {
		if ring[a].point != ring[b].point {
			return ring[a].point < ring[b].point
		}
		return ring[a].idx < ring[b].idx
	})
	return ring
}

// ringPick 在环上为 sessionID 定位：hash(sessionID) 顺时针第一个环点对应的路径索引。
// 空环返回 -1。
func (s *SessionRouter) ringPick(sessionID string) int {
	if len(s.ring) == 0 {
		return -1
	}
	h := hash64(sessionID)
	// 顺时针第一个 point >= h；越过环尾则回绕到第一个点。
	i := sort.Search(len(s.ring), func(i int) bool { return s.ring[i].point >= h })
	if i == len(s.ring) {
		i = 0
	}
	return s.ring[i].idx
}

// Pick 为会话选一条路径并返回其 Hop 序列（副本）。
//
//   - 首次见到会话：一致性哈希环选定 → 记为原哈希目标（正常态）。
//   - 偏移态会话：返回偏移路径（粘滞，不重算）。
//   - baseline 为空：返回 (nil, false)。
//
// 返回的切片是副本，调用方修改不影响内部状态。
func (s *SessionRouter) Pick(sessionID string) ([]Hop, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.baseline) == 0 {
		return nil, false
	}

	now := time.Now().UnixNano()

	st := s.sessions[sessionID]
	if st == nil {
		idx := s.ringPick(sessionID)
		if idx < 0 {
			return nil, false
		}
		st = &sessionState{
			origKey:    pathRingKey(s.baseline[idx]),
			curIdx:     idx,
			shifted:    false,
			lastAccess: now,
		}
		s.sessions[sessionID] = st
	}

	// 防御：curIdx 越界（理论上 SetBaseline 已修正，但兜底）。
	if st.curIdx < 0 || st.curIdx >= len(s.baseline) {
		idx := s.ringPick(sessionID)
		if idx < 0 {
			return nil, false
		}
		st.curIdx = idx
		st.origKey = pathRingKey(s.baseline[idx])
		st.shifted = false
	}

	st.lastAccess = now
	return clonePath(s.baseline[st.curIdx]), true
}

// Degrade 标记会话当前路径劣化：切到 K 中环上下一条可用路径，记原哈希目标，进入偏移态。
//
//   - 若会话尚未 Pick 过：先按环初始化（等价一次 Pick）再 Degrade。
//   - 当前路径同时标记为不可用（避免再次切回）。
//   - 从环上当前位置顺时针找下一条可用（不在 unavail）的路径；找到则切换并置偏移态。
//   - 无其他可用路径：保持当前路径（偏移态不变），等待 Task3.4 上层处理（全断兜底）。
func (s *SessionRouter) Degrade(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.baseline) == 0 {
		return
	}

	now := time.Now().UnixNano()

	st := s.sessions[sessionID]
	if st == nil {
		idx := s.ringPick(sessionID)
		if idx < 0 {
			return
		}
		st = &sessionState{
			origKey:    pathRingKey(s.baseline[idx]),
			curIdx:     idx,
			shifted:    false,
			lastAccess: now,
		}
		s.sessions[sessionID] = st
	}
	if st.curIdx < 0 || st.curIdx >= len(s.baseline) {
		return
	}

	st.lastAccess = now

	// 当前路径不可用。
	s.markUnavailLocked(sessionID, st.curIdx)

	// 从会话自身的环落点顺时针找下一条可用路径作偏移目标。
	next, ok := s.nextAvailableOnRing(sessionID, st.curIdx)
	if !ok {
		// 无可用偏移路径：保持当前（上层全断兜底）。
		return
	}
	st.curIdx = next
	st.shifted = true
}

// nextAvailableOnRing 从**会话自身的 hash 落点**顺时针寻找下一条可用偏移路径索引。
//
// 起点用 hash64(sessionID) 经 sort.Search 定位（与 Pick/ringPick 同源），而非当前
// 路径首个 vnode 的位置——后者会让所有从同一 idx 偏移的会话挤向同一条后继路径，
// 削弱一致性哈希环"偏移也均匀"的收益。以会话真实落点为起点，偏移分布随之均匀。
//
// 跳过 fromIdx 自身与所有不可用路径。返回 (idx, true)；空环 / 全不可用 / 仅剩 fromIdx
// 时返回 (-1, false)（与 ringPick 风格统一）。
func (s *SessionRouter) nextAvailableOnRing(sessionID string, fromIdx int) (int, bool) {
	if len(s.ring) == 0 {
		return -1, false
	}
	un := s.unavail[sessionID]

	// 会话落点：hash(sessionID) 顺时针第一个环点（与 ringPick 同源）。
	h := hash64(sessionID)
	start := sort.Search(len(s.ring), func(i int) bool { return s.ring[i].point >= h })
	if start == len(s.ring) {
		start = 0 // 越过环尾回绕。
	}

	// 从落点起顺时针绕一圈（含起点本身，因落点对应路径可能正是 fromIdx，需跳过）。
	for off := 0; off < len(s.ring); off++ {
		e := s.ring[(start+off)%len(s.ring)]
		if e.idx == fromIdx {
			continue
		}
		if un != nil && un[e.idx] {
			continue
		}
		return e.idx, true
	}
	return -1, false
}

// MarkUnavailable 标记会话的某条 baseline 路径不可用（Pick/Degrade 跳过）。
//
// pathIdx 越界或为当前路径都允许设置；SetBaseline 时全部清空。
// 真实劣化信号由 Task3.4 源中转接入；本 task 仅提供可用性模型。
func (s *SessionRouter) MarkUnavailable(sessionID string, pathIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markUnavailLocked(sessionID, pathIdx)
}

func (s *SessionRouter) markUnavailLocked(sessionID string, pathIdx int) {
	if pathIdx < 0 || pathIdx >= len(s.baseline) {
		return
	}
	un := s.unavail[sessionID]
	if un == nil {
		un = make(map[int]bool)
		s.unavail[sessionID] = un
	}
	un[pathIdx] = true
}

// SweepStale 清理达到 maxAge 未被访问的会话状态，防止长运行中转节点内存无限增长。
func (s *SessionRouter) SweepStale(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge).UnixNano()
	swept := 0
	for sid, st := range s.sessions {
		if st.lastAccess <= cutoff {
			delete(s.sessions, sid)
			delete(s.unavail, sid)
			swept++
		}
	}
	return swept
}

// clonePath 返回 Hop 序列的副本。
func clonePath(p []Hop) []Hop {
	if p == nil {
		return nil
	}
	out := make([]Hop, len(p))
	copy(out, p)
	return out
}
