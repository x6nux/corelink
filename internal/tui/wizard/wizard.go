// Package wizard 提供基于 Bubble Tea 的多步表单框架。
package wizard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/x6nux/corelink/internal/tui"
)

// Field 表单字段定义。
type Field struct {
	Label       string   // 显示标签
	Key         string   // JSON key
	Default     string   // 默认值
	Password    bool     // 密码模式
	Options     []string // 非空时为单选（Select 模式）
	Required    bool     // 必填
	Description string   // 帮助文本
	CharLimit   int      // 0 表示用默认 256
}

// Step 一步（含多个 Field）。
type Step struct {
	Title  string
	Fields []Field
}

// Wizard 多步表单 Model。
type Wizard struct {
	steps       []Step
	current     int                 // 当前步骤
	fields      [][]textinput.Model // 每步的输入框
	selectIdx   [][]int             // 每步每字段的当前选中项索引（Select 模式用）
	activeField int                 // 当前步骤内的活跃字段
	done        bool
	cancelled   bool
}

// New 创建多步表单 Wizard。
func New(steps []Step) *Wizard {
	w := &Wizard{
		steps:     steps,
		fields:    make([][]textinput.Model, len(steps)),
		selectIdx: make([][]int, len(steps)),
	}
	for i, step := range steps {
		w.fields[i] = make([]textinput.Model, len(step.Fields))
		w.selectIdx[i] = make([]int, len(step.Fields))
		for j, f := range step.Fields {
			ti := textinput.New()
			ti.Placeholder = f.Default
			ti.CharLimit = 256
			if f.CharLimit > 0 {
				ti.CharLimit = f.CharLimit
			}
			ti.Width = 40
			if f.Password {
				ti.EchoMode = textinput.EchoPassword
			}
			// Select 模式：预填默认选项。
			if len(f.Options) > 0 {
				idx := 0
				for k, opt := range f.Options {
					if opt == f.Default {
						idx = k
						break
					}
				}
				w.selectIdx[i][j] = idx
				ti.SetValue(f.Options[idx])
			}
			if i == 0 && j == 0 {
				ti.Focus()
			}
			w.fields[i][j] = ti
		}
	}
	return w
}

// Init 返回初始 Cmd。
func (w *Wizard) Init() tea.Cmd {
	if len(w.fields) > 0 && len(w.fields[0]) > 0 {
		return w.fields[0][0].Focus()
	}
	return nil
}

// Update 处理消息。
func (w *Wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if w.done || w.cancelled {
		return w, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return w.handleKey(msg)
	}

	// 委派给活跃输入框。
	return w.updateActiveInput(msg)
}

// handleKey 处理按键。
func (w *Wizard) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	step := w.steps[w.current]
	fields := w.fields[w.current]

	// Select 模式字段：左/右/j/k 切换选项。
	if len(step.Fields[w.activeField].Options) > 0 {
		switch msg.String() {
		case "left", "k":
			idx := &w.selectIdx[w.current][w.activeField]
			opts := step.Fields[w.activeField].Options
			*idx = (*idx - 1 + len(opts)) % len(opts)
			fields[w.activeField].SetValue(opts[*idx])
			return w, nil
		case "right", "j":
			idx := &w.selectIdx[w.current][w.activeField]
			opts := step.Fields[w.activeField].Options
			*idx = (*idx + 1) % len(opts)
			fields[w.activeField].SetValue(opts[*idx])
			return w, nil
		}
	}

	switch msg.String() {
	case "esc":
		w.cancelled = true
		return w, tea.Quit

	case "tab", "down":
		return w.nextField()

	case "shift+tab", "up":
		return w.prevField()

	case "enter":
		// 验证 required 字段。
		if step.Fields[w.activeField].Required && w.fieldValue(w.current, w.activeField) == "" {
			// 不前进，留在当前字段。
			return w, nil
		}
		if w.activeField < len(fields)-1 {
			return w.nextField()
		}
		// 最后一个字段：下一步或完成。
		return w.nextStep()
	}

	// 非 Select 模式才把按键发给 textinput。
	if len(step.Fields[w.activeField].Options) > 0 {
		return w, nil
	}
	return w.updateActiveInput(msg)
}

// nextField 切换到下一个字段。
func (w *Wizard) nextField() (tea.Model, tea.Cmd) {
	fields := w.fields[w.current]
	fields[w.activeField].Blur()
	w.activeField = (w.activeField + 1) % len(fields)
	return w, fields[w.activeField].Focus()
}

