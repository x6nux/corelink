package probe

import (
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

func reporterTestCfg() LinkFSMConfig { return testCfg() }

func reporterDamping() QualityDamping {
	return QualityDamping{
		MinInterval:   30 * time.Second,
		RTTThreshold:  10,
		LossThreshold: 10,
	}
}

// newTestReporter 返回 Reporter + clock + 收集到的事件/质量切片指针。
func newTestReporter() (*Reporter, *fakeClock, *[]*genv1.EdgeEvent, *[]*genv1.QualityReport) {
	clk := newFakeClock()
	var events []*genv1.EdgeEvent
	var quality []*genv1.QualityReport
	r := NewReporter(ReporterConfig{
		SelfNode: "nodeA",
		FSM:      reporterTestCfg(),
		Damping:  reporterDamping(),
		Clock:    clk.Now,
		EmitEvent: func(e *genv1.EdgeEvent) {
			events = append(events, e)
		},
		EmitQuality: func(q *genv1.QualityReport) {
			quality = append(quality, q)
		},
	})
	return r, clk, &events, &quality
}

func TestReporter_EmitEvent_DownFields(t *testing.T) {
	r, clk, events, _ := newTestReporter()
	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}

	// 基线。
	r.OnProbe(tgt, 20, 0, true)

	// 连续失败 → Down。
	for i := 0; i < reporterTestCfg().DownConfirm; i++ {
		clk.Advance(5 * time.Second)
		r.OnProbe(tgt, 0, 0, false)
	}

	if len(*events) != 1 {
		t.Fatalf("期望 1 个事件，got %d", len(*events))
	}
	e := (*events)[0]
	if e.Kind != genv1.EdgeEventKind_EDGE_EVENT_KIND_DOWN {
		t.Fatalf("Kind 应为 DOWN，got %v", e.Kind)
	}
	if e.SrcNode != "nodeA" || e.DstNode != "nodeB" || e.IngressId != "ing-1" {
		t.Fatalf("Src/Dst/Ingress 字段错误，got Src=%s Dst=%s Ing=%s", e.SrcNode, e.DstNode, e.IngressId)
	}
}

func TestReporter_EmitEvent_DegradedFields(t *testing.T) {
	r, clk, events, _ := newTestReporter()
	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}

	r.OnProbe(tgt, 20, 0, true) // baseline=20

	for i := 0; i < 12; i++ {
		clk.Advance(5 * time.Second)
		r.OnProbe(tgt, 60, 70, true) // RTT 劣化 + loss 劣化
	}

	if len(*events) != 1 {
		t.Fatalf("期望 1 个 Degraded 事件，got %d (%v)", len(*events), *events)
	}
	e := (*events)[0]
	if e.Kind != genv1.EdgeEventKind_EDGE_EVENT_KIND_DEGRADED {
		t.Fatalf("Kind 应为 DEGRADED，got %v", e.Kind)
	}
	if e.RttMs != 60 || e.LossPermille != 70 {
		t.Fatalf("Degraded RTT/Loss 应携带触发样本 60/70，got %d/%d", e.RttMs, e.LossPermille)
	}
}

func TestReporter_TransientDegrade_NoEvent(t *testing.T) {
	r, clk, events, _ := newTestReporter()
	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}
	r.OnProbe(tgt, 20, 0, true)

	for i := 0; i < 8; i++ { // 40s < THold
		clk.Advance(5 * time.Second)
		r.OnProbe(tgt, 60, 0, true)
	}
	if len(*events) != 0 {
		t.Fatalf("瞬时劣化应本地消化，无事件，got %v", *events)
	}
}

func TestReporter_QualityDamping_SuppressSmall(t *testing.T) {
	r, clk, _, quality := newTestReporter()
	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}

	// 首次：探测 + Tick → 必上报（无 lastReported 基准）。
	r.OnProbe(tgt, 20, 0, true)
	r.Tick()
	if len(*quality) != 1 {
		t.Fatalf("首次质量上报应发出，got %d", len(*quality))
	}
	if len((*quality)[0].Samples) != 1 || (*quality)[0].SrcNode != "nodeA" {
		t.Fatalf("QualityReport 内容错误：%v", (*quality)[0])
	}

	// 间隔足够，但变化很小（RTT 20→22 < RTTThreshold 10，loss 不变）→ damping 抑制。
	clk.Advance(40 * time.Second)
	r.OnProbe(tgt, 22, 0, true)
	r.Tick()
	if len(*quality) != 1 {
		t.Fatalf("小变化应被 damping 抑制，got %d", len(*quality))
	}

	// 间隔足够 + 显著变化（RTT 20→40 >= 10）→ 上报。
	clk.Advance(40 * time.Second)
	r.OnProbe(tgt, 40, 0, true)
	r.Tick()
	if len(*quality) != 2 {
		t.Fatalf("显著变化应上报，got %d", len(*quality))
	}
}

