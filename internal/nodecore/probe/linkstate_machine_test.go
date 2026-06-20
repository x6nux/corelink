package probe

import (
	"testing"
	"time"
)

// fakeClock 是确定性注入时钟（无真实 sleep）。
type fakeClock struct {
	t time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// testCfg 是测试用的小阈值配置（基线由首个健康样本固定，便于推算）。
func testCfg() LinkFSMConfig {
	return LinkFSMConfig{
		ProbeInterval:       5 * time.Second,
		DownConfirm:         3,
		THold:               45 * time.Second,
		TRecover:            30 * time.Second,
		DegradeLossPermille: 50,  // >5%
		DegradeRTTFactor:    1.5, // >1.5x baseline
		RecoverLossPermille: 20,  // <2%
		RecoverRTTFactor:    1.2, // <=1.2x baseline
		BaselineEWMAAlpha:   0,   // 固定基线 = 首个健康 RTT，测试可精确推算
	}
}

// establishBaseline 用一个健康样本初始化基线 RTT，并保持 Healthy。
func establishBaseline(f *LinkFSM, baselineRTT uint32) {
	f.Observe(baselineRTT, 0, true)
}

func TestObserve_HealthySteady_NoEvents(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)

	for i := 0; i < 10; i++ {
		clk.Advance(5 * time.Second)
		ev := f.Observe(20, 0, true)
		if len(ev) != 0 {
			t.Fatalf("健康稳态不应产生事件，got %v", ev)
		}
	}
	if f.State() != Healthy {
		t.Fatalf("期望 Healthy，got %v", f.State())
	}
}

func TestObserve_TransientDegrade_LocalOnly(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20) // baseline=20，劣化阈值 RTT>30

	// 瞬时劣化：RTT=60(>30) 持续 40s < THold(45s)，应本地消化、零事件。
	for i := 0; i < 8; i++ { // 8*5s = 40s
		clk.Advance(5 * time.Second)
		ev := f.Observe(60, 0, true)
		if len(ev) != 0 {
			t.Fatalf("第 %d 次瞬时劣化不应上报，got %v", i, ev)
		}
	}
	if f.State() != Healthy {
		t.Fatalf("瞬时劣化后应仍为 Healthy，got %v", f.State())
	}

	// 劣化消失：回到健康样本，应清除劣化计时、仍零事件。
	clk.Advance(5 * time.Second)
	if ev := f.Observe(20, 0, true); len(ev) != 0 {
		t.Fatalf("劣化消退不应上报，got %v", ev)
	}
}

func TestObserve_SustainedDegrade_ReportsDegradedOnce(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20) // 劣化阈值 RTT>30

	var degraded []FSMEvent
	// 持续劣化 >= THold(45s)。第一次劣化样本在 t0，需累计到 >=45s 才转。
	for i := 0; i < 12; i++ { // 推进足够久
		clk.Advance(5 * time.Second)
		ev := f.Observe(60, 0, true)
		degraded = append(degraded, ev...)
	}

	// 应恰好产生 1 个 Degraded 事件（转换时一次，之后不重复刷）。
	cnt := 0
	for _, e := range degraded {
		if e.Kind == EventDegraded {
			cnt++
			if e.RTTMs != 60 {
				t.Fatalf("Degraded 事件 RTT 应为触发样本 60，got %d", e.RTTMs)
			}
		}
	}
	if cnt != 1 {
		t.Fatalf("期望恰好 1 个 Degraded 事件，got %d (events=%v)", cnt, degraded)
	}
	if f.State() != Degraded {
		t.Fatalf("期望状态 Degraded，got %v", f.State())
	}
}

func TestObserve_DegradeTimingBoundary(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	// 劣化计时从首个劣化样本开始。THold=45s。
	clk.Advance(5 * time.Second)
	f.Observe(60, 0, true) // degradeSince = start
	start := clk.t

	// 严格在 THold 之前的所有劣化样本都不应转 Degraded（now-start < 45s）。
	for {
		next := clk.t.Add(5 * time.Second)
		if next.Sub(start) >= 45*time.Second {
			break // 下一步会越过 THold，停止精确推进
		}
		clk.Advance(5 * time.Second)
		if ev := f.Observe(60, 0, true); len(ev) != 0 {
			t.Fatalf("在 %v (<THold) 不应转 Degraded，got %v", clk.t.Sub(start), ev)
		}
		if f.State() != Healthy {
			t.Fatalf("<THold 仍应 Healthy，got %v", f.State())
		}
	}

	// 推进越过 THold（now-start >= 45s）→ 转 Degraded。
	clk.Advance(5 * time.Second)
	ev := f.Observe(60, 0, true)
	if len(ev) != 1 || ev[0].Kind != EventDegraded {
		t.Fatalf(">=THold 应转 Degraded，got %v (elapsed=%v)", ev, clk.t.Sub(start))
	}
}

