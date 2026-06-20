package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// 测试 GET /admin/api/topology 与 PUT /admin/api/nodes/{id}/geo。
//
// fakeTopology 是 TopologyIface 的最小内存实现：可控的定位/路由快照。
type fakeTopology struct {
	geo    []*genv1.NodeGeo
	routes []*genv1.RouteReport
	setGot *genv1.NodeGeo // 记录 SetNodeGeo 最近一次入参
}

func (f *fakeTopology) AllNodeGeo() []*genv1.NodeGeo          { return f.geo }
func (f *fakeTopology) AllRouteReports() []*genv1.RouteReport { return f.routes }
func (f *fakeTopology) SetNodeGeo(g *genv1.NodeGeo)           { f.setGot = g }

// TestTopologyDTOJSONTags 验证 DTO 的 JSON 键名（前端契约）。
func TestTopologyDTOJSONTags(t *testing.T) {
	dto := topologyDTO{
		Nodes: []topoNodeDTO{{
			ID: "n1", Name: "host", VIP: "100.64.0.1/32", Online: true,
			Latitude: 1.1, Longitude: 2.2, City: "SH", Country: "CN",
			Accuracy: "ip", CFRttMs: 12.5, ColIATA: "PVG",
		}},
		PhysicalEdges: []topoEdgeDTO{{Src: "n1", Dst: "n2"}},
		ActiveRoutes:  []topoRouteDTO{{Src: "n1", Dst: "n2", NextHop: "n2", RttMs: 10}},
	}
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nodes", "physical_edges", "active_routes"} {
		if _, ok := m[key]; !ok {
			t.Errorf("topologyDTO 缺少键 %q", key)
		}
	}
	node0, _ := m["nodes"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "name", "vip", "online", "lat", "lon", "city", "country", "accuracy", "cf_rtt_ms"} {
		if _, ok := node0[key]; !ok {
			t.Errorf("topoNodeDTO 缺少键 %q", key)
		}
	}
	// ColIATA omitempty，非空时应出现。
	if _, ok := node0["col_iata"]; !ok {
		t.Errorf("topoNodeDTO 非空 col_iata 应序列化")
	}
}

