package tui

import (
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/x6nux/corelink/internal/rpc"
)

// ---------------------------------------------------------------------------
// mock Tab
// ---------------------------------------------------------------------------

type mockTab struct{ name string }

func (m mockTab) Name() string                        { return m.name }
func (m mockTab) Init() tea.Cmd                       { return nil }
func (m mockTab) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m mockTab) View() string                        { return "mock: " + m.name }

// cmdTab 是一个 Init 返回可识别 Cmd 的 mock Tab，用于验证 App.Init 是否收集了它的 Init。
type cmdTab struct {
	name string
	tag  string // Init 触发的 Msg 标签，便于断言
}

func (m cmdTab) Name() string { return m.name }
func (m cmdTab) Init() tea.Cmd {
	tag := m.tag
	return func() tea.Msg { return initTagMsg(tag) }
}
func (m cmdTab) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m cmdTab) View() string                        { return "cmd: " + m.name }

// initTagMsg 标识某个 Tab 的 Init 被执行过。
type initTagMsg string

// ---------------------------------------------------------------------------
// App tests
// ---------------------------------------------------------------------------

func TestApp_TabSwitch(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs: []Tab{
			mockTab{name: "A"},
			mockTab{name: "B"},
			mockTab{name: "C"},
		},
	})

	// 按数字键 "2" → 切到索引 1
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}
	app.Update(msg)

	if app.activeTab != 1 {
		t.Fatalf("expected activeTab=1, got %d", app.activeTab)
	}
}

func TestApp_TabWrap(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs: []Tab{
			mockTab{name: "A"},
			mockTab{name: "B"},
			mockTab{name: "C"},
		},
	})

	// 设置到最后一个 Tab
	app.activeTab = 2

	// 按 tab → 回到第一个
	msg := tea.KeyMsg{Type: tea.KeyTab}
	app.Update(msg)

	if app.activeTab != 0 {
		t.Fatalf("expected activeTab=0 (wrapped), got %d", app.activeTab)
	}

	// 在第一个按 shift+tab → 回到最后一个
	msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	app.Update(msg)

	if app.activeTab != 2 {
		t.Fatalf("expected activeTab=2 (wrapped back), got %d", app.activeTab)
	}
}

func TestApp_Quit(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs:  []Tab{mockTab{name: "A"}},
	})

	msg := tea.KeyMsg{Type: tea.KeyF10}
	_, cmd := app.Update(msg)

	if cmd == nil {
		t.Fatal("expected quit cmd, got nil")
	}

	// tea.Quit 返回 tea.QuitMsg
	result := cmd()
	if _, ok := result.(tea.QuitMsg); !ok {
		t.Fatalf("expected QuitMsg, got %T", result)
	}
}

func TestApp_WindowSize(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs:  []Tab{mockTab{name: "A"}},
	})

	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	app.Update(msg)

	if app.width != 120 {
		t.Fatalf("expected width=120, got %d", app.width)
	}
	if app.height != 40 {
		t.Fatalf("expected height=40, got %d", app.height)
	}
}

func TestApp_View(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test app",
		Tabs: []Tab{
			mockTab{name: "T1"},
			mockTab{name: "T2"},
		},
	})
	app.width = 80

	view := app.View()
	if len(view) == 0 {
		t.Fatal("expected non-empty view")
	}
}

func TestApp_DelegateToActiveTab(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs: []Tab{
			mockTab{name: "A"},
			mockTab{name: "B"},
		},
	})

	// 非按键消息应该被委派给活跃 Tab（不崩溃即可）
	app.Update(TickMsg{})
}

func TestApp_Init(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs:  []Tab{mockTab{name: "A"}},
	})

	// App.Init 现在固定装弹 TickCmd（#13），即便 mockTab.Init() 返回 nil 也应返回非 nil
	cmd := app.Init()
	if cmd == nil {
		t.Fatal("expected non-nil cmd from Init (App arms TickCmd)")
	}
}