func TestReporter_QualityDamping_MinInterval(t *testing.T) {
	r, clk, _, quality := newTestReporter()
	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}

	r.OnProbe(tgt, 20, 0, true)
	r.Tick() // 首报
	if len(*quality) != 1 {
		t.Fatalf("首报应发出，got %d", len(*quality))
	}

	// 间隔不足 MinInterval(30s)，即使显著变化也抑制。
	clk.Advance(10 * time.Second)
	r.OnProbe(tgt, 100, 0, true)
	r.Tick()
	if len(*quality) != 1 {
		t.Fatalf("间隔不足应抑制，got %d", len(*quality))
	}

	// 间隔够了 → 上报。
	clk.Advance(25 * time.Second) // 累计 35s > 30s
	r.Tick()
	if len(*quality) != 2 {
		t.Fatalf("间隔满足后应上报，got %d", len(*quality))
	}
}

func TestReporter_MultiTarget_IndependentFSM(t *testing.T) {
	r, clk, events, _ := newTestReporter()
	a := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}
	b := ProbeTarget{NodeID: "nodeC", IngressID: "ing-2"}

	r.OnProbe(a, 20, 0, true)
	r.OnProbe(b, 20, 0, true)

	// 只让 a 断连。
	for i := 0; i < reporterTestCfg().DownConfirm; i++ {
		clk.Advance(5 * time.Second)
		r.OnProbe(a, 0, 0, false)
		r.OnProbe(b, 20, 0, true) // b 保持健康
	}

	if len(*events) != 1 {
		t.Fatalf("只应有 a 的 1 个事件，got %d", len(*events))
	}
	if (*events)[0].DstNode != "nodeB" {
		t.Fatalf("事件应来自 nodeB，got %s", (*events)[0].DstNode)
	}
	if r.State(a) != Down || r.State(b) != Healthy {
		t.Fatalf("状态独立错误：a=%v b=%v", r.State(a), r.State(b))
	}
}

func TestReporter_QualityReport_OkFlip_IsSignificant(t *testing.T) {
	r, clk, _, quality := newTestReporter()
	tgt := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}

	r.OnProbe(tgt, 20, 0, true)
	r.Tick() // 首报
	clk.Advance(40 * time.Second)

	// ok 翻转（true→false）即使 rtt/loss 数值未越阈值也算显著。
	r.OnProbe(tgt, 0, 0, false)
	r.Tick()
	if len(*quality) != 2 {
		t.Fatalf("ok 翻转应触发上报，got %d", len(*quality))
	}
}

// TestReporter_RemoveTarget_NoStaleSample 验证：目标被移除后，Tick 不再上报其陈旧样本。
func TestReporter_RemoveTarget_NoStaleSample(t *testing.T) {
	r, clk, _, quality := newTestReporter()
	a := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}
	b := ProbeTarget{NodeID: "nodeC", IngressID: "ing-2"}

	// 两条链路均产生样本并首报。
	r.OnProbe(a, 20, 0, true)
	r.OnProbe(b, 20, 0, true)
	r.Tick()
	if len(*quality) != 1 {
		t.Fatalf("首报应发出，got %d", len(*quality))
	}
	if len((*quality)[0].Samples) != 2 {
		t.Fatalf("首报应含 2 条样本，got %d", len((*quality)[0].Samples))
	}

	// 移除 b，并让 a 显著变化触发下一轮上报。
	r.RemoveTarget(b)
	clk.Advance(40 * time.Second)
	r.OnProbe(a, 60, 0, true) // RTT 20→60 显著
	r.Tick()

	if len(*quality) != 2 {
		t.Fatalf("a 显著变化应上报，got %d", len(*quality))
	}
	rep := (*quality)[1]
	if len(rep.Samples) != 1 {
		t.Fatalf("移除 b 后应只上报 a 一条样本，got %d", len(rep.Samples))
	}
	if rep.Samples[0].DstNode != "nodeB" {
		t.Fatalf("唯一样本应为 a(nodeB)，got %s", rep.Samples[0].DstNode)
	}
}

// TestReporter_RemoveTarget_PrunesMaps 验证：移除目标后 fsms/samples/lastReported 同步收缩，不无界增长。
func TestReporter_RemoveTarget_PrunesMaps(t *testing.T) {
	r, _, _, _ := newTestReporter()
	a := ProbeTarget{NodeID: "nodeB", IngressID: "ing-1"}
	b := ProbeTarget{NodeID: "nodeC", IngressID: "ing-2"}

	r.OnProbe(a, 20, 0, true)
	r.OnProbe(b, 20, 0, true)
	r.Tick() // 写入 lastReported

	if got := r.trackedLen(); got != 2 {
		t.Fatalf("两目标应各占一槽，got %d", got)
	}

	r.RemoveTarget(b)
	if got := r.trackedLen(); got != 1 {
		t.Fatalf("移除 b 后应只剩 1 槽，got %d", got)
	}

	// SetTargets 只保留 a：再喂入历史目标 b 也不应残留。
	r.OnProbe(b, 20, 0, true) // 重新引入 b
	if got := r.trackedLen(); got != 2 {
		t.Fatalf("重新探测 b 应恢复 2 槽，got %d", got)
	}
	r.SetTargets([]ProbeTarget{a})
	if got := r.trackedLen(); got != 1 {
		t.Fatalf("SetTargets([a]) 后应只剩 1 槽，got %d", got)
	}
}
