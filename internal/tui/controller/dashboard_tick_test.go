package controller

import (
	"testing"

	"github.com/x6nux/corelink/internal/tui"
)

// TestDashboardTab_DoesNotRearmTick 验证 #13 配套清理：
// DashboardTab 收到 TickMsg 后不再自行装弹 TickCmd（Tick 链已由 App 层统一驱动）。
// 无 client 时 fetch() 返回 nil，因此整体应返回 nil Cmd —— 若仍含 TickCmd 则非 nil。
func TestDashboardTab_DoesNotRearmTick(t *testing.T) {
	tab := NewDashboardTab(nil) // 无 client → fetch() 返回 nil

	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("DashboardTab should not rearm TickCmd on TickMsg (App layer drives it), got non-nil cmd")
	}
}

// TestDashboardTab_InitDoesNotArmTick 验证 DashboardTab.Init 不再装弹 TickCmd。
func TestDashboardTab_InitDoesNotArmTick(t *testing.T) {
	tab := NewDashboardTab(nil)
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("DashboardTab.Init should not arm TickCmd with nil client, got non-nil cmd")
	}
}
