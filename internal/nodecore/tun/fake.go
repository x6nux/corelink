package tun

import (
	"log/slog"
	"os"
	"sync"
)

// fakeTUN 是纯内存的 TUN 设备实现，供单元测试使用（无需特权）。
//
// 数据流：
//
//	host→设备方向：Inject(pkt) 入 inbound 队列，Read 从中取出（喂给 WG）。
//	设备→host方向：Write(pkt) 入 outbound 队列，Outbound() 取出（断言用）。
//
// 编译期断言 fakeTUN 满足本地 Device 接口。
type fakeTUN struct {
	name string
	mtu  int

	inbound  chan []byte // host→设备：Read 取出
	outbound chan []byte // 设备→host：Write 写入
	events   chan Event

	done   chan struct{} // 关闭信号，用 select 保护 channel 发送避免 send-on-closed panic
	mu     sync.Mutex
	closed bool
}

var _ Device = (*fakeTUN)(nil)

// NewFakeTUN 创建一个内存 TUN 设备，队列容量足够测试用。
func NewFakeTUN(name string, mtu int) *fakeTUN {
	t := &fakeTUN{
		name:     name,
		mtu:      mtu,
		inbound:  make(chan []byte, 256),
		outbound: make(chan []byte, 256),
		events:   make(chan Event, 4),
		done:     make(chan struct{}),
	}
	// 模拟设备启动事件（与真实 TUN 行为一致，便于 device.Up 流转）。
	t.events <- EventUp
	return t
}

// Inject 模拟 host 侧发来一个待 WG 处理的包（host→设备）。
// 使用 done channel + select 模式避免 Close() 后 send-on-closed-channel panic。
func (t *fakeTUN) Inject(pkt []byte) {
	cp := append([]byte(nil), pkt...)
	select {
	case <-t.done:
		return
	default:
	}
	select {
	case t.inbound <- cp:
	case <-t.done:
	}
}

// Outbound 返回设备→host 方向的包通道（WG 解密后写出的包）。
func (t *fakeTUN) Outbound() <-chan []byte { return t.outbound }

func (t *fakeTUN) File() *os.File { return nil }

// Read 从 inbound 队列取一个包写入 bufs[0][offset:]。阻塞直到有包或关闭。
func (t *fakeTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case pkt, ok := <-t.inbound:
		if !ok {
			return 0, ErrClosed
		}
		n := copy(bufs[0][offset:], pkt)
		sizes[0] = n
		return 1, nil
	case <-t.done:
		return 0, ErrClosed
	}
}

// Write 把每个 buf[offset:] 作为一个包放入 outbound 队列。
// 使用 done channel + select 模式避免 Close() 后 send-on-closed-channel panic。
func (t *fakeTUN) Write(bufs [][]byte, offset int) (int, error) {
	select {
	case <-t.done:
		return 0, ErrClosed
	default:
	}
	count := 0
	for _, b := range bufs {
		if offset > len(b) {
			continue
		}
		cp := append([]byte(nil), b[offset:]...)
		select {
		case t.outbound <- cp:
			count++
		case <-t.done:
			return count, ErrClosed
		default:
			// 队列满则丢弃（测试场景不应发生）。
			slog.Debug("tun: fakeTUN outbound 队列满，丢弃包", "len", len(b[offset:]))
		}
	}
	return count, nil
}

func (t *fakeTUN) MTU() (int, error)     { return t.mtu, nil }
func (t *fakeTUN) Name() (string, error) { return t.name, nil }
func (t *fakeTUN) Events() <-chan Event  { return t.events }
func (t *fakeTUN) BatchSize() int        { return 1 }

func (t *fakeTUN) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	// 关闭 done 信号——Read/Inject/Write 通过 select <-t.done 退出，
	// 无需关闭 inbound/outbound 通道（它们有并发发送方，关闭会 panic）。
	// events 通道仅在构造时写入一次 EventUp，之后无发送方，可安全关闭。
	close(t.done)
	close(t.events)
	return nil
}