func TestApp_InitEmpty(t *testing.T) {
	app := NewApp(AppConfig{Title: "test"})
	// 无 Tab 时 App.Init 仍装弹 TickCmd（Tick 链与 Tab 无关）
	cmd := app.Init()
	if cmd == nil {
		t.Fatal("expected non-nil cmd from Init (App arms TickCmd even with no tabs)")
	}
}

func TestApp_Init_BatchesAllTabs(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs: []Tab{
			cmdTab{name: "A", tag: "a"},
			cmdTab{name: "B", tag: "b"},
			cmdTab{name: "C", tag: "c"},
		},
	})

	cmd := app.Init()
	if cmd == nil {
		t.Fatal("expected non-nil cmd from Init with multiple cmd tabs")
	}

	// Init 返回的应是 BatchMsg：含 App 层装弹的 TickCmd（#13）+ 全部 3 个 Tab 的 Init Cmd。
	// 不写死 batch 长度（避免与 #13 的 TickCmd 装弹耦合）——只断言每个 Tab 的 Init 标签都被收集到。
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", msg)
	}

	// 逐个执行 batch 内的 Cmd，收集 Tab 的 initTagMsg 标签（TickCmd 产出 TickMsg，忽略）。
	got := map[initTagMsg]bool{}
	for _, c := range batch {
		if tag, ok := c().(initTagMsg); ok {
			got[tag] = true
		}
	}
	for _, want := range []initTagMsg{"a", "b", "c"} {
		if !got[want] {
			t.Fatalf("missing Init from tab tag %q; got %v", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// #13 — Tick 链上移到 App 层：切到不续约 Tab 后 Tick 不断
// ---------------------------------------------------------------------------

// tickCountingTab 是一个「不自行续约」的 mock Tab：
// 收到 TickMsg 只累加计数，从不返回 TickCmd（模拟 acl/certs/nodes 等真实 Tab）。
type tickCountingTab struct {
	name      string
	tickCount *int
}

func (m tickCountingTab) Name() string  { return m.name }
func (m tickCountingTab) Init() tea.Cmd { return nil }
func (m tickCountingTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(TickMsg); ok {
		*m.tickCount++
	}
	return m, nil
}
func (m tickCountingTab) View() string { return "tick: " + m.name }

// TestApp_TickChainRearmsAtAppLayer 验证：即使活跃 Tab 不自行续约，
// App.Update 收到 TickMsg 后也无条件重装弹 TickCmd，使 Tick 链不断。
func TestApp_TickChainRearmsAtAppLayer(t *testing.T) {
	cnt := 0
	app := NewApp(AppConfig{
		Title: "test",
		Tabs:  []Tab{tickCountingTab{name: "noRearm", tickCount: &cnt}},
	})

	_, cmd := app.Update(TickMsg{})

	// 活跃 Tab 应已收到这一拍 TickMsg
	if cnt != 1 {
		t.Fatalf("expected active tab tickCount=1, got %d", cnt)
	}
	// App 必须返回非 nil Cmd（重装弹 TickCmd），否则 Tick 链断裂
	if cmd == nil {
		t.Fatal("expected App to rearm TickCmd on TickMsg, got nil cmd")
	}
	// 执行返回的 Cmd 应最终产出 TickMsg（确认是 Tick 续约而非别的 Cmd）
	msg := cmd()
	if _, ok := msg.(TickMsg); !ok {
		t.Fatalf("expected rearmed cmd to yield TickMsg, got %T", msg)
	}
}

// TestApp_InitArmsTickChain 验证：App.Init 启动时即装弹 TickCmd（不依赖任何 Tab 自行装弹）。
func TestApp_InitArmsTickChain(t *testing.T) {
	cnt := 0
	app := NewApp(AppConfig{
		Title: "test",
		Tabs:  []Tab{tickCountingTab{name: "noRearm", tickCount: &cnt}},
	})

	cmd := app.Init()
	if cmd == nil {
		t.Fatal("expected App.Init to arm TickCmd, got nil cmd")
	}
}

func TestApp_Connected(t *testing.T) {
	// 有 client 时 connected=true
	app := NewApp(AppConfig{
		Title:  "test",
		Client: &RPCClient{},
	})
	if !app.connected {
		t.Fatal("expected connected=true when client is provided")
	}

	// 无 client 时 connected=false
	app2 := NewApp(AppConfig{Title: "test"})
	if app2.connected {
		t.Fatal("expected connected=false when client is nil")
	}
}

func TestApp_TabSwitch_TriggersInit(t *testing.T) {
	app := NewApp(AppConfig{
		Title: "test",
		Tabs: []Tab{
			cmdTab{name: "A", tag: "a"},
			cmdTab{name: "B", tag: "b"},
			cmdTab{name: "C", tag: "c"},
		},
	})

	// 数字键 "2" 切到索引 1（B），应返回 B.Init() 的 Cmd。
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if cmd == nil {
		t.Fatal("expected Init cmd when switching tab via number key")
	}
	if tag, ok := cmd().(initTagMsg); !ok || tag != "b" {
		t.Fatalf("expected initTagMsg(\"b\") from number-key switch, got %#v", cmd())
	}

	// tab 键 → 切到索引 2（C），应返回 C.Init() 的 Cmd。
	_, cmd = app.Update(tea.KeyMsg{Type: tea.KeyTab})
	if cmd == nil {
		t.Fatal("expected Init cmd when switching tab via tab key")
	}
	if tag, ok := cmd().(initTagMsg); !ok || tag != "c" {
		t.Fatalf("expected initTagMsg(\"c\") from tab-key switch, got %#v", cmd())
	}

	// shift+tab → 回到索引 1（B），应返回 B.Init() 的 Cmd。
	_, cmd = app.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if cmd == nil {
		t.Fatal("expected Init cmd when switching tab via shift+tab")
	}
	if tag, ok := cmd().(initTagMsg); !ok || tag != "b" {
		t.Fatalf("expected initTagMsg(\"b\") from shift+tab switch, got %#v", cmd())
	}
}

// ---------------------------------------------------------------------------
// RPCClient test
// ---------------------------------------------------------------------------

func TestRPCClient_Call(t *testing.T) {
	// 启动 mock RPC server
	srv := rpc.NewServer()
	srv.Register("echo", func(params json.RawMessage) (any, error) {
		var m map[string]string
		if err := json.Unmarshal(params, &m); err != nil {
			return nil, err
		}
		return m, nil
	})
	sockPath := startRPCServer(t, srv)

	client, err := NewRPCClient(sockPath)
	if err != nil {
		t.Fatalf("NewRPCClient: %v", err)
	}
	defer client.Close()

	// Call 返回 tea.Cmd，手动执行它
	cmd := client.Call("echo", map[string]string{"msg": "hello"}, func() any {
		return &map[string]string{}
	})

	result := cmd()
	rpcResult, ok := result.(RPCResult)
	if !ok {
		t.Fatalf("expected RPCResult, got %T", result)
	}
	if rpcResult.Err != nil {
		t.Fatalf("unexpected error: %v", rpcResult.Err)
	}
	if rpcResult.Method != "echo" {
		t.Fatalf("expected method=echo, got %s", rpcResult.Method)
	}

	m, ok := rpcResult.Result.(*map[string]string)
	if !ok {
		t.Fatalf("expected *map[string]string, got %T", rpcResult.Result)
	}
	if (*m)["msg"] != "hello" {
		t.Fatalf("expected msg=hello, got %s", (*m)["msg"])
	}
}

func TestRPCClient_CallError(t *testing.T) {
	srv := rpc.NewServer()
	// 不注册任何方法 → method not found
	sockPath := startRPCServer(t, srv)

	client, err := NewRPCClient(sockPath)
	if err != nil {
		t.Fatalf("NewRPCClient: %v", err)
	}
	defer client.Close()

	cmd := client.Call("nonexistent", nil, func() any { return new(struct{}) })
	result := cmd()
	rpcResult, ok := result.(RPCResult)
	if !ok {
		t.Fatalf("expected RPCResult, got %T", result)
	}
	if rpcResult.Err == nil {
		t.Fatal("expected error for nonexistent method")
	}
}
