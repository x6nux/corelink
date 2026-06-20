package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/x6nux/corelink/internal/controller/config"
	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// testConfig 构造一个用临时 sqlite 文件库的最小配置（回环、固定 32 字节 enc key）。
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "corelink-test.db")
	key := make([]byte, 32) // 固定零密钥（测试用，buildController 不再随机）
	return &config.Config{
		DBDSN:          "sqlite://" + dbPath,
		GRPCEnrollAddr: "127.0.0.1:0",
		GRPCAddr:       "127.0.0.1:0",
		HTTPAddr:       "127.0.0.1:0",
		VirtualCIDR:    "100.64.0.0/10",
		CASubject:      "Test CA",
		TLSMode:        "self-signed",
		SelfSignedHost: "localhost",
		CAEncKey:       key,
	}
}

// fakeTLSState 构造带指定 CN 的 mTLS 连接状态（ConfigHTTP nodeID 提取）。
func fakeTLSState(nodeID string) *tls.ConnectionState {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	cert, _ := x509.ParseCertificate(der)
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
}

// seedNode 在 store 写入一个节点（ConfigHTTP.buildNodeConfig 需要 GetNode）。
func seedNode(t *testing.T, st *store.Store, id, vip, wg string) {
	t.Helper()
	if err := st.CreateNode(&store.Node{
		ID: id, Role: "node", WGPubKey: wg, VirtualIP: vip, User: "u", Generation: 1,
	}); err != nil {
		t.Fatalf("CreateNode(%s): %v", id, err)
	}
}

// feedTopology 给 Receiver 喂两个高置信节点 + 双向质量，使优化器能产出分配。
func feedTopology(t *testing.T, c *controllerComponents) {
	t.Helper()
	sets := []*genv1.IngressSet{
		{NodeId: "node-a", Ingresses: []*genv1.Ingress{
			{Id: "a1", Host: "10.0.0.1", Port: 443, Confidence: 90, NatType: genv1.NatType_NAT_TYPE_FULL_CONE},
		}},
		{NodeId: "node-b", Ingresses: []*genv1.Ingress{
			{Id: "b1", Host: "10.0.0.2", Port: 443, Confidence: 90, NatType: genv1.NatType_NAT_TYPE_FULL_CONE},
		}},
	}
	for _, s := range sets {
		if _, err := c.receiver.ReportIngress(nil, s); err != nil {
			t.Fatalf("ReportIngress: %v", err)
		}
	}
	reports := []*genv1.QualityReport{
		{SrcNode: "node-a", Samples: []*genv1.EdgeSample{{DstNode: "node-b", IngressId: "b1", RttMs: 10}}},
		{SrcNode: "node-b", Samples: []*genv1.EdgeSample{{DstNode: "node-a", IngressId: "a1", RttMs: 10}}},
	}
	for _, q := range reports {
		if _, err := c.receiver.ReportQuality(nil, q); err != nil {
			t.Fatalf("ReportQuality: %v", err)
		}
	}
}

func TestBuildController_SmokeAssembly(t *testing.T) {
	cfg := testConfig(t)
	c, err := buildController(cfg)
	if err != nil {
		t.Fatalf("buildController: %v", err)
	}
	defer c.Close()

	seedNode(t, c.st, "node-a", "100.64.0.1/32", "wg-a")
	seedNode(t, c.st, "node-b", "100.64.0.2/32", "wg-b")

	feedTopology(t, c)

	// 手工触发全量重算（version=1）。
	if err := c.topoSvc.Recompute(1); err != nil {
		t.Fatalf("Recompute: %v", err)
	}

	// AssignmentForNode 应对两个节点产出分配。
	asgA, ok := c.topoSvc.AssignmentForNode("node-a")
	if !ok || asgA == nil {
		t.Fatalf("node-a 应有拓扑分配")
	}
	if asgA.Version != 1 {
		t.Errorf("node-a 分配版本应为 1，got %d", asgA.Version)
	}
	if _, ok := c.topoSvc.AssignmentForNode("node-b"); !ok {
		t.Fatalf("node-b 应有拓扑分配")
	}

	// 节点拉 /v1/config 应含 Topology（assignmentFn 注入路径）。
	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState("node-a")
	w := httptest.NewRecorder()
	c.cfgSvcs.HTTPHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/v1/config 期望 200，got %d\n%s", w.Code, w.Body.String())
	}
	var nc genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &nc); err != nil {
		t.Fatalf("解码 NodeConfig: %v", err)
	}
	if nc.Topology == nil {
		t.Fatalf("/v1/config 应含 Topology")
	}
	if nc.Topology.Version != 1 {
		t.Errorf("下发 Topology 版本应为 1，got %d", nc.Topology.Version)
	}
}

