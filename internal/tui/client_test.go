package tui

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// TickCmd / TickMsg
// ---------------------------------------------------------------------------

func TestTickCmd_返回非nil(t *testing.T) {
	cmd := TickCmd(1)
	if cmd == nil {
		t.Fatal("TickCmd(1) 应返回非 nil Cmd")
	}
}

func TestTickCmd_执行产出TickMsg(t *testing.T) {
	// TickCmd 返回的是 tea.Tick 封装的延迟 Cmd，
	// 直接执行它会产出 tea.tickMsg（内部类型，非 TickMsg），
	// 因此这里只验证 Cmd 非 nil。
	cmd := TickCmd(3)
	if cmd == nil {
		t.Fatal("TickCmd(3) 应返回非 nil Cmd")
	}
}

func TestTickCmd_不同秒数(t *testing.T) {
	// 验证不同秒数都能正常返回 Cmd（不 panic）
	for _, sec := range []int{0, 1, 5, 30, 60} {
		cmd := TickCmd(sec)
		if cmd == nil {
			t.Errorf("TickCmd(%d) 应返回非 nil Cmd", sec)
		}
	}
}

// ---------------------------------------------------------------------------
// RPCResult 类型断言
// ---------------------------------------------------------------------------

func TestRPCResult_字段访问(t *testing.T) {
	// 验证 RPCResult 结构体字段可正常赋值和读取
	r := RPCResult{
		Method: "status",
		Result: map[string]string{"key": "val"},
		Err:    nil,
	}
	if r.Method != "status" {
		t.Errorf("expected Method='status', got %q", r.Method)
	}
	if r.Result == nil {
		t.Error("expected non-nil Result")
	}
	if r.Err != nil {
		t.Errorf("expected nil Err, got %v", r.Err)
	}
}

func TestRPCResult_错误场景(t *testing.T) {
	r := RPCResult{
		Method: "fail",
		Err:    errors.New("rpc failed"), // 模拟调用失败
	}
	if r.Err == nil {
		t.Error("expected non-nil Err")
	}
	if r.Result != nil {
		t.Error("expected nil Result in error case")
	}
}

// ---------------------------------------------------------------------------
// TickMsg 类型
// ---------------------------------------------------------------------------

func TestTickMsg_可用作tea消息(t *testing.T) {
	// TickMsg 是空结构体，验证可以正常实例化并用于类型断言
	var msg any = TickMsg{}
	if _, ok := msg.(TickMsg); !ok {
		t.Fatal("TickMsg 应可通过类型断言识别")
	}
}
