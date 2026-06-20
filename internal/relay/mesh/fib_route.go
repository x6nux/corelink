package mesh

import (
	"net/netip"

	"github.com/x6nux/corelink/internal/ecmp"
	"github.com/x6nux/corelink/internal/transport/fib"
)

// FIBRoute 基于 FIB + ECMP Rendezvous Hash 的路由器，替代 SessionRouter。
// FIB 提供最长前缀匹配查找 next-hop 集合，Rendezvous Hash 在多 next-hop 间
// 按流哈希键做加权 ECMP 选择，保证同一流始终选中同一 peer（流亲和性）。
type FIBRoute struct {
	fib *fib.FIB
}

// NewFIBRoute 创建一个空的 FIB 路由器。
func NewFIBRoute() *FIBRoute {
	return &FIBRoute{fib: fib.NewFIB()}
}

// UpdateFIB 插入或覆盖指定前缀的 next-hop 集合。
func (r *FIBRoute) UpdateFIB(prefix netip.Prefix, nhs []fib.NextHop) {
	r.fib.Insert(prefix, nhs)
}

// RemoveFIB 删除指定前缀的路由条目。
func (r *FIBRoute) RemoveFIB(prefix netip.Prefix) {
	r.fib.Remove(prefix)
}

// Route 根据目标 IP 和流哈希键选择 next-hop。
// 先在 FIB 中做最长前缀匹配查找候选 next-hop 集合，再用 Rendezvous Hash
// 做加权 ECMP 选择。单 next-hop 时直接返回，跳过哈希计算。
func (r *FIBRoute) Route(dst netip.Addr, flowKey uint64) (fib.NextHop, bool) {
	nhs, ok := r.fib.Lookup(dst)
	if !ok || len(nhs) == 0 {
		return fib.NextHop{}, false
	}
	if len(nhs) == 1 {
		return nhs[0], true
	}
	peerIDs := make([]string, len(nhs))
	weights := make([]uint32, len(nhs))
	for i, nh := range nhs {
		peerIDs[i] = nh.PeerID
		weights[i] = nh.Weight
	}
	idx := ecmp.RendezvousSelect(peerIDs, weights, flowKey)
	if idx < 0 {
		return fib.NextHop{}, false
	}
	return nhs[idx], true
}
