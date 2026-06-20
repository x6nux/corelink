package multirelay_test

import (
	"sync"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/multirelay"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── 测试辅助 ──────────────────────────────────────────────────────────────────

// fakeClock 是注入的可控时钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(0, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeProber 按 relayID 返回预设的 RTT/可用性，支持运行时改写。
type fakeProber struct {
	mu    sync.Mutex
	rtt   map[string]int  // relayID → rttMs
	down  map[string]bool // relayID → 是否探测失败
	calls map[string]int  // relayID → 探测次数
}

func newFakeProber() *fakeProber {
	return &fakeProber{
		rtt:   make(map[string]int),
		down:  make(map[string]bool),
		calls: make(map[string]int),
	}
}

func (p *fakeProber) set(relayID string, rttMs int) {
	p.mu.Lock()
	p.rtt[relayID] = rttMs
	p.down[relayID] = false
	p.mu.Unlock()
}

func (p *fakeProber) kill(relayID string) {
	p.mu.Lock()
	p.down[relayID] = true
	p.mu.Unlock()
}

func (p *fakeProber) probe(ep *genv1.RelayEndpoint) (rttMs int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := ep.GetRelayId()
	p.calls[id]++
	if p.down[id] {
		return 0, false
	}
	return p.rtt[id], true
}

func (p *fakeProber) callCount(relayID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[relayID]
}

func relays(specs ...[2]interface{}) []*genv1.RelayEndpoint {
	var out []*genv1.RelayEndpoint
	for _, s := range specs {
		out = append(out, &genv1.RelayEndpoint{
			RelayId:  s[0].(string),
			Priority: uint32(s[1].(int)),
		})
	}
	return out
}

// switchRec 记录切换回调。
type switchRec struct {
	mu   sync.Mutex
	from []string
	to   []string
}

func (s *switchRec) onSwitch(from, to string) {
	s.mu.Lock()
	s.from = append(s.from, from)
	s.to = append(s.to, to)
	s.mu.Unlock()
}

func (s *switchRec) last() (from, to string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n = len(s.to)
	if n == 0 {
		return "", "", 0
	}
	return s.from[n-1], s.to[n-1], n
}

// ─── 测试：初始选择按 Priority 选最优（最小 Priority）──────────────────────────

func TestSelector_InitialPicksLowestPriority(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	sel := multirelay.New(multirelay.Config{
		Candidates: relays([2]interface{}{"r-a", 10}, [2]interface{}{"r-b", 1}, [2]interface{}{"r-c", 5}),
		Probe:      pr.probe,
		Clock:      clk.Now,
	})
	if got := sel.Current(); got != "r-b" {
		t.Errorf("初始接入应选最低 Priority 的 r-b，got %q", got)
	}
}

// ─── 测试：候选探测——非当前接入的候选会被低频探测 ─────────────────────────────

func TestSelector_ProbesNonPrimaryCandidates(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 20)
	pr.set("r-b", 30)
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
	})
	// 当前接入 r-a。推进一个探测周期，触发对候选 r-b 的探测。
	clk.advance(time.Second)
	sel.Tick()

	if pr.callCount("r-b") == 0 {
		t.Error("候选 r-b 应被低频探测，但未探测")
	}
}

// ─── 测试：候选明显更优 + 滞回——连续 N 次更优才切换 ──────────────────────────

func TestSelector_SwitchesToBetterCandidateWithHysteresis(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 100) // 当前接入，RTT 高
	pr.set("r-b", 20)  // 候选，明显更优
	rec := &switchRec{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchMargin:  30, // 候选需比当前低至少 30ms
		SwitchStreak:  3,  // 连续 3 次更优才切
		OnSwitch:      rec.onSwitch,
	})
	if sel.Current() != "r-a" {
		t.Fatalf("初始应为 r-a")
	}

	// 前 2 次更优：不应切换（滞回未满）。
	for i := 0; i < 2; i++ {
		clk.advance(time.Second)
		sel.Tick()
	}
	if _, _, n := rec.last(); n != 0 {
		t.Fatalf("滞回未满（2<3）不应切换，但已切换 %d 次", n)
	}
	if sel.Current() != "r-a" {
		t.Errorf("滞回未满时 Current 应仍为 r-a，got %q", sel.Current())
	}

	// 第 3 次更优：达到滞回阈值，切换到 r-b。
	clk.advance(time.Second)
	sel.Tick()
	from, to, n := rec.last()
	if n != 1 {
		t.Fatalf("达到滞回阈值应切换 1 次，got %d", n)
	}
	if from != "r-a" || to != "r-b" {
		t.Errorf("切换应为 r-a→r-b，got %s→%s", from, to)
	}
	if sel.Current() != "r-b" {
		t.Errorf("切换后 Current 应为 r-b，got %q", sel.Current())
	}
}

// ─── 测试：候选只是略优（未超 margin）不切换——防抖动 ──────────────────────────

