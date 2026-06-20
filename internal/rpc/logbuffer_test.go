package rpc

import (
	"bytes"
	"log/slog"
	"testing"
	"time"
)

// ── LogBuffer 基础测试 ─────────────────────────────────────────────────────────

func TestLogBuffer_AddAndRecent(t *testing.T) {
	buf := NewLogBuffer(4)

	// 空缓冲区返回 nil
	if got := buf.Recent(5); got != nil {
		t.Fatalf("空缓冲区 Recent 应返回 nil，got %v", got)
	}

	// 添加 2 条，请求 5 条，只返回 2 条
	buf.Add(LogEntry{Message: "m1", Level: "INFO"})
	buf.Add(LogEntry{Message: "m2", Level: "WARN"})
	got := buf.Recent(5)
	if len(got) != 2 {
		t.Fatalf("Recent(5) len = %d, want 2", len(got))
	}
	if got[0].Message != "m1" || got[1].Message != "m2" {
		t.Errorf("Recent 顺序错误: %v", got)
	}
}

func TestLogBuffer_Wrap(t *testing.T) {
	// 容量 3，写入 5 条，验证环形覆盖
	buf := NewLogBuffer(3)
	for i := range 5 {
		buf.Add(LogEntry{Message: string(rune('a' + i))}) // a,b,c,d,e
	}

	got := buf.Recent(3)
	if len(got) != 3 {
		t.Fatalf("Recent(3) len = %d, want 3", len(got))
	}
	// 最近 3 条应为 c,d,e
	want := []string{"c", "d", "e"}
	for i, w := range want {
		if got[i].Message != w {
			t.Errorf("got[%d].Message = %q, want %q", i, got[i].Message, w)
		}
	}

	// 请求 2 条，只返回最新 2 条
	got2 := buf.Recent(2)
	if len(got2) != 2 {
		t.Fatalf("Recent(2) len = %d, want 2", len(got2))
	}
	if got2[0].Message != "d" || got2[1].Message != "e" {
		t.Errorf("Recent(2) = %v, want [d,e]", got2)
	}
}

func TestLogBuffer_RecentZero(t *testing.T) {
	buf := NewLogBuffer(4)
	buf.Add(LogEntry{Message: "x"})
	if got := buf.Recent(0); got != nil {
		t.Errorf("Recent(0) 应返回 nil，got %v", got)
	}
}

func TestLogBuffer_ExactCapacity(t *testing.T) {
	// 恰好填满容量的边界情况
	buf := NewLogBuffer(3)
	buf.Add(LogEntry{Message: "a"})
	buf.Add(LogEntry{Message: "b"})
	buf.Add(LogEntry{Message: "c"})

	got := buf.Recent(3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Message != "a" || got[2].Message != "c" {
		t.Errorf("got = %v", got)
	}
}

// ── slog.Handler 集成测试 ──────────────────────────────────────────────────────

func TestLogBuffer_SlogHandler(t *testing.T) {
	buf := NewLogBuffer(10)
	base := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := buf.Handler(base)

	logger := slog.New(handler)
	logger.Info("hello", "key", "val")
	logger.Warn("warning msg")

	entries := buf.Recent(10)
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	if entries[0].Message != "hello" || entries[0].Level != "INFO" {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	if entries[1].Message != "warning msg" || entries[1].Level != "WARN" {
		t.Errorf("entries[1] = %+v", entries[1])
	}
	// attrs 应包含 key=val
	if entries[0].Attrs == "" {
		t.Error("entries[0].Attrs 应非空")
	}
}

func TestLogBuffer_SlogHandlerWithGroup(t *testing.T) {
	buf := NewLogBuffer(10)
	base := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := buf.Handler(base)

	logger := slog.New(handler).WithGroup("mygroup")
	logger.Info("grouped", "k", "v")

	entries := buf.Recent(1)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	// group 名应出现在 attrs 中
	if entries[0].Attrs == "" {
		t.Error("grouped 日志 Attrs 应非空")
	}
}

func TestLogBuffer_SlogHandlerWithAttrs(t *testing.T) {
	buf := NewLogBuffer(10)
	base := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := buf.Handler(base)

	logger := slog.New(handler.WithAttrs([]slog.Attr{slog.String("component", "test")}))
	logger.Info("with-attrs")

	entries := buf.Recent(1)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Message != "with-attrs" {
		t.Errorf("message = %q", entries[0].Message)
	}
}

func TestLogBuffer_SlogHandlerEnabled(t *testing.T) {
	buf := NewLogBuffer(10)
	// base 只启用 Warn 及以上
	base := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	handler := buf.Handler(base)

	if handler.Enabled(nil, slog.LevelInfo) {
		t.Error("Info 不应被启用（base 阈值 Warn）")
	}
	if !handler.Enabled(nil, slog.LevelWarn) {
		t.Error("Warn 应被启用")
	}
}

func TestLogBuffer_EntryTimePreserved(t *testing.T) {
	buf := NewLogBuffer(5)
	now := time.Now()
	buf.Add(LogEntry{Time: now, Level: "INFO", Message: "ts"})

	entries := buf.Recent(1)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if !entries[0].Time.Equal(now) {
		t.Errorf("时间不一致: got %v, want %v", entries[0].Time, now)
	}
}

func TestLogBuffer_SlogNestedGroup(t *testing.T) {
	buf := NewLogBuffer(10)
	base := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := buf.Handler(base)

	// 嵌套 group：outer.inner
	logger := slog.New(handler).WithGroup("outer").WithGroup("inner")
	logger.Info("nested", "x", 1)

	entries := buf.Recent(1)
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	// attrs 应包含 outer.inner 前缀
	if entries[0].Attrs == "" {
		t.Error("嵌套 group 日志 Attrs 应非空")
	}
}
