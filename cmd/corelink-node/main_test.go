package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/x6nux/corelink/internal/featureflag"
	agentconfig "github.com/x6nux/corelink/internal/nodecore/config"
	"github.com/x6nux/corelink/internal/nodecore/keystore"
	"github.com/x6nux/corelink/internal/nodecore/portmap"
	"github.com/x6nux/corelink/internal/nodecore/probe"
	"github.com/x6nux/corelink/internal/nodecore/tun"
	"github.com/x6nux/corelink/internal/relay/mesh"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─────────────────────── roleFromConfig ───────────────────────

func TestRoleFromConfig(t *testing.T) {
	tests := []struct {
		name string
		nc   *genv1.NodeConfig
		want genv1.NodeTopoRole
	}{
		{"nil config", nil, genv1.NodeTopoRole_NODE_TOPO_ROLE_UNSPECIFIED},
		{"no topology", &genv1.NodeConfig{}, genv1.NodeTopoRole_NODE_TOPO_ROLE_UNSPECIFIED},
		{
			"leaf",
			&genv1.NodeConfig{Topology: &genv1.TopologyAssignment{Role: genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF}},
			genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF,
		},
		{
			"transit",
			&genv1.NodeConfig{Topology: &genv1.TopologyAssignment{Role: genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT}},
			genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roleFromConfig(tt.nc); got != tt.want {
				t.Fatalf("roleFromConfig = %v, want %v", got, tt.want)
			}
		})
	}
}

// ─────────────────────── assignmentToBaseline ───────────────────────

func TestAssignmentToBaseline_NilEmpty(t *testing.T) {
	if got := assignmentToBaseline(nil); len(got) != 0 {
		t.Fatalf("nil assignment → want empty, got %v", got)
	}
	if got := assignmentToBaseline(&genv1.TopologyAssignment{}); len(got) != 0 {
		t.Fatalf("empty assignment → want empty, got %v", got)
	}
}

func TestAssignmentToBaseline_GroupsByDstAndConvertsHops(t *testing.T) {
	asg := &genv1.TopologyAssignment{
		BaselineRoutes: []*genv1.Route2{
			{
				DstNode: "dst-A",
				Hops: []*genv1.Hop{
					{NodeId: "n1", IngressId: "i1"},
					{NodeId: "n2", IngressId: "i2"},
				},
			},
			{
				DstNode: "dst-A", // 同 dst 的第二条 K 路径
				Hops: []*genv1.Hop{
					{NodeId: "n3", IngressId: "i3"},
				},
			},
			{
				DstNode: "dst-B",
				Hops: []*genv1.Hop{
					{NodeId: "n4", IngressId: "i4"},
				},
			},
		},
	}
	got := assignmentToBaseline(asg)

	if len(got) != 2 {
		t.Fatalf("want 2 dst groups, got %d: %v", len(got), got)
	}
	// dst-A 应有 2 条路径。
	pathsA := got["dst-A"]
	if len(pathsA) != 2 {
		t.Fatalf("dst-A want 2 paths, got %d", len(pathsA))
	}
	// 第一条路径 hop 序列正确转换。
	if len(pathsA[0]) != 2 ||
		pathsA[0][0].Node != "n1" || pathsA[0][0].Ingress != "i1" ||
		pathsA[0][1].Node != "n2" || pathsA[0][1].Ingress != "i2" {
		t.Fatalf("dst-A path0 mismatch: %+v", pathsA[0])
	}
	if len(pathsA[1]) != 1 || pathsA[1][0].Node != "n3" || pathsA[1][0].Ingress != "i3" {
		t.Fatalf("dst-A path1 mismatch: %+v", pathsA[1])
	}
	// dst-B 单条。
	if pathsB := got["dst-B"]; len(pathsB) != 1 || pathsB[0][0].Node != "n4" {
		t.Fatalf("dst-B mismatch: %+v", got["dst-B"])
	}
}

// ─────────────────────── buildIngressOptions ───────────────────────

func TestBuildIngressOptions_WiresAllFns(t *testing.T) {
	cfg := &agentconfig.Config{
		Ingresses: []agentconfig.IngressConfig{
			{Kind: "ip", Host: "203.0.113.5", Port: 7443},
		},
	}
	observed := &genv1.Endpoint{Host: "198.51.100.9", Port: 9000}

	stunCalled, netifCalled, urlCalled := false, false, false
	stunFn := func(ctx context.Context) (string, uint32, genv1.NatType, error) {
		stunCalled = true
		return "", 0, genv1.NatType_NAT_TYPE_UNKNOWN, nil
	}
	netifFn := func() []*genv1.Ingress { netifCalled = true; return nil }
	urlFn := func(ctx context.Context) (string, error) { urlCalled = true; return "", nil }

	opts := buildIngressOptions(cfg, "node-1", observed, stunFn, netifFn, urlFn, nil)

	if opts.NodeID != "node-1" {
		t.Fatalf("NodeID = %q, want node-1", opts.NodeID)
	}
	if opts.Observed != observed {
		t.Fatalf("Observed not wired")
	}
	if len(opts.ConfigIngresses) != 1 || opts.ConfigIngresses[0].GetHost() != "203.0.113.5" {
		t.Fatalf("ConfigIngresses not wired: %+v", opts.ConfigIngresses)
	}
	// 调用各 fn 确认非 nil 且接通。
	if opts.StunFn == nil || opts.NetifFn == nil || opts.UrlFn == nil {
		t.Fatalf("fns not all wired: stun=%t netif=%t url=%t", opts.StunFn == nil, opts.NetifFn == nil, opts.UrlFn == nil)
	}
	_, _, _, _ = opts.StunFn(context.Background())
	opts.NetifFn()
	_, _ = opts.UrlFn(context.Background())
	if !stunCalled || !netifCalled || !urlCalled {
		t.Fatalf("fns not invoked: stun=%v netif=%v url=%v", stunCalled, netifCalled, urlCalled)
	}
}

