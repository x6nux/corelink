package ecmp

import (
	"hash/fnv"
	"math"
)

// RendezvousSelect 使用加权 Rendezvous Hashing 选择 next-hop 索引。
// 算法（Schindelhauer 2005）：score = -weight / ln(u)，u ∈ (0,1)，取最高分。
// next-hop 增减时仅影响指向变更 peer 的 flow，满足最小重映射性质。
func RendezvousSelect(peerIDs []string, weights []uint32, flowKey uint64) int {
	if len(peerIDs) == 0 || len(peerIDs) != len(weights) {
		return -1
	}
	best := -1
	bestScore := math.Inf(-1)
	for i, pid := range peerIDs {
		w := weights[i]
		if w == 0 {
			continue
		}
		hashVal := mixHash(pid, flowKey)
		// 映射到 (0, 1)：分母 = MaxUint64+1 = 2^64，保证 norm < 1；
		// hashVal=0 时用 epsilon 保证 norm > 0。
		norm := float64(hashVal) / (float64(math.MaxUint64) + 1.0)
		if norm < 1e-18 {
			norm = 1e-18
		}
		score := -float64(w) / math.Log(norm)
		if score > bestScore {
			bestScore = score
			best = i
		}
	}
	return best
}

// splitmix64 是一个高质量的 64 位整数混合器（SplitMix64 finalizer）。
// 保证单比特输入差异扩散至全部 64 位输出。
func splitmix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// mixHash 对 peerID 和 flowKey 做高质量混合哈希。
// 先用 FNV-1a 将变长 peerID 折叠为 64 位种子，再与 flowKey 做 splitmix64 混合，
// 保证每个 (peerID, flowKey) 组合的输出独立且均匀分布。
func mixHash(peerID string, flowKey uint64) uint64 {
	h := fnv.New64a()
	h.Write([]byte(peerID))
	peerSeed := h.Sum64()
	// 将 peerSeed 和 flowKey 组合后通过 splitmix64 获得充分雪崩
	combined := peerSeed + flowKey // 加法保留双向信息，避免 XOR 自消
	return splitmix64(combined)
}

// FlowHash 计算五元组流哈希键。
func FlowHash(srcIP, dstIP []byte, proto byte, srcPort, dstPort uint16) uint64 {
	h := fnv.New64a()
	h.Write(srcIP)
	h.Write(dstIP)
	h.Write([]byte{proto})
	h.Write([]byte{byte(srcPort >> 8), byte(srcPort)})
	h.Write([]byte{byte(dstPort >> 8), byte(dstPort)})
	return h.Sum64()
}