// prevField 切换到上一个字段。
func (w *Wizard) prevField() (tea.Model, tea.Cmd) {
	fields := w.fields[w.current]
	fields[w.activeField].Blur()
	w.activeField = (w.activeField - 1 + len(fields)) % len(fields)
	return w, fields[w.activeField].Focus()
}

// nextStep 前进到下一步或完成。
func (w *Wizard) nextStep() (tea.Model, tea.Cmd) {
	if w.current >= len(w.steps)-1 {
		w.done = true
		return w, tea.Quit
	}
	w.fields[w.current][w.activeField].Blur()
	w.current++
	w.activeField = 0
	if len(w.fields[w.current]) > 0 {
		return w, w.fields[w.current][0].Focus()
	}
	return w, nil
}

// updateActiveInput 把消息委派给当前活跃输入框。
func (w *Wizard) updateActiveInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if w.current < len(w.fields) && w.activeField < len(w.fields[w.current]) {
		var cmd tea.Cmd
		w.fields[w.current][w.activeField], cmd = w.fields[w.current][w.activeField].Update(msg)
		return w, cmd
	}
	return w, nil
}

// View 渲染界面。
func (w *Wizard) View() string {
	if w.done {
		return tui.StyleTitle.Render("✓ 配置完成") + "\n"
	}
	if w.cancelled {
		return tui.StyleError.Render("✗ 已取消") + "\n"
	}

	var b strings.Builder

	// 进度指示。
	progress := fmt.Sprintf("  步骤 %d / %d", w.current+1, len(w.steps))
	b.WriteString(lipgloss.NewStyle().Foreground(tui.ColorMuted).Render(progress))
	b.WriteByte('\n')

	step := w.steps[w.current]
	// 步骤标题。
	b.WriteString(tui.StyleTitle.Render(step.Title))
	b.WriteString("\n\n")

	// 字段列表。
	for i, f := range step.Fields {
		field := w.fields[w.current][i]
		label := f.Label
		if f.Required {
			label += " *"
		}

		if i == w.activeField {
			b.WriteString(lipgloss.NewStyle().
				Foreground(tui.ColorPrimary).
				Bold(true).
				Render("▸ " + label))
		} else {
			b.WriteString(lipgloss.NewStyle().
				Foreground(tui.ColorMuted).
				Render("  " + label))
		}
		b.WriteByte('\n')

		// Select 模式：显示选项列表。
		if len(f.Options) > 0 {
			idx := w.selectIdx[w.current][i]
			var opts []string
			for k, opt := range f.Options {
				if k == idx {
					opts = append(opts, lipgloss.NewStyle().
						Foreground(tui.ColorPrimary).
						Bold(true).
						Render("["+opt+"]"))
				} else {
					opts = append(opts, lipgloss.NewStyle().
						Foreground(tui.ColorMuted).
						Render(" "+opt+" "))
				}
			}
			b.WriteString("    " + strings.Join(opts, " "))
		} else {
			b.WriteString("    " + field.View())
		}
		b.WriteByte('\n')

		// 帮助文本。
		if f.Description != "" {
			b.WriteString(tui.StyleHelp.Render("    " + f.Description))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	// 底部提示。
	var hint string
	if w.current < len(w.steps)-1 {
		hint = "[Enter:下一步] [Tab/↓:下一字段] [Shift-Tab/↑:上一字段] [Esc:取消]"
	} else {
		hint = "[Enter:完成] [Tab/↓:下一字段] [Shift-Tab/↑:上一字段] [Esc:取消]"
	}
	b.WriteString(tui.StyleHelp.Render(hint))
	b.WriteByte('\n')

	return b.String()
}

// fieldValue 返回指定步骤字段的值，空则回落 Default。
func (w *Wizard) fieldValue(step, field int) string {
	v := w.fields[step][field].Value()
	if v == "" {
		v = w.steps[step].Fields[field].Default
	}
	return v
}

// Values 返回所有字段值 map[key]value。
func (w *Wizard) Values() map[string]string {
	vals := make(map[string]string)
	for i, step := range w.steps {
		for j, f := range step.Fields {
			vals[f.Key] = w.fieldValue(i, j)
		}
	}
	return vals
}

// Done 是否完成。
func (w *Wizard) Done() bool { return w.done }

// Cancelled 是否取消。
func (w *Wizard) Cancelled() bool { return w.cancelled }