// TestHandleGetTopologyAggregation 验证聚合：节点 + 定位 + 路由 + 物理边。
func TestHandleGetTopologyAggregation(t *testing.T) {
	h := newHarness(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")
	seedNode(t, h.st, "n2", "100.64.0.3/32")
	h.online.online["n1"] = true

	topo := &fakeTopology{
		geo: []*genv1.NodeGeo{{
			NodeId: "n1", Latitude: 31.23, Longitude: 121.47,
			City: "Shanghai", Country: "CN", Accuracy: "ip", CfRttMs: 8.5, ColoIata: "PVG",
		}},
		routes: []*genv1.RouteReport{{
			SrcNodeId: "n1",
			Routes: []*genv1.RouteHop{{
				DstNodeId: "n2", NextHopId: "n2", RttMs: 12,
				Ranked: []string{"n2"},
			}},
		}},
	}
	deps := h.srv.deps
	deps.Topology = topo
	h.srv = NewAdminServer(deps)

	rec := h.do(http.MethodGet, "/admin/api/topology", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[topologyDTO](t, rec)

	if len(resp.Nodes) != 2 {
		t.Fatalf("nodes len = %d, 期望 2", len(resp.Nodes))
	}
	// 定位关联：找到 n1，验证 lat/city。
	var n1 *topoNodeDTO
	for i := range resp.Nodes {
		if resp.Nodes[i].ID == "n1" {
			n1 = &resp.Nodes[i]
		}
	}
	if n1 == nil {
		t.Fatal("响应缺少 n1")
	}
	if !n1.Online {
		t.Error("n1 应在线")
	}
	if n1.Latitude != 31.23 || n1.City != "Shanghai" || n1.ColIATA != "PVG" {
		t.Errorf("n1 定位未关联: %+v", n1)
	}

	// active_routes：应有 1 条 n1->n2。
	if len(resp.ActiveRoutes) != 1 {
		t.Fatalf("active_routes len = %d, 期望 1", len(resp.ActiveRoutes))
	}
	ar := resp.ActiveRoutes[0]
	if ar.Src != "n1" || ar.Dst != "n2" || ar.NextHop != "n2" || ar.RttMs != 12 {
		t.Errorf("active_route 内容错误: %+v", ar)
	}

	// physical_edges：ranked 含 n2，应有一条 n1->n2。
	if len(resp.PhysicalEdges) != 1 {
		t.Fatalf("physical_edges len = %d, 期望 1", len(resp.PhysicalEdges))
	}
	pe := resp.PhysicalEdges[0]
	if pe.Src != "n1" || pe.Dst != "n2" {
		t.Errorf("physical_edge 内容错误: %+v", pe)
	}
}

// TestHandleGetTopologyNilTopology 验证 Topology 为 nil 时仅返回 nodes（不 panic）。
func TestHandleGetTopologyNilTopology(t *testing.T) {
	h := newHarness(t)
	seedNode(t, h.st, "n1", "100.64.0.2/32")

	rec := h.do(http.MethodGet, "/admin/api/topology", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[topologyDTO](t, rec)
	if len(resp.Nodes) != 1 {
		t.Errorf("nodes len = %d, 期望 1", len(resp.Nodes))
	}
	if len(resp.ActiveRoutes) != 0 || len(resp.PhysicalEdges) != 0 {
		t.Errorf("nil Topology 时 routes/edges 应为空: %+v", resp)
	}
}

// TestHandleSetNodeGeoSuccess 验证手动修正坐标成功路径。
func TestHandleSetNodeGeoSuccess(t *testing.T) {
	h := newHarness(t)
	topo := &fakeTopology{}
	deps := h.srv.deps
	deps.Topology = topo
	h.srv = NewAdminServer(deps)

	body := map[string]any{"lat": 39.9, "lon": 116.4, "city": "Beijing"}
	rec := h.do(http.MethodPut, "/admin/api/nodes/n1/geo", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if topo.setGot == nil {
		t.Fatal("SetNodeGeo 未被调用")
	}
	if topo.setGot.NodeId != "n1" {
		t.Errorf("NodeId = %q, 期望 n1", topo.setGot.NodeId)
	}
	if topo.setGot.Latitude != 39.9 || topo.setGot.Longitude != 116.4 {
		t.Errorf("坐标错误: %+v", topo.setGot)
	}
	if topo.setGot.City != "Beijing" {
		t.Errorf("City = %q", topo.setGot.City)
	}
	if topo.setGot.Accuracy != "manual" {
		t.Errorf("Accuracy = %q, 期望 manual", topo.setGot.Accuracy)
	}
}

// TestHandleSetNodeGeoNilTopology 验证 Topology 为 nil 时返回 500。
func TestHandleSetNodeGeoNilTopology(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"lat": 1.0, "lon": 2.0, "city": "X"}
	rec := h.do(http.MethodPut, "/admin/api/nodes/n1/geo", body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, 期望 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleSetNodeGeoBadJSON 验证请求体解析失败返回 400。
func TestHandleSetNodeGeoBadJSON(t *testing.T) {
	h := newHarness(t)
	topo := &fakeTopology{}
	deps := h.srv.deps
	deps.Topology = topo
	h.srv = NewAdminServer(deps)

	// 直接构造非法 JSON 请求体（绕过 harness.do 的自动 encode）。
	req := httptest.NewRequest(http.MethodPut, "/admin/api/nodes/n1/geo", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, 期望 400; body=%s", rec.Code, rec.Body.String())
	}
	if topo.setGot != nil {
		t.Error("解析失败时不应调用 SetNodeGeo")
	}
}