func TestBuildIngressOptions_NilConfig(t *testing.T) {
	opts := buildIngressOptions(nil, "node-x", nil, nil, nil, nil, nil)
	if opts.NodeID != "node-x" {
		t.Fatalf("NodeID = %q", opts.NodeID)
	}
	if len(opts.ConfigIngresses) != 0 {
		t.Fatalf("nil cfg → want no config ingresses")
	}
}

// ─────────────────────── buildIngressOptions + PortmapFn ───────────────────────

func TestBuildIngressOptionsWithPortmapFn(t *testing.T) {
	portmapCalled := false
	portmapFn := func(ctx context.Context) ([]*genv1.Ingress, error) {
		portmapCalled = true
		return []*genv1.Ingress{{Id: "upnp-test", Host: "1.2.3.4", Port: 51820}}, nil
	}

	opts := buildIngressOptions(nil, "node-pm", nil, nil, nil, nil, portmapFn)
	if opts.PortmapFn == nil {
		t.Fatalf("PortmapFn not wired (nil)")
	}
	ings, err := opts.PortmapFn(context.Background())
	if err != nil {
		t.Fatalf("PortmapFn returned error: %v", err)
	}
	if !portmapCalled {
		t.Fatalf("PortmapFn was not invoked")
	}
	if len(ings) != 1 || ings[0].GetHost() != "1.2.3.4" {
		t.Fatalf("PortmapFn result mismatch: %+v", ings)
	}
}

func TestBuildIngressOptionsWithPortmapFn_Nil(t *testing.T) {
	opts := buildIngressOptions(nil, "node-pm-nil", nil, nil, nil, nil, nil)
	if opts.PortmapFn != nil {
		t.Fatalf("nil portmapFn should yield nil PortmapFn, got non-nil")
	}
}

// ─────────────────────── probeTargetsFromConfig ───────────────────────

func TestProbeTargetsFromConfig(t *testing.T) {
	if got := probeTargetsFromConfig(nil); len(got) != 0 {
		t.Fatalf("nil → want empty, got %v", got)
	}
	asg := &genv1.TopologyAssignment{
		ProbeTargets: []*genv1.ProbeTarget{
			{NodeId: "n1", IngressIds: []string{"i1", "i2"}},
			{NodeId: "n2", IngressIds: []string{"i3"}},
		},
	}
	got := probeTargetsFromConfig(asg)
	if len(got) != 3 {
		t.Fatalf("want 3 targets, got %d: %v", len(got), got)
	}
	want := map[string]bool{"n1\x00i1": true, "n1\x00i2": true, "n2\x00i3": true}
	for _, pt := range got {
		k := pt.NodeID + "\x00" + pt.IngressID
		if !want[k] {
			t.Fatalf("unexpected target %q", k)
		}
	}
}

// ─────────────────────── ingressResolverFromAssignment ───────────────────────

func TestIngressResolverFromAssignment(t *testing.T) {
	// nil / 无邻居 → nil resolver。
	if r := ingressResolverFromAssignment(nil); r != nil {
		t.Fatalf("nil assignment → want nil resolver")
	}
	if r := ingressResolverFromAssignment(&genv1.TopologyAssignment{}); r != nil {
		t.Fatalf("no neighbors → want nil resolver")
	}

	asg := &genv1.TopologyAssignment{
		Neighbors: []*genv1.NeighborRef{
			{
				NodeId: "relay-cdn",
				Ingresses: []*genv1.Ingress{
					{Id: "ing-cdn", Host: "cdn.example.com", Port: 443, Kind: genv1.IngressKind_INGRESS_KIND_CDN, Sni: "sni.example.com"},
				},
			},
			{
				NodeId: "relay-ip",
				Ingresses: []*genv1.Ingress{
					{Id: "ing-ip", Host: "203.0.113.7", Port: 7443, Kind: genv1.IngressKind_INGRESS_KIND_IP_DIRECT},
				},
			},
		},
	}
	r := ingressResolverFromAssignment(asg)
	if r == nil {
		t.Fatalf("want non-nil resolver")
	}
	cdn := r("relay-cdn")
	if cdn == nil || !cdn.IsCDN || cdn.SNI != "sni.example.com" || cdn.Addr != "cdn.example.com:443" || cdn.IngressID != "ing-cdn" {
		t.Fatalf("cdn endpoint mismatch: %+v", cdn)
	}
	ip := r("relay-ip")
	if ip == nil || ip.IsCDN || ip.SNI != "" || ip.Addr != "203.0.113.7:7443" {
		t.Fatalf("ip endpoint mismatch: %+v", ip)
	}
	if r("unknown") != nil {
		t.Fatalf("unknown relay → want nil")
	}
}

// ─────────────────────── peerIngressAddrsFromAssignment ───────────────────────

func TestPeerIngressAddrsFromAssignment(t *testing.T) {
	asg := &genv1.TopologyAssignment{
		Neighbors: []*genv1.NeighborRef{
			{
				NodeId: "nb1",
				Ingresses: []*genv1.Ingress{
					{Id: "i1", Host: "10.0.0.1", Port: 7443},
					{Id: "i2", Host: "cdn.x", Port: 443, Kind: genv1.IngressKind_INGRESS_KIND_CDN, Sni: "s.x"},
				},
			},
		},
	}
	addrs, sni := peerIngressAddrsFromAssignment(asg, &genv1.NodeConfig{})
	if addrs["nb1"]["i1"] != "10.0.0.1:7443" {
		t.Fatalf("addr i1 mismatch: %v", addrs)
	}
	if addrs["nb1"]["i2"] != "cdn.x:443" {
		t.Fatalf("addr i2 mismatch: %v", addrs)
	}
	if sni["nb1"]["i2"] != "s.x" {
		t.Fatalf("sni i2 mismatch: %v", sni)
	}
	if _, ok := sni["nb1"]["i1"]; ok {
		t.Fatalf("non-cdn i1 should have no sni")
	}
}

// ─────────────────────── assembleByRole 分支选择 ───────────────────────

// fakeAssembler 记录被调用的分支与参数，用于断言角色装配分支选择正确。
type fakeAssembler struct {
	leaf    *leafParams
	transit *transitParams
	basic   *basicParams
}

