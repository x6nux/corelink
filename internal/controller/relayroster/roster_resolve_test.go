package relayroster_test

import (
	"context"
	"testing"

	"github.com/x6nux/corelink/internal/controller/relayroster"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// ─── 测试：ResolveNodeLocation 查询当前接入 relay ────────────────────────────

func TestResolveNodeLocation(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	// 未接入：解析不到。
	if relayID, ok := r.ResolveNodeLocation("node-1"); ok {
		t.Errorf("未接入应解析不到, got (%q,%v)", relayID, ok)
	}

	// attach 后可解析。
	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId: "node-1", RelayId: "relay-1", Attached: true,
	})
	relayID, ok := r.ResolveNodeLocation("node-1")
	if !ok || relayID != "relay-1" {
		t.Errorf("ResolveNodeLocation(node-1) = (%q,%v), want (relay-1,true)", relayID, ok)
	}

	// detach 后解析不到。
	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId: "node-1", RelayId: "relay-1", Attached: false,
	})
	if relayID, ok := r.ResolveNodeLocation("node-1"); ok {
		t.Errorf("detach 后应解析不到, got (%q,%v)", relayID, ok)
	}
}

// ─── 测试：位置迁移（relay 改变）被识别为变更并触发重算 ───────────────────────

func TestReportNodeLocation_MigrationTriggersNotify(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	// 初次 attach relay-1。
	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId: "node-1", RelayId: "relay-1", Attached: true,
	})
	// 迁移到 relay-2（agent 切换接入）。
	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId: "node-1", RelayId: "relay-2", Attached: true,
	})

	if relayID, _ := r.ResolveNodeLocation("node-1"); relayID != "relay-2" {
		t.Errorf("迁移后位置应为 relay-2, got %q", relayID)
	}
	// 两次上报都应触发对 node-1 的重算。
	called := ntf.getCalled()
	count := 0
	for _, id := range called {
		if id == "node-1" {
			count++
		}
	}
	if count < 2 {
		t.Errorf("attach + 迁移应至少触发 2 次 node-1 重算, got %d", count)
	}
}

// ─── 测试：AffectedNodes 扩展受影响节点集合（会合 relay 变更）──────────────────

func TestReportNodeLocation_AffectedNodesDistribution(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	// 注入：与 node-1 有路由关系的节点（peer），位置变更时一并重算。
	r.SetAffectedNodes(func(changedNode string) []string {
		if changedNode == "node-1" {
			return []string{"peer-A", "peer-B"}
		}
		return nil
	})

	_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
		NodeId: "node-1", RelayId: "relay-1", Attached: true,
	})

	called := ntf.getCalled()
	want := map[string]bool{"node-1": false, "peer-A": false, "peer-B": false}
	for _, id := range called {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("位置变更应触发 %q 重算，但未触发；called=%v", id, called)
		}
	}
}

// ─── 测试：重复上报相同位置仍幂等（不 panic，可解析）──────────────────────────

func TestReportNodeLocation_RepeatSamePosition(t *testing.T) {
	st := &stubStore{}
	ntf := &stubNotify{}
	r := relayroster.New(st, ntf)

	for i := 0; i < 3; i++ {
		_, _ = r.ReportNodeLocation(context.Background(), &genv1.NodeLocation{
			NodeId: "node-1", RelayId: "relay-1", Attached: true,
		})
	}
	if relayID, ok := r.ResolveNodeLocation("node-1"); !ok || relayID != "relay-1" {
		t.Errorf("重复上报后位置应稳定为 relay-1, got (%q,%v)", relayID, ok)
	}
}
