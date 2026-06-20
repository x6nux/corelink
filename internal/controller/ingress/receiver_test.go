package ingress

import (
	"context"
	"net"
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/grpc/peer"
)

// fakeSink records every report handed off by the Receiver so tests can assert
// the downstream forwarding happened.
type fakeSink struct {
	ingress  []*genv1.IngressSet
	quality  []*genv1.QualityReport
	events   []*genv1.EdgeEvent
	machines []*genv1.MachineSpec
}

func (f *fakeSink) PutIngressSet(s *genv1.IngressSet)   { f.ingress = append(f.ingress, s) }
func (f *fakeSink) PutQuality(q *genv1.QualityReport)   { f.quality = append(f.quality, q) }
func (f *fakeSink) PutEdgeEvent(e *genv1.EdgeEvent)     { f.events = append(f.events, e) }
func (f *fakeSink) PutMachineSpec(m *genv1.MachineSpec) { f.machines = append(f.machines, m) }

func TestReportIngressStoresAndQueries(t *testing.T) {
	r := New(nil)
	set := &genv1.IngressSet{NodeId: "node-a"}

	ack, err := r.ReportIngress(context.Background(), set)
	if err != nil {
		t.Fatalf("ReportIngress error: %v", err)
	}
	if ack == nil || !ack.Ok {
		t.Fatalf("expected ok ack, got %+v", ack)
	}

	got, ok := r.GetIngressSet("node-a")
	if !ok {
		t.Fatalf("GetIngressSet(node-a) not found")
	}
	if got != set {
		t.Fatalf("GetIngressSet returned %p, want %p", got, set)
	}

	all := r.AllIngressSets()
	if len(all) != 1 || all[0] != set {
		t.Fatalf("AllIngressSets = %+v, want [%p]", all, set)
	}

	if _, ok := r.GetIngressSet("missing"); ok {
		t.Fatalf("GetIngressSet(missing) unexpectedly found")
	}
}

func TestReportQualityAndEdgeEventStore(t *testing.T) {
	r := New(nil)

	q := &genv1.QualityReport{SrcNode: "node-q"}
	if _, err := r.ReportQuality(context.Background(), q); err != nil {
		t.Fatalf("ReportQuality error: %v", err)
	}
	r.mu.RLock()
	if r.quality["node-q"] != q {
		r.mu.RUnlock()
		t.Fatalf("quality not stored under src_node")
	}
	r.mu.RUnlock()

	ev := &genv1.EdgeEvent{SrcNode: "node-e"}
	if _, err := r.ReportEdgeEvent(context.Background(), ev); err != nil {
		t.Fatalf("ReportEdgeEvent error: %v", err)
	}
	r.mu.RLock()
	if r.edgeEvents["node-e"] != ev {
		r.mu.RUnlock()
		t.Fatalf("edge event not stored under src_node")
	}
	r.mu.RUnlock()
}

func TestSinkForwarding(t *testing.T) {
	sink := &fakeSink{}
	r := New(sink)

	set := &genv1.IngressSet{NodeId: "n"}
	q := &genv1.QualityReport{SrcNode: "n"}
	ev := &genv1.EdgeEvent{SrcNode: "n"}

	_, _ = r.ReportIngress(context.Background(), set)
	_, _ = r.ReportQuality(context.Background(), q)
	_, _ = r.ReportEdgeEvent(context.Background(), ev)

	if len(sink.ingress) != 1 || sink.ingress[0] != set {
		t.Fatalf("sink did not receive ingress set: %+v", sink.ingress)
	}
	if len(sink.quality) != 1 || sink.quality[0] != q {
		t.Fatalf("sink did not receive quality report: %+v", sink.quality)
	}
	if len(sink.events) != 1 || sink.events[0] != ev {
		t.Fatalf("sink did not receive edge event: %+v", sink.events)
	}
}

