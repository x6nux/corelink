package configsvc

import (
	"testing"
)

// 测试 ChangeSignalMsg 结构体与 NewNotify 构造函数。

func TestChangeSignalMsgFields(t *testing.T) {
	// 验证 ChangeSignalMsg 字段赋值正确。
	msg := &ChangeSignalMsg{Changed: true, Generation: 99}
	if !msg.Changed {
		t.Error("Changed 应为 true")
	}
	if msg.Generation != 99 {
		t.Errorf("Generation = %d, 期望 99", msg.Generation)
	}
}

func TestNewNotifyReturnsNonNil(t *testing.T) {
	// 验证 NewNotify 返回非 nil。
	bumper := newStubBumper()
	n := NewNotify(bumper)
	if n == nil {
		t.Fatal("NewNotify 不应返回 nil")
	}
	n.Close()
}

func TestNewNotifyInitialState(t *testing.T) {
	// 验证新建的 Notify 初始状态：无订阅者、任何节点不在线。
	n := NewNotify(newStubBumper())
	defer n.Close()
	if n.OnlineCount("any-node") != 0 {
		t.Errorf("初始 OnlineCount 应为 0, 实际 %d", n.OnlineCount("any-node"))
	}
	if n.IsOnline("any-node") {
		t.Error("初始状态任何节点不应在线")
	}
}

func TestNotifyCloseIdempotent(t *testing.T) {
	// 多次 Close 不 panic。
	n := NewNotify(newStubBumper())
	n.Close()
	n.Close() // 第二次不应 panic
	n.Close() // 第三次也不应 panic
}
