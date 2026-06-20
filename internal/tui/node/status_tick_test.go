package node

import (
	"testing"

	"github.com/x6nux/corelink/internal/tui"
)

// TestStatusTab_DoesNotRearmTick 验证 #13 配套清理：
// StatusTab 收到 TickMsg 后不再自行装弹 TickCmd（Tick 链已由 App 层统一驱动）。
// 无 client 时 fetch() 返回 nil，因此整体应返回 nil Cmd —— 若仍含 TickCmd 则非 nil。
func TestStatusTab_DoesNotRearmTick(t *testing.T) {
	tab := NewStatusTab() // 无 client → fetch() 返回 nil

	_, cmd := tab.Update(tui.TickMsg{})
	if cmd != nil {
		t.Fatalf("StatusTab should not rearm TickCmd on TickMsg (App layer drives it), got non-nil cmd")
	}
}

// TestStatusTab_InitDoesNotArmTick 验证 StatusTab.Init 不再装弹 TickCmd。
func TestStatusTab_InitDoesNotArmTick(t *testing.T) {
	tab := NewStatusTab()
	if cmd := tab.Init(); cmd != nil {
		t.Fatalf("StatusTab.Init should not arm TickCmd with nil client, got non-nil cmd")
	}
}