func TestObserve_Down_AfterDownConfirm_Once(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	// 前 DownConfirm-1 次失败：本地消化，不发事件。
	for i := 0; i < testCfg().DownConfirm-1; i++ {
		clk.Advance(5 * time.Second)
		if ev := f.Observe(0, 0, false); len(ev) != 0 {
			t.Fatalf("第 %d 次失败(未达阈值)不应上报，got %v", i+1, ev)
		}
		if f.State() != Healthy {
			t.Fatalf("未达 DownConfirm 应仍 Healthy，got %v", f.State())
		}
	}

	// 第 DownConfirm 次失败：转 Down，发 1 个 Down 事件。
	clk.Advance(5 * time.Second)
	ev := f.Observe(0, 0, false)
	if len(ev) != 1 || ev[0].Kind != EventDown {
		t.Fatalf("达 DownConfirm 应发 Down，got %v", ev)
	}
	if f.State() != Down {
		t.Fatalf("期望 Down，got %v", f.State())
	}

	// 继续失败：已 Down，不重复发事件。
	for i := 0; i < 5; i++ {
		clk.Advance(5 * time.Second)
		if ev := f.Observe(0, 0, false); len(ev) != 0 {
			t.Fatalf("Down 后继续失败不应重复上报，got %v", ev)
		}
	}
}

func TestObserve_SingleFailDoesNotResetByGap(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	// 失败、失败、成功（清零）、失败、失败、失败 → 第三次连续失败才 Down。
	clk.Advance(5 * time.Second)
	f.Observe(0, 0, false) // fail 1
	clk.Advance(5 * time.Second)
	f.Observe(0, 0, false) // fail 2
	clk.Advance(5 * time.Second)
	if ev := f.Observe(20, 0, true); len(ev) != 0 { // success → 清零
		t.Fatalf("成功探测不应上报，got %v", ev)
	}

	clk.Advance(5 * time.Second)
	f.Observe(0, 0, false) // fail 1 (重新计数)
	clk.Advance(5 * time.Second)
	if ev := f.Observe(0, 0, false); len(ev) != 0 { // fail 2
		t.Fatalf("成功后才 2 次失败不应 Down，got %v", ev)
	}
	clk.Advance(5 * time.Second)
	ev := f.Observe(0, 0, false) // fail 3 → Down
	if len(ev) != 1 || ev[0].Kind != EventDown {
		t.Fatalf("连续 3 次失败应 Down，got %v", ev)
	}
}

func TestObserve_Recover_AfterTRecover_Once(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	// 先打到 Down。
	for i := 0; i < testCfg().DownConfirm; i++ {
		clk.Advance(5 * time.Second)
		f.Observe(0, 0, false)
	}
	if f.State() != Down {
		t.Fatalf("前置应 Down，got %v", f.State())
	}

	// 恢复样本：loss=0(<20)，RTT=20(<=baseline*1.2=24)。需保持 >= TRecover(30s)。
	var recovered []FSMEvent
	for i := 0; i < 10; i++ {
		clk.Advance(5 * time.Second)
		recovered = append(recovered, f.Observe(20, 0, true)...)
	}

	cnt := 0
	for _, e := range recovered {
		if e.Kind == EventRecovered {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("期望恰好 1 个 Recovered，got %d (events=%v)", cnt, recovered)
	}
	if f.State() != Healthy {
		t.Fatalf("恢复后应 Healthy，got %v", f.State())
	}
}

func TestObserve_RecoverTimingBoundary(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	for i := 0; i < testCfg().DownConfirm; i++ {
		clk.Advance(5 * time.Second)
		f.Observe(0, 0, false)
	}

	// 第一恢复样本启动 recoverSince。TRecover=30s。
	clk.Advance(5 * time.Second)
	f.Observe(20, 0, true)
	start := clk.t

	for {
		next := clk.t.Add(5 * time.Second)
		if next.Sub(start) >= 30*time.Second {
			break
		}
		clk.Advance(5 * time.Second)
		if ev := f.Observe(20, 0, true); len(ev) != 0 {
			t.Fatalf("在 %v (<TRecover) 不应恢复，got %v", clk.t.Sub(start), ev)
		}
		if f.State() != Down {
			t.Fatalf("<TRecover 仍应 Down，got %v", f.State())
		}
	}

	clk.Advance(5 * time.Second) // 越过 30s
	ev := f.Observe(20, 0, true)
	if len(ev) != 1 || ev[0].Kind != EventRecovered {
		t.Fatalf(">=TRecover 应恢复，got %v", ev)
	}
}

func TestObserve_RecoverInterruptedByRelapse(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	for i := 0; i < testCfg().DownConfirm; i++ {
		clk.Advance(5 * time.Second)
		f.Observe(0, 0, false)
	}

	// 恢复 20s（< TRecover），然后一次失败打断 → 恢复计时清零，仍 Down。
	for i := 0; i < 4; i++ { // 20s
		clk.Advance(5 * time.Second)
		f.Observe(20, 0, true)
	}
	clk.Advance(5 * time.Second)
	f.Observe(0, 0, false) // relapse → reset recover timer

	// 再恢复 20s，仍不足（因为被打断重新计时）。
	for i := 0; i < 4; i++ {
		clk.Advance(5 * time.Second)
		if ev := f.Observe(20, 0, true); len(ev) != 0 {
			t.Fatalf("打断后 20s 不应恢复，got %v", ev)
		}
	}
	if f.State() != Down {
		t.Fatalf("应仍 Down，got %v", f.State())
	}
}

// TestObserve_Hysteresis_NoFlapping 验证滞回带内的抖动不触发状态变化。
func TestObserve_Hysteresis_NoFlapping(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20) // baseline=20

	// 先持续劣化到 Degraded。
	for i := 0; i < 12; i++ {
		clk.Advance(5 * time.Second)
		f.Observe(60, 0, true) // RTT=60 > 30 劣化
	}
	if f.State() != Degraded {
		t.Fatalf("前置应 Degraded，got %v", f.State())
	}

	// 滞回带：RTT 在 (recover阈值 24, degrade阈值 30] 之间，loss 也在 (20,50] 之间。
	// 取 RTT=28（>24 不满足恢复，<=30 不再加重劣化），loss=30（>=20 不恢复，<=50 不劣化）。
	// 这种"中间态"既不触发恢复也不触发劣化 → 零事件，状态保持 Degraded。
	for i := 0; i < 20; i++ {
		clk.Advance(5 * time.Second)
		ev := f.Observe(28, 30, true)
		if len(ev) != 0 {
			t.Fatalf("第 %d 次滞回带抖动不应产生事件，got %v", i, ev)
		}
		if f.State() != Degraded {
			t.Fatalf("滞回带内状态应保持 Degraded，got %v", f.State())
		}
	}
}