func TestBuildController_RestartLoadsPersistedTopology(t *testing.T) {
	cfg := testConfig(t) // 同一 DBDSN 复用（temp file）。

	// ── 第一次启动：喂拓扑 + 重算到版本 5 + 持久化 ──
	c1, err := buildController(cfg)
	if err != nil {
		t.Fatalf("buildController#1: %v", err)
	}
	seedNode(t, c1.st, "node-a", "100.64.0.1/32", "wg-a")
	seedNode(t, c1.st, "node-b", "100.64.0.2/32", "wg-b")
	feedTopology(t, c1)
	if err := c1.topoSvc.Recompute(5); err != nil {
		t.Fatalf("Recompute#1: %v", err)
	}
	asg1, ok := c1.topoSvc.AssignmentForNode("node-a")
	if !ok {
		t.Fatalf("启动#1 node-a 应有分配")
	}
	if asg1.Version != 5 {
		t.Fatalf("启动#1 版本应为 5，got %d", asg1.Version)
	}
	c1.Close()

	// ── 重启：重开同库 → buildController 内 Load() 应加载持久化结果，立即服务 ──
	c2, err := buildController(cfg)
	if err != nil {
		t.Fatalf("buildController#2: %v", err)
	}
	defer c2.Close()

	// 无需重算：Load() 已重建 assignments，AssignmentForNode 立即返回。
	asg2, ok := c2.topoSvc.AssignmentForNode("node-a")
	if !ok || asg2 == nil {
		t.Fatalf("重启后 node-a 应立即有分配（持久化加载即服务）")
	}
	if asg2.Version != 5 {
		t.Errorf("重启后版本应延续为 5，got %d", asg2.Version)
	}

	// 版本延续：重启后节点重新上报入口/质量（receiver 内存态不持久化），下一次重算
	// 应推进到 >5（nextVersion 不回退）。
	feedTopology(t, c2)
	if err := c2.topoSvc.Recompute(6); err != nil {
		t.Fatalf("Recompute#2: %v", err)
	}
	asg3, _ := c2.topoSvc.AssignmentForNode("node-a")
	if asg3.Version != 6 {
		t.Errorf("重启后重算版本应为 6，got %d", asg3.Version)
	}
}

func TestEdgeEventSink_DrivesOnEvent(t *testing.T) {
	cfg := testConfig(t)
	c, err := buildController(cfg)
	if err != nil {
		t.Fatalf("buildController: %v", err)
	}
	defer c.Close()
	seedNode(t, c.st, "node-a", "100.64.0.1/32", "wg-a")
	seedNode(t, c.st, "node-b", "100.64.0.2/32", "wg-b")
	feedTopology(t, c)

	// 建立增量基线。
	if err := c.topoSvc.Recompute(1); err != nil {
		t.Fatalf("Recompute: %v", err)
	}

	// 通过 Receiver 收边事件 → sink → TopoService.OnEvent。
	// damping MinInterval=2s，刚重算过，OnEvent 仅入队不立即重算——断言不 panic 且
	// 接线路径连通（PutEdgeEvent 被调用）。
	ev := &genv1.EdgeEvent{
		SrcNode: "node-a", DstNode: "node-b", IngressId: "b1",
		Kind: genv1.EdgeEventKind_EDGE_EVENT_KIND_DEGRADED, RttMs: 200,
	}
	if _, err := c.receiver.ReportEdgeEvent(nil, ev); err != nil {
		t.Fatalf("ReportEdgeEvent: %v", err)
	}
	// 接线连通即可（增量实际效果由 topology 包单测覆盖）。
}
