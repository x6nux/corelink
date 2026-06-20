package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// LogEntry 环形缓冲区中的一条日志。
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Attrs   string    `json:"attrs,omitempty"`
}

// LogBuffer 线程安全的日志环形缓冲区。
type LogBuffer struct {
	mu      sync.RWMutex
	entries []LogEntry
	cap     int
	idx     int // 下一写入位置
	full    bool
}

// NewLogBuffer 创建指定容量的环形缓冲区。
func NewLogBuffer(capacity int) *LogBuffer {
	return &LogBuffer{
		entries: make([]LogEntry, capacity),
		cap:     capacity,
	}
}

// Add 追加一条日志。
func (b *LogBuffer) Add(entry LogEntry) {
	b.mu.Lock()
	b.entries[b.idx] = entry
	b.idx = (b.idx + 1) % b.cap
	if b.idx == 0 {
		b.full = true
	}
	b.mu.Unlock()
}

// Recent 返回最近 n 条日志（时间升序）。
func (b *LogBuffer) Recent(n int) []LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	total := b.idx
	if b.full {
		total = b.cap
	}
	if n > total {
		n = total
	}
	if n == 0 {
		return nil
	}

	result := make([]LogEntry, n)
	start := b.idx - n
	if start < 0 {
		start += b.cap
	}
	for i := range n {
		result[i] = b.entries[(start+i)%b.cap]
	}
	return result
}

// Handler 返回一个 slog.Handler 将日志写入此缓冲区（同时转发到 base handler）。
func (b *LogBuffer) Handler(base slog.Handler) slog.Handler {
	return &logBufHandler{buf: b, base: base}
}

type logBufHandler struct {
	buf   *LogBuffer
	base  slog.Handler
	group string
	attrs []slog.Attr
}

func (h *logBufHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *logBufHandler) Handle(ctx context.Context, r slog.Record) error {
	var attrs string
	// 先输出 WithAttrs 注册的属性，再输出本条记录的属性。
	for _, a := range h.attrs {
		if attrs != "" {
			attrs += " "
		}
		attrs += fmt.Sprintf("%s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		if attrs != "" {
			attrs += " "
		}
		attrs += fmt.Sprintf("%s=%v", a.Key, a.Value)
		return true
	})
	if h.group != "" {
		attrs = h.group + ": " + attrs
	}

	h.buf.Add(LogEntry{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   attrs,
	})
	return h.base.Handle(ctx, r)
}

func (h *logBufHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &logBufHandler{
		buf:   h.buf,
		base:  h.base.WithAttrs(attrs),
		group: h.group,
		attrs: append(h.attrs, attrs...),
	}
}

func (h *logBufHandler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &logBufHandler{
		buf:   h.buf,
		base:  h.base.WithGroup(name),
		group: g,
		attrs: h.attrs,
	}
}