// TestObserve_HysteresisLoss_NoFlapping 用 loss 维度验证滞回。
func TestObserve_HysteresisLoss_NoFlapping(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	// 持续高 loss → Degraded。
	for i := 0; i < 12; i++ {
		clk.Advance(5 * time.Second)
		f.Observe(20, 80, true) // loss=80 > 50
	}
	if f.State() != Degraded {
		t.Fatalf("前置应 Degraded，got %v", f.State())
	}

	// loss=35 落在 [RecoverLoss=20, DegradeLoss=50] 滞回带：不恢复也不再劣化。
	for i := 0; i < 15; i++ {
		clk.Advance(5 * time.Second)
		if ev := f.Observe(20, 35, true); len(ev) != 0 {
			t.Fatalf("loss 滞回带抖动不应产生事件，got %v", ev)
		}
	}
	if f.State() != Degraded {
		t.Fatalf("应保持 Degraded，got %v", f.State())
	}
}

// TestObserve_DegradedToRecovered 验证 Degraded 也能恢复。
func TestObserve_DegradedToRecovered(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	for i := 0; i < 12; i++ {
		clk.Advance(5 * time.Second)
		f.Observe(60, 0, true)
	}
	if f.State() != Degraded {
		t.Fatalf("前置应 Degraded，got %v", f.State())
	}

	var got []FSMEvent
	for i := 0; i < 10; i++ {
		clk.Advance(5 * time.Second)
		got = append(got, f.Observe(20, 0, true)...) // 恢复样本
	}
	cnt := 0
	for _, e := range got {
		if e.Kind == EventRecovered {
			cnt++
		}
	}
	if cnt != 1 || f.State() != Healthy {
		t.Fatalf("Degraded 应恢复到 Healthy 一次，got cnt=%d state=%v", cnt, f.State())
	}
}

// TestObserve_FullCycle 验证完整生命周期 Healthy→Degraded→Down→Recovered 不重复刷。
func TestObserve_FullCycle(t *testing.T) {
	clk := newFakeClock()
	f := NewLinkFSM(testCfg(), clk.Now)
	establishBaseline(f, 20)

	var all []FSMEvent
	obs := func(rtt, loss uint32, ok bool) {
		clk.Advance(5 * time.Second)
		all = append(all, f.Observe(rtt, loss, ok)...)
	}

	// 持续劣化 → Degraded。
	for i := 0; i < 12; i++ {
		obs(60, 0, true)
	}
	// 然后直接断连 → Down（连续失败）。
	for i := 0; i < 4; i++ {
		obs(0, 0, false)
	}
	// 恢复 → Recovered。
	for i := 0; i < 10; i++ {
		obs(20, 0, true)
	}

	kinds := map[FSMEventKind]int{}
	for _, e := range all {
		kinds[e.Kind]++
	}
	if kinds[EventDegraded] != 1 || kinds[EventDown] != 1 || kinds[EventRecovered] != 1 {
		t.Fatalf("各事件应恰好一次，got %v", kinds)
	}
	if f.State() != Healthy {
		t.Fatalf("最终应 Healthy，got %v", f.State())
	}
}