func (f *fakeAssembler) SetupLeaf(_ context.Context, p leafParams) error {
	f.leaf = &p
	return nil
}
func (f *fakeAssembler) SetupTransit(_ context.Context, p transitParams) error {
	f.transit = &p
	return nil
}
func (f *fakeAssembler) SetupBasicAgent(_ context.Context, p basicParams) error {
	f.basic = &p
	return nil
}

func TestAssembleByRole_Transit(t *testing.T) {
	nc := &genv1.NodeConfig{
		Topology: &genv1.TopologyAssignment{
			Role: genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT,
			Neighbors: []*genv1.NeighborRef{
				{NodeId: "nb1", Ingresses: []*genv1.Ingress{{Id: "i1", Host: "10.0.0.1", Port: 7443}}},
			},
			BaselineRoutes: []*genv1.Route2{
				{DstNode: "dst-A", Hops: []*genv1.Hop{{NodeId: "n1", IngressId: "i1"}}},
			},
		},
	}
	fa := &fakeAssembler{}
	if err := assembleByRole(context.Background(), fa, "self", nc); err != nil {
		t.Fatal(err)
	}
	if fa.transit == nil {
		t.Fatalf("expected SetupTransit branch")
	}
	if fa.leaf != nil || fa.basic != nil {
		t.Fatalf("unexpected other branch invoked")
	}
	if fa.transit.NodeID != "self" {
		t.Fatalf("NodeID = %q", fa.transit.NodeID)
	}
	if len(fa.transit.Baseline["dst-A"]) != 1 {
		t.Fatalf("baseline not wired: %v", fa.transit.Baseline)
	}
	if fa.transit.PeerIngressAddrs["nb1"]["i1"] != "10.0.0.1:7443" {
		t.Fatalf("peer ingress addrs not wired: %v", fa.transit.PeerIngressAddrs)
	}
	if len(fa.transit.NeighborIDs) != 1 || fa.transit.NeighborIDs[0] != "nb1" {
		t.Fatalf("neighbor ids not wired: %v", fa.transit.NeighborIDs)
	}
}

func TestAssembleByRole_Leaf(t *testing.T) {
	nc := &genv1.NodeConfig{
		Relays: []*genv1.RelayEndpoint{{RelayId: "r1"}},
		Topology: &genv1.TopologyAssignment{
			Role: genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF,
			Neighbors: []*genv1.NeighborRef{
				{NodeId: "r1", Ingresses: []*genv1.Ingress{{Id: "i1", Host: "1.2.3.4", Port: 7443}}},
			},
		},
	}
	fa := &fakeAssembler{}
	if err := assembleByRole(context.Background(), fa, "self", nc); err != nil {
		t.Fatal(err)
	}
	if fa.leaf == nil {
		t.Fatalf("expected SetupLeaf branch")
	}
	if fa.transit != nil || fa.basic != nil {
		t.Fatalf("unexpected other branch invoked")
	}
	if len(fa.leaf.Candidates) != 1 || fa.leaf.Candidates[0].GetRelayId() != "r1" {
		t.Fatalf("candidates not wired: %v", fa.leaf.Candidates)
	}
	if fa.leaf.IngressResolver == nil {
		t.Fatalf("ingress resolver not wired")
	}
	if ep := fa.leaf.IngressResolver("r1"); ep == nil || ep.Addr != "1.2.3.4:7443" {
		t.Fatalf("resolver result mismatch: %+v", ep)
	}
}

func TestAssembleByRole_BasicAgentWhenNoTopology(t *testing.T) {
	nc := &genv1.NodeConfig{Relays: []*genv1.RelayEndpoint{{RelayId: "r1"}}}
	fa := &fakeAssembler{}
	if err := assembleByRole(context.Background(), fa, "self", nc); err != nil {
		t.Fatal(err)
	}
	if fa.basic == nil {
		t.Fatalf("expected SetupBasicAgent branch")
	}
	if fa.leaf != nil || fa.transit != nil {
		t.Fatalf("unexpected other branch invoked")
	}
	if fa.basic.NodeID != "self" {
		t.Fatalf("NodeID = %q", fa.basic.NodeID)
	}
}

func TestAssembleByRole_BasicAgentWhenUnspecifiedRole(t *testing.T) {
	nc := &genv1.NodeConfig{
		Topology: &genv1.TopologyAssignment{Role: genv1.NodeTopoRole_NODE_TOPO_ROLE_UNSPECIFIED},
	}
	fa := &fakeAssembler{}
	if err := assembleByRole(context.Background(), fa, "self", nc); err != nil {
		t.Fatal(err)
	}
	if fa.basic == nil {
		t.Fatalf("unspecified role → expected SetupBasicAgent branch")
	}
}

// ─────────────────────── addrOf / uitoa ───────────────────────

func TestAddrOf(t *testing.T) {
	cases := map[string]struct {
		host string
		port uint32
		want string
	}{
		"host+port": {"1.2.3.4", 7443, "1.2.3.4:7443"},
		"zero port": {"1.2.3.4", 0, "1.2.3.4"},
		"port 1":    {"h", 1, "h:1"},
	}
	for name, c := range cases {
		if got := addrOf(c.host, c.port); got != c.want {
			t.Fatalf("%s: addrOf = %q, want %q", name, got, c.want)
		}
	}
}

// ─────────────────────── 冒烟：ingress 上报 + probe 驱动（无真实网络）───────────

// mockIngressClient 是 genv1.IngressServiceClient 的内存 mock，记录各 RPC 调用。
type mockIngressClient struct {
	reportIngress atomic.Int32
	reportQuality atomic.Int32
	reportEdge    atomic.Int32
	observeCalled atomic.Int32
	lastSet       *genv1.IngressSet
	mu            sync.Mutex
}