func TestSelector_NoSwitchWhenMarginNotExceeded(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 50)
	pr.set("r-b", 40) // 只低 10ms，不足 margin 30
	rec := &switchRec{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchMargin:  30,
		SwitchStreak:  2,
		OnSwitch:      rec.onSwitch,
	})
	for i := 0; i < 10; i++ {
		clk.advance(time.Second)
		sel.Tick()
	}
	if _, _, n := rec.last(); n != 0 {
		t.Errorf("候选未超 margin 不应切换，但切换了 %d 次", n)
	}
	if sel.Current() != "r-a" {
		t.Errorf("Current 应仍为 r-a，got %q", sel.Current())
	}
}

// ─── 测试：当前主连接劣化——切换到次优候选 ────────────────────────────────────

func TestSelector_SwitchesOnPrimaryDegradation(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 20) // 当前接入，初始良好
	pr.set("r-b", 50) // 候选，更差但可用
	rec := &switchRec{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchMargin:  30,
		SwitchStreak:  2,
		DegradeStreak: 2,
		OnSwitch:      rec.onSwitch,
	})

	// 主连接劣化：注入连续质量劣化信号。
	sel.ReportPrimaryDegraded()
	sel.ReportPrimaryDegraded()

	// 即便候选 r-b 比 r-a 更差，主连接劣化也应切到可用候选。
	clk.advance(time.Second)
	sel.Tick()

	from, to, n := rec.last()
	if n == 0 {
		t.Fatal("主连接劣化应触发切换")
	}
	if from != "r-a" || to != "r-b" {
		t.Errorf("劣化切换应为 r-a→r-b，got %s→%s", from, to)
	}
}

// ─── 测试：当前接入探测失败（不可用）——切到可用候选 ──────────────────────────

func TestSelector_SwitchesWhenPrimaryProbeFails(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 20)
	pr.set("r-b", 60)
	rec := &switchRec{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchMargin:  30,
		SwitchStreak:  2,
		DegradeStreak: 2,
		OnSwitch:      rec.onSwitch,
	})
	pr.kill("r-a") // 当前接入不可用

	for i := 0; i < 3; i++ {
		clk.advance(time.Second)
		sel.Tick()
	}
	from, to, n := rec.last()
	if n == 0 {
		t.Fatal("主连接探测失败应触发切换")
	}
	if from != "r-a" || to != "r-b" {
		t.Errorf("应切到可用候选 r-a→r-b，got %s→%s", from, to)
	}
}

// ─── 测试：切换触发位置上报 ───────────────────────────────────────────────────

type recReporter struct {
	mu   sync.Mutex
	locs []*genv1.NodeLocation
}

func (r *recReporter) ReportLocation(relayID string, attached bool) {
	r.mu.Lock()
	r.locs = append(r.locs, &genv1.NodeLocation{RelayId: relayID, Attached: attached})
	r.mu.Unlock()
}

func (r *recReporter) snapshot() []*genv1.NodeLocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]*genv1.NodeLocation, len(r.locs))
	copy(cp, r.locs)
	return cp
}

func TestSelector_SwitchTriggersLocationReport(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 100)
	pr.set("r-b", 10)
	rep := &recReporter{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchMargin:  30,
		SwitchStreak:  1,
		OnLocation:    rep.ReportLocation,
	})

	clk.advance(time.Second)
	sel.Tick()

	locs := rep.snapshot()
	// 期望：detach 旧 relay r-a + attach 新 relay r-b。
	var attachedB, detachedA bool
	for _, l := range locs {
		if l.RelayId == "r-b" && l.Attached {
			attachedB = true
		}
		if l.RelayId == "r-a" && !l.Attached {
			detachedA = true
		}
	}
	if !attachedB {
		t.Error("切换应上报新 relay r-b attached=true")
	}
	if !detachedA {
		t.Error("切换应上报旧 relay r-a attached=false")
	}
}

// ─── 测试：滞回不抖动——更优信号间断时计数清零，不误切 ────────────────────────

func TestSelector_HysteresisResetsOnFlap(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 50)
	pr.set("r-b", 10)
	rec := &switchRec{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchMargin:  30,
		SwitchStreak:  3,
		OnSwitch:      rec.onSwitch,
	})

	// 2 次更优。
	clk.advance(time.Second)
	sel.Tick()
	clk.advance(time.Second)
	sel.Tick()
	// 抖动：候选 r-b 突然变差（不再更优），计数应清零。
	pr.set("r-b", 60)
	clk.advance(time.Second)
	sel.Tick()
	// 候选恢复更优，但滞回重新累计，仅 1 次。
	pr.set("r-b", 10)
	clk.advance(time.Second)
	sel.Tick()

	if _, _, n := rec.last(); n != 0 {
		t.Errorf("抖动应清零滞回计数，不应切换，但切换了 %d 次", n)
	}
	if sel.Current() != "r-a" {
		t.Errorf("Current 应仍为 r-a，got %q", sel.Current())
	}
}

