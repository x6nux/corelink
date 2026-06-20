package relayroster_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/x6nux/corelink/internal/controller/relayroster"
	"github.com/x6nux/corelink/internal/controller/store"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── store stub ────────────────────────────────────────────────────────────────

type stubStore struct {
	relayInfos []store.RelayInfo
	relayLinks []store.RelayLink
}

func (s *stubStore) UpsertRelayInfo(info *store.RelayInfo) error {
	for i, ri := range s.relayInfos {
		if ri.NodeID == info.NodeID {
			s.relayInfos[i] = *info
			return nil
		}
	}
	s.relayInfos = append(s.relayInfos, *info)
	return nil
}

func (s *stubStore) ListRelayInfo() ([]store.RelayInfo, error) {
	return s.relayInfos, nil
}

func (s *stubStore) ListRelayLinks() ([]store.RelayLink, error) {
	return s.relayLinks, nil
}

// ─── notify stub ───────────────────────────────────────────────────────────────

type stubNotify struct {
	mu     sync.Mutex
	called []string
}

func (n *stubNotify) RecomputeAndNotify(nodeIDs ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.called = append(n.called, nodeIDs...)
}

func (n *stubNotify) getCalled() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := make([]string, len(n.called))
	copy(cp, n.called)
	return cp
}

// ─── 测试：attach 更新映射 ─────────────────────────────────────────────────────

func TestReportNodeLocation_Attach(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	ack, err := r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId:   "node-1",
		RelayId:  "relay-1",
		Attached: true,
	})
	if err != nil {
		t.Fatalf("ReportNodeLocation 失败: %v", err)
	}
	if ack.Version == 0 {
		t.Error("期望 version > 0")
	}

	m := r.NodeRelay()
	if got := m["node-1"]; got != "relay-1" {
		t.Errorf("NodeRelay[node-1] = %q, want relay-1", got)
	}
}

// ─── 测试：detach 删除映射 ─────────────────────────────────────────────────────

func TestReportNodeLocation_Detach(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	// 先 attach
	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId:   "node-1",
		RelayId:  "relay-1",
		Attached: true,
	})

	// 再 detach
	ack, err := r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId:   "node-1",
		RelayId:  "relay-1",
		Attached: false,
	})
	if err != nil {
		t.Fatalf("detach 失败: %v", err)
	}
	if ack.Version == 0 {
		t.Error("期望 version > 0")
	}

	m := r.NodeRelay()
	if _, exists := m["node-1"]; exists {
		t.Error("期望 node-1 在 detach 后从映射中删除")
	}
}

// ─── 测试：version 单调递增 ────────────────────────────────────────────────────

func TestReportNodeLocation_VersionIncrement(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	var lastVersion uint64
	for i := 0; i < 5; i++ {
		ack, err := r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
			NodeId:   "node-1",
			RelayId:  "relay-1",
			Attached: true,
		})
		if err != nil {
			t.Fatalf("第 %d 次失败: %v", i, err)
		}
		if ack.Version <= lastVersion {
			t.Errorf("version 未递增：上次=%d, 本次=%d", lastVersion, ack.Version)
		}
		lastVersion = ack.Version
	}
}

// ─── 测试：触发 RecomputeAndNotify ────────────────────────────────────────────

func TestReportNodeLocation_TriggersNotify(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId:   "node-1",
		RelayId:  "relay-1",
		Attached: true,
	})

	called := ntf.getCalled()
	if len(called) == 0 {
		t.Error("期望触发 RecomputeAndNotify，但未调用")
	}
	found := false
	for _, id := range called {
		if id == "node-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("期望 RecomputeAndNotify 被调用时包含 node-1，实际 called=%v", called)
	}
}

// ─── 测试：NodeRelay 快照是独立副本 ───────────────────────────────────────────

func TestNodeRelay_Snapshot(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId:   "n1",
		RelayId:  "r1",
		Attached: true,
	})
	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId:   "n2",
		RelayId:  "r2",
		Attached: true,
	})

	snap1 := r.NodeRelay()
	if len(snap1) != 2 {
		t.Fatalf("快照大小 = %d, want 2", len(snap1))
	}

	// 修改快照不影响内部状态
	snap1["n1"] = "modified"

	snap2 := r.NodeRelay()
	if snap2["n1"] != "r1" {
		t.Errorf("快照修改影响了内部状态：snap2[n1]=%q, want r1", snap2["n1"])
	}
}

// ─── 测试：RegisterRelay / Topology ───────────────────────────────────────────

func TestRegisterRelayAndTopology(t *testing.T) {
	st := &stubStore{
		relayLinks: []store.RelayLink{
			{RelayID: "relay-A", NeighborID: "relay-B"},
			{RelayID: "relay-B", NeighborID: "relay-A"},
		},
	}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	// 注册 relay
	err := r.RegisterRelay(&store.RelayInfo{
		NodeID:         "relay-A",
		TunnelEndpoint: "10.0.0.1:7443",
		Priority:       1,
	})
	if err != nil {
		t.Fatalf("RegisterRelay 失败: %v", err)
	}

	// 查询拓扑
	topo, err := r.Topology()
	if err != nil {
		t.Fatalf("Topology 失败: %v", err)
	}

	neighbors, ok := topo["relay-A"]
	if !ok {
		t.Fatal("拓扑中未找到 relay-A")
	}
	if len(neighbors) != 1 || neighbors[0] != "relay-B" {
		t.Errorf("relay-A 的邻居 = %v, want [relay-B]", neighbors)
	}
}

// ─── 测试：并发安全 ────────────────────────────────────────────────────────────

func TestConcurrentReportNodeLocation(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	const goroutines = 50
	var done atomic.Int32
	errs := make(chan error, goroutines)

	for i := range goroutines {
		go func(i int) {
			defer done.Add(1)
			nodeID := "node-" + string(rune('A'+i%26))
			_, err := r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
				NodeId:   nodeID,
				RelayId:  "relay-x",
				Attached: true,
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}

	// 等待所有 goroutine
	for done.Load() < goroutines {
		// busy wait（测试简单等待）
	}
	close(errs)
	for err := range errs {
		t.Errorf("并发错误: %v", err)
	}
}