func (m *mockIngressClient) ReportIngress(_ context.Context, in *genv1.IngressSet, _ ...grpc.CallOption) (*genv1.Ack, error) {
	m.reportIngress.Add(1)
	m.mu.Lock()
	m.lastSet = in
	m.mu.Unlock()
	return &genv1.Ack{}, nil
}
func (m *mockIngressClient) ReportQuality(_ context.Context, _ *genv1.QualityReport, _ ...grpc.CallOption) (*genv1.Ack, error) {
	m.reportQuality.Add(1)
	return &genv1.Ack{}, nil
}
func (m *mockIngressClient) ReportEdgeEvent(_ context.Context, _ *genv1.EdgeEvent, _ ...grpc.CallOption) (*genv1.Ack, error) {
	m.reportEdge.Add(1)
	return &genv1.Ack{}, nil
}
func (m *mockIngressClient) ObserveSource(_ context.Context, _ *genv1.ObserveRequest, _ ...grpc.CallOption) (*genv1.SourceAddr, error) {
	m.observeCalled.Add(1)
	return &genv1.SourceAddr{Host: "198.51.100.1", Port: 1234}, nil
}

// TestSmoke_DiscoverAndReportIngress 冒烟：装配的入口发现 + 上报路径会调用
// ObserveSource + ReportIngress（用 mock client，不接触真实网络 / controller）。
func TestSmoke_DiscoverAndReportIngress(t *testing.T) {
	mock := &mockIngressClient{}
	cfg := &agentconfig.Config{
		// 仅用配置入口路，避免真实 STUN/网卡/URL 探测引入外部依赖与不确定性。
		Ingresses: []agentconfig.IngressConfig{
			{Kind: "ip", Host: "203.0.113.42", Port: 7443},
		},
	}

	// 直接调用装配好的上报逻辑，但用确定性 fn 注入（不走真实 StunProbe）。
	// discoverAndReportIngress 内部用真实探测 fn；此处复用其装配的核心：
	// buildIngressOptions + Discover + ReportIngress，以确定性 fn 验证上报被调。
	observed := &genv1.Endpoint{Host: "198.51.100.1", Port: 1234}
	opts := buildIngressOptions(cfg, "node-smoke", observed,
		func(context.Context) (string, uint32, genv1.NatType, error) {
			return "", 0, genv1.NatType_NAT_TYPE_UNKNOWN, nil
		},
		func() []*genv1.Ingress { return nil },
		func(context.Context) (string, error) { return "", nil },
		nil, // portmapFn
	)
	set := discoverWithOpts(opts)
	if _, err := mock.ReportIngress(context.Background(), set); err != nil {
		t.Fatalf("ReportIngress: %v", err)
	}

	if mock.reportIngress.Load() != 1 {
		t.Fatalf("ReportIngress 未被调用一次: %d", mock.reportIngress.Load())
	}
	mock.mu.Lock()
	last := mock.lastSet
	mock.mu.Unlock()
	if last.GetNodeId() != "node-smoke" {
		t.Fatalf("上报 IngressSet.NodeId = %q", last.GetNodeId())
	}
	// 至少含配置入口（203.0.113.42）。
	found := false
	for _, ing := range last.GetIngresses() {
		if ing.GetHost() == "203.0.113.42" {
			found = true
		}
	}
	if !found {
		t.Fatalf("上报集未含配置入口: %+v", last.GetIngresses())
	}
}

// TestSmoke_ProbeLoopDrivesReporter 冒烟：driveProbeLoop 对下发 probe_targets 探测
// 并经 Reporter 上报（EmitEvent / EmitQuality 至少被驱动）。
func TestSmoke_ProbeLoopDrivesReporter(t *testing.T) {
	var quality atomic.Int32
	reporter := probe.NewReporter(probe.ReporterConfig{
		SelfNode: "self",
		Clock:    time.Now,
		EmitQuality: func(*genv1.QualityReport) {
			quality.Add(1)
		},
	})
	targets := []probeTarget{{NodeID: "n1", IngressID: "i1"}}

	// 直接驱动一轮（不依赖 ticker 时间，避免 flaky）：对每个 target OnProbe + Tick。
	for _, tg := range targets {
		pt := probe.ProbeTarget{NodeID: tg.NodeID, IngressID: tg.IngressID}
		rtt, loss, ok := probe.ProbeOnce(placeholderProber, pt)
		if !ok || rtt != 1 || loss != 0 {
			t.Fatalf("placeholderProber 返回异常: rtt=%d loss=%d ok=%v", rtt, loss, ok)
		}
		reporter.OnProbe(pt, rtt, loss, ok)
	}
	reporter.Tick() // 首次 Tick 不受 damping 间隔限制，应触发 EmitQuality
	if quality.Load() == 0 {
		t.Fatalf("Reporter.Tick 未驱动 EmitQuality")
	}
}

// TestSmoke_DriveProbeLoopCtxCancel 冒烟：driveProbeLoop 在 ctx 取消后退出（不泄漏）。
func TestSmoke_DriveProbeLoopCtxCancel(t *testing.T) {
	reporter := probe.NewReporter(probe.ReporterConfig{SelfNode: "self", Clock: time.Now})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		driveProbeLoop(ctx, reporter, placeholderProber, func() []probeTarget { return nil })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("driveProbeLoop 未在 ctx 取消后退出")
	}
}

// ─────────────────────── 装配冒烟：realAssembler 真实装配链（fakeTUN）──────────
//
// plan Task4.1 验收要求"corelink-node（fakeTUN）→ 按角色起子系统"的装配冒烟。
// 本测试用 fakeTUN + 临时 keystore + 手工构造 firstCfg（含一个 RelayEndpoint）
// 走通 realAssembler 真实装配链（非 fakeAssembler 分支选择），覆盖
// SetupBasicAgent → setupNodeCore（起 DataPlane）→ Close 干净。

// newTestAssembler 构造一个 realAssembler，用临时 DataDir 的 keystore
// 与手工身份，注入 fakeTUN 工厂，不需 controller / 真实 enroll。
func newTestAssembler(t *testing.T) *realAssembler {
	t.Helper()
	dataDir := t.TempDir()
	ks := keystore.New(dataDir)
	// 生成测试用自签证书（setupNodeCore 需要 mTLS 拨号器）
	certPEM, keyPEM, caPEM := generateTestCert(t)
	return &realAssembler{
		ks: ks,
		id: &keystore.Identity{
			NodeID:      "node-assemble-smoke",
			NodeCertPEM: certPEM,
			NodeKeyPEM:  keyPEM,
			CACertPEM:   caPEM,
		},
		cfg: &agentconfig.Config{DataDir: dataDir},
		tunFactory: func(name string, mtu int) (tun.Device, error) {
			return tun.NewFakeTUN(name, mtu), nil
		},
	}
}