// ─── 测试：无候选/单候选——稳定不切换 ─────────────────────────────────────────

func TestSelector_SingleCandidateStable(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 50)
	rec := &switchRec{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchStreak:  1,
		OnSwitch:      rec.onSwitch,
	})
	for i := 0; i < 5; i++ {
		clk.advance(time.Second)
		sel.Tick()
	}
	if _, _, n := rec.last(); n != 0 {
		t.Errorf("单候选不应切换，但切换了 %d 次", n)
	}
	if sel.Current() != "r-a" {
		t.Errorf("Current 应为 r-a，got %q", sel.Current())
	}
}

// ── Task3.6：按下发的选定入口接入（含 CDN SNI）──

// TestSelectorSwitchIngressCDN 验证：切换时 OnSwitchIngress 携带新接入 relay 的
// 选定入口端点；CDN 入口附带 SNI 拨号参数（入口选择来自下发，不做第二层选优）。
func TestSelectorSwitchIngressCDN(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 100) // 当前主接入，较差
	pr.set("r-b", 10)  // 候选更优 → 触发切换

	// 下发的选定入口表：r-b 经 CDN 入口接入（带 SNI）。
	ingresses := map[string]*multirelay.IngressEndpoint{
		"r-a": {IngressID: "ia", Addr: "1.1.1.1:443"},
		"r-b": {IngressID: "ib-cdn", Addr: "edge:443", SNI: "rb.cdn.example.com", IsCDN: true},
	}
	resolver := func(relayID string) *multirelay.IngressEndpoint { return ingresses[relayID] }

	var mu sync.Mutex
	var gotTo string
	var gotEP *multirelay.IngressEndpoint
	sel := multirelay.New(multirelay.Config{
		Candidates:      relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:           pr.probe,
		Clock:           clk.Now,
		ProbeInterval:   time.Second,
		SwitchMargin:    30,
		SwitchStreak:    1,
		IngressResolver: resolver,
		OnSwitchIngress: func(from, to string, ep *multirelay.IngressEndpoint) {
			mu.Lock()
			gotTo, gotEP = to, ep
			mu.Unlock()
		},
	})

	for i := 0; i < 3; i++ {
		clk.advance(time.Second)
		sel.Tick()
	}

	mu.Lock()
	defer mu.Unlock()
	if gotTo != "r-b" {
		t.Fatalf("应切换到 r-b，got %q", gotTo)
	}
	if gotEP == nil {
		t.Fatal("OnSwitchIngress 应携带 r-b 的选定入口端点，got nil")
	}
	if !gotEP.IsCDN || gotEP.SNI != "rb.cdn.example.com" || gotEP.IngressID != "ib-cdn" {
		t.Fatalf("r-b 选定入口应为 CDN 且带 SNI，got %+v", gotEP)
	}
}

// TestSelectorCurrentIngress 验证 CurrentIngress 返回当前接入的选定入口。
func TestSelectorCurrentIngress(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	resolver := func(relayID string) *multirelay.IngressEndpoint {
		if relayID == "r-a" {
			return &multirelay.IngressEndpoint{IngressID: "ia", Addr: "a:443"}
		}
		return nil
	}
	sel := multirelay.New(multirelay.Config{
		Candidates:      relays([2]interface{}{"r-a", 1}),
		Probe:           pr.probe,
		Clock:           clk.Now,
		IngressResolver: resolver,
	})
	ep := sel.CurrentIngress()
	if ep == nil || ep.IngressID != "ia" || ep.Addr != "a:443" {
		t.Fatalf("CurrentIngress 应为 r-a 的入口 ia，got %+v", ep)
	}
}

// TestSelectorNoIngressResolverBackCompat 验证向后兼容：
// 不注入 IngressResolver/OnSwitchIngress 时 Selector 行为与既有完全一致。
func TestSelectorNoIngressResolverBackCompat(t *testing.T) {
	clk := newFakeClock()
	pr := newFakeProber()
	pr.set("r-a", 100)
	pr.set("r-b", 10)

	rec := &switchRec{}
	sel := multirelay.New(multirelay.Config{
		Candidates:    relays([2]interface{}{"r-a", 1}, [2]interface{}{"r-b", 2}),
		Probe:         pr.probe,
		Clock:         clk.Now,
		ProbeInterval: time.Second,
		SwitchMargin:  30,
		SwitchStreak:  1,
		OnSwitch:      rec.onSwitch,
	})
	for i := 0; i < 3; i++ {
		clk.advance(time.Second)
		sel.Tick()
	}
	if _, to, n := rec.last(); n == 0 || to != "r-b" {
		t.Fatalf("无入口接入时仍应正常切换到 r-b，got to=%q n=%d", to, n)
	}
	if sel.CurrentIngress() != nil {
		t.Fatal("未注入 IngressResolver 时 CurrentIngress 应为 nil")
	}
}