func TestNilSinkDoesNotPanic(t *testing.T) {
	r := New(nil) // explicitly nil sink
	// Each call must not panic and must still store in memory.
	if _, err := r.ReportIngress(context.Background(), &genv1.IngressSet{NodeId: "x"}); err != nil {
		t.Fatalf("ReportIngress: %v", err)
	}
	if _, err := r.ReportQuality(context.Background(), &genv1.QualityReport{SrcNode: "x"}); err != nil {
		t.Fatalf("ReportQuality: %v", err)
	}
	if _, err := r.ReportEdgeEvent(context.Background(), &genv1.EdgeEvent{SrcNode: "x"}); err != nil {
		t.Fatalf("ReportEdgeEvent: %v", err)
	}
}

func TestObserveSourceReturnsPeerAddr(t *testing.T) {
	r := New(nil)

	fakeAddr := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51234}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: fakeAddr})

	src, err := r.ObserveSource(ctx, &genv1.ObserveRequest{})
	if err != nil {
		t.Fatalf("ObserveSource error: %v", err)
	}
	if src.Host != "203.0.113.7" {
		t.Fatalf("ObserveSource host = %q, want 203.0.113.7", src.Host)
	}
	if src.Port != 51234 {
		t.Fatalf("ObserveSource port = %d, want 51234", src.Port)
	}
}

func TestObserveSourceNoPeerErrors(t *testing.T) {
	r := New(nil)
	if _, err := r.ObserveSource(context.Background(), &genv1.ObserveRequest{}); err == nil {
		t.Fatalf("expected error when no peer in context")
	}
}

func TestReceiver_ReportMachineSpec(t *testing.T) {
	r := New(nil) // sink 可 nil（内存模式）
	_, err := r.ReportMachineSpec(context.Background(), &genv1.MachineSpec{
		NodeId: "n1", Cpus: 4, MemoryMb: 8192, LoadPermille: 1500,
	})
	if err != nil {
		t.Fatalf("ReportMachineSpec: %v", err)
	}
	if got := r.MachineSpec("n1"); got == nil || got.Cpus != 4 {
		t.Fatalf("未存储: %+v", got)
	}
}

func TestMachineSpecSinkForwarding(t *testing.T) {
	sink := &fakeSink{}
	r := New(sink)
	spec := &genv1.MachineSpec{NodeId: "n", Cpus: 8}
	if _, err := r.ReportMachineSpec(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(sink.machines) != 1 || sink.machines[0] != spec {
		t.Fatalf("sink 未收到 MachineSpec: %+v", sink.machines)
	}
}

func TestReportNodeGeoStoresAndQueries(t *testing.T) {
	r := New(nil)
	geo := &genv1.NodeGeo{NodeId: "101", Latitude: 22.3, Longitude: 113.9, City: "Hong Kong", Accuracy: "high"}
	if _, err := r.ReportNodeGeo(context.Background(), geo); err != nil {
		t.Fatalf("ReportNodeGeo: %v", err)
	}
	got, ok := r.GetNodeGeo("101")
	if !ok || got.Latitude != 22.3 {
		t.Errorf("GetNodeGeo 失败: %v ok=%v", got, ok)
	}
	// AllNodeGeo
	all := r.AllNodeGeo()
	if len(all) != 1 || all[0].NodeId != "101" {
		t.Errorf("AllNodeGeo: %+v", all)
	}
}

func TestReportRoutesStoresAndQueries(t *testing.T) {
	r := New(nil)
	rep := &genv1.RouteReport{SrcNodeId: "101", Routes: []*genv1.RouteHop{
		{DstNodeId: "102", NextHopId: "102", RttMs: 5, Ranked: []string{"102", "103"}},
	}}
	if _, err := r.ReportRoutes(context.Background(), rep); err != nil {
		t.Fatalf("ReportRoutes: %v", err)
	}
	all := r.AllRouteReports()
	if len(all) != 1 || all[0].SrcNodeId != "101" {
		t.Errorf("AllRouteReports 失败: %+v", all)
	}
}