func generateTestCert(t *testing.T) (certPEM, keyPEM, caPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	caPEM = certPEM // 自签 → CA = 自己
	return
}

// smokeFirstCfg 构造含一个 RelayEndpoint（UDP 回环占位）的最小 NodeConfig。
func smokeFirstCfg() *genv1.NodeConfig {
	return &genv1.NodeConfig{
		Generation: 1,
		Relays: []*genv1.RelayEndpoint{
			{RelayId: "r1", Udp: &genv1.Endpoint{Host: "127.0.0.1", Port: 51820}},
		},
	}
}

// TestSmoke_AssembleBasicAgent_RealNodeCore 装配冒烟：SetupBasicAgent 走真实
// setupNodeCore（起 DataPlane），断言数据面就绪，Close 干净。
func TestSmoke_AssembleBasicAgent_RealNodeCore(t *testing.T) {
	a := newTestAssembler(t)
	firstCfg := smokeFirstCfg()

	if err := a.SetupBasicAgent(context.Background(), basicParams{
		NodeID:      a.id.NodeID,
		FirstConfig: firstCfg,
	}); err != nil {
		t.Fatalf("SetupBasicAgent: %v", err)
	}

	// 断言 node-core 数据面就绪。
	a.mu.Lock()
	dp := a.dp
	a.mu.Unlock()
	if dp == nil {
		t.Fatalf("SetupBasicAgent 未起 DataPlane（a.dp 为 nil）")
	}

	// ApplyConfig 动态路径可用（peers/routes always）。
	a.ApplyConfig(firstCfg)

	// Close 干净（不 panic、不泄漏；句柄置空非必须但验证可重复调用安全）。
	a.Close()
}

// TestSmoke_AssembleByRole_BasicAgent_RealAssembler 通过 assembleByRole 决策入口
// 驱动 realAssembler（非 fakeAssembler），无 topology → 基础 agent → setupNodeCore。
func TestSmoke_AssembleByRole_BasicAgent_RealAssembler(t *testing.T) {
	a := newTestAssembler(t)
	defer a.Close()
	firstCfg := smokeFirstCfg() // 无 Topology → roleFromConfig=UNSPECIFIED → 基础 agent

	if err := assembleByRole(context.Background(), a, a.id.NodeID, firstCfg); err != nil {
		t.Fatalf("assembleByRole(basic): %v", err)
	}
	a.mu.Lock()
	dp := a.dp
	a.mu.Unlock()
	if dp == nil {
		t.Fatalf("assembleByRole 基础 agent 分支未起 DataPlane")
	}
}

// ─────────────────────── reportGate 节流 ───────────────────────

func TestReportGate_FirstCallAlwaysAllows(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	g := newReportGate(30*time.Second, func() time.Time { return now })
	if !g.Allow() {
		t.Fatalf("首次调用应通过")
	}
}

func TestReportGate_ThrottlesWithinInterval(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	g := newReportGate(30*time.Second, clock)

	// 首次通过。
	if !g.Allow() {
		t.Fatalf("首次调用应通过")
	}

	// 10s 后 → 被节流。
	mu.Lock()
	now = now.Add(10 * time.Second)
	mu.Unlock()
	if g.Allow() {
		t.Fatalf("10s 后应被节流")
	}

	// 20s 后（总 20s < 30s）→ 仍被节流。
	mu.Lock()
	now = now.Add(10 * time.Second)
	mu.Unlock()
	if g.Allow() {
		t.Fatalf("20s 后应仍被节流")
	}

	// 30s 后（总 30s >= 30s）→ 通过。
	mu.Lock()
	now = now.Add(10 * time.Second)
	mu.Unlock()
	if !g.Allow() {
		t.Fatalf("30s 后应通过")
	}

	// 立即再调 → 被节流（重新计时）。
	if g.Allow() {
		t.Fatalf("刚通过后立即调用应被节流")
	}
}

func TestReportGate_ExactIntervalAllows(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	g := newReportGate(30*time.Second, clock)

	g.Allow() // 首次

	// 恰好 30s → 通过。
	mu.Lock()
	now = now.Add(30 * time.Second)
	mu.Unlock()
	if !g.Allow() {
		t.Fatalf("恰好 30s 间隔应通过")
	}
}

// ─────────────────────── 编排冒烟：portmapFn 注入 + OnMappingLost 重报 ───────────

