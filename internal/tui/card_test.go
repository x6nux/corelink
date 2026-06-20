package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// RenderCard
// ---------------------------------------------------------------------------

func TestRenderCard_默认宽度(t *testing.T) {
	out := RenderCard("CPU", "12%")
	if len(out) == 0 {
		t.Fatal("RenderCard 返回空字符串")
	}
	// 渲染结果应包含标签和值文本
	if !strings.Contains(out, "CPU") {
		t.Error("输出应包含标签 'CPU'")
	}
	if !strings.Contains(out, "12%") {
		t.Error("输出应包含值 '12%'")
	}
}

func TestRenderCard_自定义宽度(t *testing.T) {
	opts := CardOpts{Width: 30}
	out := RenderCard("内存", "256MB", opts)
	if len(out) == 0 {
		t.Fatal("RenderCard 返回空字符串")
	}
	if !strings.Contains(out, "内存") {
		t.Error("输出应包含标签 '内存'")
	}
}

func TestRenderCard_自定义颜色(t *testing.T) {
	opts := CardOpts{ValueColor: lipgloss.Color("42")}
	out := RenderCard("状态", "在线", opts)
	if len(out) == 0 {
		t.Fatal("RenderCard 返回空字符串")
	}
	if !strings.Contains(out, "在线") {
		t.Error("输出应包含值 '在线'")
	}
}

func TestRenderCard_长文本截断(t *testing.T) {
	longValue := strings.Repeat("x", 100)
	out := RenderCard("测试", longValue)
	if len(out) == 0 {
		t.Fatal("RenderCard 返回空字符串")
	}
	// 截断后应包含省略号
	if !strings.Contains(out, "…") {
		t.Error("超长值应被截断并附加省略号")
	}
}

// ---------------------------------------------------------------------------
// JoinCards
// ---------------------------------------------------------------------------

func TestJoinCards_空输入(t *testing.T) {
	got := JoinCards()
	if got != "" {
		t.Errorf("JoinCards() = %q, want empty", got)
	}
}

func TestJoinCards_单卡片(t *testing.T) {
	card := RenderCard("A", "1")
	got := JoinCards(card)
	if len(got) == 0 {
		t.Fatal("JoinCards 单卡片不应返回空字符串")
	}
	// 单卡片结果应包含卡片内容
	if !strings.Contains(got, "A") {
		t.Error("单卡片结果应包含标签")
	}
}

func TestJoinCards_多卡片(t *testing.T) {
	c1 := RenderCard("A", "1")
	c2 := RenderCard("B", "2")
	c3 := RenderCard("C", "3")
	got := JoinCards(c1, c2, c3)
	if len(got) == 0 {
		t.Fatal("JoinCards 多卡片不应返回空字符串")
	}
	// 横向拼接后所有标签都应出现在输出中
	for _, label := range []string{"A", "B", "C"} {
		if !strings.Contains(got, label) {
			t.Errorf("输出应包含标签 %q", label)
		}
	}
}
