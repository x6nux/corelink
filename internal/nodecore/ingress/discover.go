package ingress

import (
	"context"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// 各路来源的置信度（0-100），与 netif/config 既有常量风格一致。
const (
	// observedConfidence controller 实际观察到的源地址：可达性较强，中高。
	observedConfidence uint32 = 80
	// upnpConfidence 自助打洞主动建立，比 STUN 可靠(70)，低于网卡公网(90)。
	upnpConfidence uint32 = 75
	// stunConfidence STUN 反射地址：可达但需 keepalive 维持映射，中。
	stunConfidence uint32 = 70
	// urlConfidence URL 公网 IP：仅确认出口 IP、无端口信息，低。
	urlConfidence uint32 = 50
)

// DiscoverOptions 聚合 6 路入口来源。各路通过注入函数解耦，便于确定性测试；
// 真实装配（Task 1.5/4.x）传入 StunProbe/EnumInterfaces/QueryPublicIP 的闭包。
type DiscoverOptions struct {
	// NodeID 本节点 ID，写入产出的 IngressSet.NodeId。
	NodeID string

	// ConfigIngresses 静态配置（含 CDN）入口，已是 source=CONFIG（Task 1.3 产出）。
	ConfigIngresses []*genv1.Ingress

	// Observed controller 观察到的源地址，可空。
	Observed *genv1.Endpoint

	// StunFn 探测 STUN 反射地址与 NAT 类型；返回 err 时该路跳过。nil 时跳过。
	StunFn func(ctx context.Context) (host string, port uint32, nat genv1.NatType, err error)

	// NetifFn 枚举本机网卡入口，已是 source=NETIF（Task 1.3 产出）。nil 时跳过。
	NetifFn func() []*genv1.Ingress

	// UrlFn 查询公网出口 IP；返回 err 时该路跳过。nil 时跳过。
	UrlFn func(ctx context.Context) (host string, err error)

	// PortmapFn 通过端口映射（UPnP-IGD/NAT-PMP/PCP）获取公网入口；返回 err 时该路跳过。nil 时跳过。
	PortmapFn func(ctx context.Context) ([]*genv1.Ingress, error)
}

// Discover 聚合 6 路入口来源 → 合并去重 → genv1.IngressSet。
//
// 6 路：配置(含CDN) / controller 观察源地址 / STUN / 网卡 / URL / 端口映射(UPnP/NAT-PMP/PCP)。
// 各路独立容错：某路 fn 为 nil 或出错（含 host 为空）→ 跳过该路，不影响其他路。
// 始终返回非 nil 的 IngressSet（全部为空时 Ingresses 为空切片）。
func Discover(ctx context.Context, opts DiscoverOptions) *genv1.IngressSet {
	var candidates []*genv1.Ingress

	// 1. 配置(含 CDN)：直接采用（Task 1.3 已标 source=CONFIG）。
	candidates = append(candidates, opts.ConfigIngresses...)

	// 2. controller 观察源地址。
	if opts.Observed != nil && opts.Observed.GetHost() != "" {
		candidates = append(candidates, &genv1.Ingress{
			Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
			Host:       opts.Observed.GetHost(),
			Port:       opts.Observed.GetPort(),
			Source:     genv1.IngressSource_INGRESS_SOURCE_OBSERVED,
			Confidence: observedConfidence,
		})
	}

	// 3. STUN 反射地址（成功且 host 非空才纳入）。
	if opts.StunFn != nil {
		if host, port, nat, err := opts.StunFn(ctx); err == nil && host != "" {
			candidates = append(candidates, &genv1.Ingress{
				Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
				Host:       host,
				Port:       port,
				Source:     genv1.IngressSource_INGRESS_SOURCE_STUN,
				Confidence: stunConfidence,
				NatType:    nat,
			})
		}
	}

	// 4. 网卡入口（Task 1.3 已标 source=NETIF）。
	if opts.NetifFn != nil {
		candidates = append(candidates, opts.NetifFn()...)
	}

	// 5. URL 出口 IP（仅确认 IP，无端口）。
	if opts.UrlFn != nil {
		if host, err := opts.UrlFn(ctx); err == nil && host != "" {
			candidates = append(candidates, &genv1.Ingress{
				Kind:       genv1.IngressKind_INGRESS_KIND_IP_DIRECT,
				Host:       host,
				Source:     genv1.IngressSource_INGRESS_SOURCE_URL,
				Confidence: urlConfidence,
			})
		}
	}

	// 6. 端口映射（UPnP-IGD/NAT-PMP/PCP）主动建立的公网入口。
	if opts.PortmapFn != nil {
		if ings, err := opts.PortmapFn(ctx); err == nil {
			candidates = append(candidates, ings...)
		}
	}

	return &genv1.IngressSet{
		NodeId:    opts.NodeID,
		Ingresses: Merge(candidates),
	}
}