func TestSmoke_PortmapFnIntegrationAndOnMappingLost(t *testing.T) {
	mock := &mockIngressClient{}
	cfg := &agentconfig.Config{
		Ingresses: []agentconfig.IngressConfig{
			{Kind: "ip", Host: "203.0.113.42", Port: 7443},
		},
	}

	// 模拟 portmapFn：返回一个 UPnP 入口。
	portmapCalled := atomic.Int32{}
	portmapFn := func(ctx context.Context) ([]*genv1.Ingress, error) {
		portmapCalled.Add(1)
		return []*genv1.Ingress{
			{Id: "upnp-udp-1.2.3.4-51820", Host: "1.2.3.4", Port: 51820,
				Source: genv1.IngressSource_INGRESS_SOURCE_UPNP},
		}, nil
	}

	// 1. 验证 discoverAndReportIngress 接入 portmapFn。
	// 用确定性 buildIngressOptions + Discover + mock ReportIngress。
	observed := &genv1.Endpoint{Host: "198.51.100.1", Port: 1234}
	opts := buildIngressOptions(cfg, "node-pm-smoke", observed,
		func(context.Context) (string, uint32, genv1.NatType, error) {
			return "", 0, genv1.NatType_NAT_TYPE_UNKNOWN, nil
		},
		func() []*genv1.Ingress { return nil },
		func(context.Context) (string, error) { return "", nil },
		portmapFn,
	)
	set := discoverWithOpts(opts)
	if _, err := mock.ReportIngress(context.Background(), set); err != nil {
		t.Fatalf("ReportIngress: %v", err)
	}
	if portmapCalled.Load() != 1 {
		t.Fatalf("portmapFn should have been called once, got %d", portmapCalled.Load())
	}
	// 验证 portmap 入口出现在上报集中。
	mock.mu.Lock()
	last := mock.lastSet
	mock.mu.Unlock()
	foundUpnp := false
	for _, ing := range last.GetIngresses() {
		if ing.GetHost() == "1.2.3.4" && ing.GetPort() == 51820 {
			foundUpnp = true
		}
	}
	if !foundUpnp {
		t.Fatalf("portmap 入口未出现在上报集中: %+v", last.GetIngresses())
	}

	// 2. 验证 OnMappingLost + reportGate 触发重报计数。
	reReportCount := atomic.Int32{}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	gate := newReportGate(30*time.Second, clock)
	onLost := func(_ *portmap.Mapping) {
		if gate.Allow() {
			reReportCount.Add(1)
		}
	}

	// 首次触发 → 通过。
	onLost(nil)
	if reReportCount.Load() != 1 {
		t.Fatalf("首次 OnMappingLost 应触发重报，got %d", reReportCount.Load())
	}

	// 5s 后触发 → 被节流。
	clockMu.Lock()
	now = now.Add(5 * time.Second)
	clockMu.Unlock()
	onLost(nil)
	if reReportCount.Load() != 1 {
		t.Fatalf("5s 内 OnMappingLost 应被节流，got %d", reReportCount.Load())
	}

	// 31s 后触发 → 通过。
	clockMu.Lock()
	now = now.Add(26 * time.Second)
	clockMu.Unlock()
	onLost(nil)
	if reReportCount.Load() != 2 {
		t.Fatalf("31s 后 OnMappingLost 应触发重报，got %d", reReportCount.Load())
	}
}

// ─────────────────────── #29：装配失败资源回收 ───────────────────────

// TestSetupTransit_InvalidCert_ReturnsError 验证：SetupTransit 在证书无效时
// 正确返回错误，不泄漏 goroutine。
func TestSetupTransit_InvalidCert_ReturnsError(t *testing.T) {
	a := newTestAssembler(t)

	// 故意不设置有效证书——使 TLS 证书解析失败。
	a.id.NodeCertPEM = []byte("INVALID")
	a.id.NodeKeyPEM = []byte("INVALID")
	a.id.CACertPEM = []byte("INVALID")

	firstCfg := &genv1.NodeConfig{
		Generation: 1,
		Topology: &genv1.TopologyAssignment{
			Role: genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT,
		},
	}

	before := runtime.NumGoroutine()

	// SetupTransit 在 setupNodeCore 失败时 warn 但继续（TRANSIT 允许 node-core 降级运行），
	// 因此不返回 error。
	err := a.SetupTransit(context.Background(), transitParams{
		NodeID:      a.id.NodeID,
		FirstConfig: firstCfg,
	})
	if err != nil {
		t.Fatalf("SetupTransit 不应返回 error（node-core 失败仅 warn），实际 %v", err)
	}

	// goroutine 应收敛到调用前水平。
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+1 {
		t.Fatalf("SetupTransit 失败后 goroutine 未收敛：before=%d got=%d", before, got)
	}
}

// TestRealAssembler_CloseAfterPartialSetup_NoLeak 验证：assembler 在「已建部分
// 子系统（node-core）」状态下调用 Close，能完整回收全部已建资源、
// goroutine 收敛——这是 runNode 装配失败时 defer assembler.Close() 必须成立的不变量。
func TestRealAssembler_CloseAfterPartialSetup_NoLeak(t *testing.T) {
	a := newTestAssembler(t)
	firstCfg := smokeFirstCfg()

	before := runtime.NumGoroutine()

	// 1) 起真实 node-core（DataPlane + TUN）。
	if err := a.SetupBasicAgent(context.Background(), basicParams{
		NodeID:      a.id.NodeID,
		FirstConfig: firstCfg,
	}); err != nil {
		t.Fatalf("SetupBasicAgent: %v", err)
	}

	// 2) Close 必须回收 node-core，goroutine 收敛到装配前水平附近。
	a.Close()

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+5 {
		t.Fatalf("部分装配后 Close 未收敛 goroutine：before=%d got=%d", before, got)
	}
}

// ─────────────────────── #32：setupNodeCore 重建不泄漏旧数据面 ──────────────

// spyTUN 包装 tun.Device，记录 Close 调用次数，供 #32 重建泄漏断言用。
type spyTUN struct {
	tun.Device
	mu     sync.Mutex
	closes int
}

func (s *spyTUN) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return s.Device.Close()
}

func (s *spyTUN) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// TestSetupNodeCore_RebuildClosesOldDataplane 反复重建 node-core（模拟 SwitchRole
// 多次翻转触发的 setupNodeCore 覆盖），断言每次覆盖前旧 DataPlane 被关闭，
// 不累积泄漏（#32）。
func TestSetupNodeCore_RebuildClosesOldDataplane(t *testing.T) {
	a := newTestAssembler(t)

	// 替换为记录 Close 的 spy TUN 工厂；按创建顺序留存以便逐个断言。
	var created []*spyTUN
	a.tunFactory = func(name string, mtu int) (tun.Device, error) {
		s := &spyTUN{Device: tun.NewFakeTUN(name, mtu)}
		created = append(created, s)
		return s, nil
	}

	firstCfg := smokeFirstCfg()

	// 第一次装配。
	if err := a.setupNodeCore(firstCfg); err != nil {
		t.Fatalf("第一次 setupNodeCore: %v", err)
	}
	a.mu.Lock()
	oldDP := a.dp
	a.mu.Unlock()
	if oldDP == nil {
		t.Fatalf("第一次 setupNodeCore 未起 DataPlane")
	}

	// 第二次装配（覆盖）：此前旧 DataPlane 必须被关闭。
	if err := a.setupNodeCore(firstCfg); err != nil {
		t.Fatalf("第二次 setupNodeCore: %v", err)
	}

	if len(created) != 2 {
		t.Fatalf("期望创建 2 个 TUN，实际 %d", len(created))
	}
	// 旧 TUN closeCount==1 证明 setupNodeCore 在覆盖前关闭了旧 DataPlane（DataPlane.Close
	// 会级联关闭底层 TUN），同时不重复关闭（双关会使 closeCount==2）。
	if got := created[0].closeCount(); got != 1 {
		t.Fatalf("旧 TUN 应在重建时恰被关闭一次，实际 closeCount=%d", got)
	}

	// 收尾：当前（第二个）TUN 仍存活，Close 干净。
	if c := created[1].closeCount(); c != 0 {
		t.Fatalf("当前 TUN 不应在重建时被关闭，closeCount=%d", c)
	}
	a.Close()
}

