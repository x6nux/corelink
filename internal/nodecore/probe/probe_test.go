package probe

import (
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// TestProbeOnce_InjectedProber 验证 ProbeOnce 透传注入 Prober 的结果。
func TestProbeOnce_InjectedProber(t *testing.T) {
	called := ""
	var p Prober = func(ingressID string) (uint32, uint32, bool) {
		called = ingressID
		return 33, 7, true
	}
	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-xyz"}
	rtt, loss, ok := ProbeOnce(p, tgt)
	if called != "ing-xyz" {
		t.Fatalf("Prober 应以 IngressID 调用，got %q", called)
	}
	if rtt != 33 || loss != 7 || !ok {
		t.Fatalf("结果透传错误：%d/%d/%v", rtt, loss, ok)
	}
}

func TestProbeTarget_Key_Unique(t *testing.T) {
	a := ProbeTarget{NodeID: "n", IngressID: "i"}
	b := ProbeTarget{NodeID: "n", IngressID: "j"}
	c := ProbeTarget{NodeID: "n", IngressID: "i"}
	if a.Key() == b.Key() {
		t.Fatalf("不同 ingress 的 key 不应相同")
	}
	if a.Key() != c.Key() {
		t.Fatalf("相同 target 的 key 应相同")
	}
}

// TestEndToEnd_ProberDrivesReporter 用 fake Prober 周期驱动 Reporter，验证整条链路。
func TestEndToEnd_ProberDrivesReporter(t *testing.T) {
	clk := newFakeClock()
	var events []*genv1.EdgeEvent
	r := NewReporter(ReporterConfig{
		SelfNode:  "nodeA",
		FSM:       testCfg(),
		Damping:   DefaultQualityDamping(),
		Clock:     clk.Now,
		EmitEvent: func(e *genv1.EdgeEvent) { events = append(events, e) },
	})

	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}

	// fake Prober：前若干次健康，之后持续超时（ok=false）模拟断连。
	round := 0
	var p Prober = func(ingressID string) (uint32, uint32, bool) {
		round++
		if round <= 3 {
			return 20, 0, true
		}
		return 0, 0, false
	}

	// 周期调度：探测 → 喂 Reporter。
	for i := 0; i < 8; i++ {
		clk.Advance(5 * time.Second)
		rtt, loss, ok := ProbeOnce(p, tgt)
		r.OnProbe(tgt, rtt, loss, ok)
	}

	if len(events) != 1 || events[0].Kind != genv1.EdgeEventKind_EDGE_EVENT_KIND_DOWN {
		t.Fatalf("端到端应产生 1 个 Down 事件，got %v", events)
	}
	if r.State(tgt) != Down {
		t.Fatalf("最终状态应 Down，got %v", r.State(tgt))
	}
}