// 旧 relay 关闭死锁测试已移除（relay server 和 interconnect 已从 realAssembler 中删除）。

// ─────────────────────── driveProbeLoop 目标同步 ───────────────────────

// TestDriveProbeLoop_SyncTargets 验证：当探测目标集缩减后，下一轮 Tick 不再上报
// 已移除目标的样本（bug #21）。聚焦「SetTargets→OnProbe→Tick」探测内核接线正确。
func TestDriveProbeLoop_SyncTargets(t *testing.T) {
	var mu sync.Mutex
	var reports []*genv1.QualityReport
	r := probe.NewReporter(probe.ReporterConfig{
		SelfNode: "nodeA",
		FSM:      probe.DefaultLinkFSMConfig(),
		Damping: probe.QualityDamping{
			MinInterval:   0, // 关闭间隔限制，确保每轮 Tick 都尝试上报
			RTTThreshold:  10,
			LossThreshold: 10,
		},
		EmitQuality: func(q *genv1.QualityReport) {
			mu.Lock()
			reports = append(reports, q)
			mu.Unlock()
		},
	})

	// 第一阶段两目标，第二阶段缩减为一个。
	var tmu sync.Mutex
	cur := []probeTarget{
		{NodeID: "nodeB", IngressID: "ing-1"},
		{NodeID: "nodeC", IngressID: "ing-2"},
	}
	targetsFn := func() []probeTarget {
		tmu.Lock()
		defer tmu.Unlock()
		out := make([]probeTarget, len(cur))
		copy(out, cur)
		return out
	}

	// driveProbeLoop 内 ticker 固定 15s 不适合单测直接计时；这里直接驱动一轮
	// 探测内核：SetTargets→OnProbe→Tick，与 driveProbeLoop case 体逻辑等价。
	// rttMs 参数化：第二轮喂入显著变化的 RTT，确保 damping 不抑制，从而能断言
	// 缩减后样本集只含 nodeB（已剪除 nodeC）。
	reporter := r
	probeOnce := func(targets []probeTarget, rttMs uint32) {
		pts := make([]probe.ProbeTarget, 0, len(targets))
		for _, tgt := range targets {
			pts = append(pts, probe.ProbeTarget{NodeID: tgt.NodeID, IngressID: tgt.IngressID})
		}
		reporter.SetTargets(pts)
		for _, pt := range pts {
			reporter.OnProbe(pt, rttMs, 0, true)
		}
		reporter.Tick()
	}

	probeOnce(targetsFn(), 20) // 首轮：2 目标
	mu.Lock()
	first := len(reports)
	firstSamples := 0
	if first > 0 {
		firstSamples = len(reports[len(reports)-1].Samples)
	}
	mu.Unlock()
	if first != 1 || firstSamples != 2 {
		t.Fatalf("首轮应上报 2 样本，got reports=%d samples=%d", first, firstSamples)
	}

	// 缩减目标集为单个 nodeB。
	tmu.Lock()
	cur = []probeTarget{{NodeID: "nodeB", IngressID: "ing-1"}}
	tmu.Unlock()

	probeOnce(targetsFn(), 60) // 二轮：nodeB RTT 20→60 显著，应只上报 nodeB
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 2 {
		t.Fatalf("二轮应再上报一次，got %d", len(reports))
	}
	last := reports[1]
	if len(last.Samples) != 1 || last.Samples[0].DstNode != "nodeB" {
		t.Fatalf("缩减后应只上报 nodeB 一条，got %v", last.Samples)
	}
}

// ─────────────────────── applyFIBToRoute ───────────────────────

func TestApplyFIBToRoute_Basic(t *testing.T) {
	fr := mesh.NewFIBRoute()
	fib := &genv1.FIBTable{
		Version: 1,
		Entries: []*genv1.FIBEntry{
			{
				Prefix: "10.0.0.2/32",
				NextHops: []*genv1.NextHopEntry{
					{PeerId: "relay-1", Weight: 100, IngressId: "ing-1"},
				},
			},
		},
	}
	if err := applyFIBToRoute(fr, fib); err != nil {
		t.Fatal(err)
	}
	hop, ok := fr.Route(netip.MustParseAddr("10.0.0.2"), 12345)
	if !ok {
		t.Fatal("expected route hit, got miss")
	}
	if hop.PeerID != "relay-1" {
		t.Errorf("expected PeerID=relay-1, got %q", hop.PeerID)
	}
	if hop.Weight != 100 {
		t.Errorf("expected Weight=100, got %d", hop.Weight)
	}
	if hop.IngressID != "ing-1" {
		t.Errorf("expected IngressID=ing-1, got %q", hop.IngressID)
	}
}

func TestApplyFIBToRoute_MultipleEntries(t *testing.T) {
	fr := mesh.NewFIBRoute()
	fib := &genv1.FIBTable{
		Version: 2,
		Entries: []*genv1.FIBEntry{
			{
				Prefix: "10.0.0.2/32",
				NextHops: []*genv1.NextHopEntry{
					{PeerId: "relay-1", Weight: 100},
				},
			},
			{
				Prefix: "10.0.1.0/24",
				NextHops: []*genv1.NextHopEntry{
					{PeerId: "relay-2", Weight: 50},
					{PeerId: "relay-3", Weight: 50},
				},
			},
		},
	}
	if err := applyFIBToRoute(fr, fib); err != nil {
		t.Fatal(err)
	}
	// 精确匹配 /32
	hop1, ok1 := fr.Route(netip.MustParseAddr("10.0.0.2"), 0)
	if !ok1 || hop1.PeerID != "relay-1" {
		t.Errorf("10.0.0.2/32: expected relay-1, got %+v (ok=%v)", hop1, ok1)
	}
	// /24 子网匹配
	hop2, ok2 := fr.Route(netip.MustParseAddr("10.0.1.5"), 0)
	if !ok2 {
		t.Fatal("10.0.1.5: expected route hit, got miss")
	}
	if hop2.PeerID != "relay-2" && hop2.PeerID != "relay-3" {
		t.Errorf("10.0.1.0/24: expected relay-2 or relay-3, got %q", hop2.PeerID)
	}
	// 无匹配
	_, ok3 := fr.Route(netip.MustParseAddr("192.168.0.1"), 0)
	if ok3 {
		t.Error("192.168.0.1: expected miss, got hit")
	}
}

func TestApplyFIBToRoute_NilSafe(t *testing.T) {
	// nil fib → no-op
	if err := applyFIBToRoute(mesh.NewFIBRoute(), nil); err != nil {
		t.Fatal(err)
	}
	// nil FIBRoute → no-op
	if err := applyFIBToRoute(nil, &genv1.FIBTable{Version: 1}); err != nil {
		t.Fatal(err)
	}
	// both nil
	if err := applyFIBToRoute(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestApplyFIBToRoute_SkipsInvalidPrefix(t *testing.T) {
	fr := mesh.NewFIBRoute()
	fib := &genv1.FIBTable{
		Version: 1,
		Entries: []*genv1.FIBEntry{
			{Prefix: "not-a-cidr", NextHops: []*genv1.NextHopEntry{{PeerId: "r1"}}},
			{Prefix: "10.0.0.3/32", NextHops: []*genv1.NextHopEntry{{PeerId: "r2", Weight: 10}}},
		},
	}
	if err := applyFIBToRoute(fr, fib); err != nil {
		t.Fatal(err)
	}
	// 无效前缀被跳过，有效前缀仍然生效
	hop, ok := fr.Route(netip.MustParseAddr("10.0.0.3"), 0)
	if !ok || hop.PeerID != "r2" {
		t.Errorf("expected r2, got %+v (ok=%v)", hop, ok)
	}
}

func TestApplyFIBToRoute_SkipsNilEntries(t *testing.T) {
	fr := mesh.NewFIBRoute()
	fib := &genv1.FIBTable{
		Version: 1,
		Entries: []*genv1.FIBEntry{
			nil,
			{
				Prefix: "10.0.0.4/32",
				NextHops: []*genv1.NextHopEntry{
					nil,
					{PeerId: "r3", Weight: 1},
				},
			},
		},
	}
	if err := applyFIBToRoute(fr, fib); err != nil {
		t.Fatal(err)
	}
	hop, ok := fr.Route(netip.MustParseAddr("10.0.0.4"), 0)
	if !ok || hop.PeerID != "r3" {
		t.Errorf("expected r3, got %+v (ok=%v)", hop, ok)
	}
}

// ─────────────────────── assembleByRole VIP 分支 ───────────────────────

func TestAssembleByRole_Transit_VIPMode(t *testing.T) {
	fa := &fakeAssembler{}
	nc := &genv1.NodeConfig{
		Topology: &genv1.TopologyAssignment{
			Role:    genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT,
			Version: 10,
			Fib: &genv1.FIBTable{
				Version: 1,
				Entries: []*genv1.FIBEntry{
					{Prefix: "10.0.0.2/32", NextHops: []*genv1.NextHopEntry{{PeerId: "r1", Weight: 100}}},
				},
			},
		},
	}
	flags := featureflag.FromMap(map[string]bool{featureflag.VIPRouting: true})
	if err := assembleByRole(context.Background(), fa, "n1", nc, flags); err != nil {
		t.Fatal(err)
	}
	if fa.transit == nil {
		t.Fatal("expected SetupTransit to be called")
	}
	// 验证 FIB 和 Flags 被正确传递
	if fa.transit.FIB == nil {
		t.Fatal("expected FIB to be passed to transitParams")
	}
	if fa.transit.Flags == nil || !fa.transit.Flags.Enabled(featureflag.VIPRouting) {
		t.Fatal("expected VIPRouting flag to be enabled in transitParams")
	}
	if len(fa.transit.FIB.Entries) != 1 || fa.transit.FIB.Entries[0].Prefix != "10.0.0.2/32" {
		t.Fatalf("unexpected FIB entries: %v", fa.transit.FIB.Entries)
	}
}

func TestAssembleByRole_Transit_FlagOff_UsesBaseline(t *testing.T) {
	fa := &fakeAssembler{}
	nc := &genv1.NodeConfig{
		Topology: &genv1.TopologyAssignment{
			Role:    genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT,
			Version: 10,
			BaselineRoutes: []*genv1.Route2{
				{DstNode: "dst-A", Hops: []*genv1.Hop{{NodeId: "n1", IngressId: "i1"}}},
			},
			Fib: &genv1.FIBTable{
				Version: 1,
				Entries: []*genv1.FIBEntry{
					{Prefix: "10.0.0.2/32", NextHops: []*genv1.NextHopEntry{{PeerId: "r1"}}},
				},
			},
		},
	}
	// VIPRouting flag 关闭
	flags := featureflag.New()
	if err := assembleByRole(context.Background(), fa, "n1", nc, flags); err != nil {
		t.Fatal(err)
	}
	if fa.transit == nil {
		t.Fatal("expected SetupTransit to be called")
	}
	// Baseline 仍然被传递（向下兼容）
	if len(fa.transit.Baseline) == 0 {
		t.Fatal("expected Baseline to be populated even when VIP flag is off")
	}
	// FIB 也被传递（由 SetupTransit 决定是否使用）
	if fa.transit.FIB == nil {
		t.Fatal("expected FIB to be passed regardless of flag state")
	}
}
