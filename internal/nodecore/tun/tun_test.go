package tun

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// TestFakeTUNRoundTrip 验证 fakeTUN 的注入/写出双向队列：
//   - Inject 注入的包可被 Read 读到（host→设备方向）；
//   - Write 写入的包可被 Outbound 取到（设备→host 方向）。
func TestFakeTUNRoundTrip(t *testing.T) {
	dev := NewFakeTUN("utun-test", 1420)

	// 设备名/MTU。
	if name, _ := dev.Name(); name != "utun-test" {
		t.Fatalf("Name=%q, 期望 utun-test", name)
	}
	if mtu, _ := dev.MTU(); mtu != 1420 {
		t.Fatalf("MTU=%d, 期望 1420", mtu)
	}
	if dev.BatchSize() < 1 {
		t.Fatalf("BatchSize=%d, 期望 >=1", dev.BatchSize())
	}

	// host→设备：Inject 后 Read 读到。
	want := []byte("hello-inbound-packet")
	dev.Inject(want)

	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	n, err := dev.Read(bufs, sizes, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 1 {
		t.Fatalf("Read n=%d, 期望 1", n)
	}
	if got := bufs[0][:sizes[0]]; !bytes.Equal(got, want) {
		t.Fatalf("Read got=%q, 期望 %q", got, want)
	}

	// 设备→host：Write 后 Outbound 取到。
	out := []byte("bye-outbound-packet")
	if _, err := dev.Write([][]byte{out}, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case got := <-dev.Outbound():
		if !bytes.Equal(got, out) {
			t.Fatalf("Outbound got=%q, 期望 %q", got, out)
		}
	case <-time.After(time.Second):
		t.Fatal("超时等待 Outbound")
	}
}

// TestFakeTUNReadOffset 验证 Read 的 offset 语义（写入到每个 buf 的 offset 处）。
func TestFakeTUNReadOffset(t *testing.T) {
	dev := NewFakeTUN("utun-off", 1420)
	want := []byte("payload")
	dev.Inject(want)

	const offset = 4
	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	n, err := dev.Read(bufs, sizes, offset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 1 {
		t.Fatalf("Read n=%d", n)
	}
	if got := bufs[0][offset : offset+sizes[0]]; !bytes.Equal(got, want) {
		t.Fatalf("offset Read got=%q, 期望 %q", got, want)
	}
}

// TestFakeTUNClose 验证关闭后 Read 返回错误、Events 通道关闭。
func TestFakeTUNClose(t *testing.T) {
	dev := NewFakeTUN("utun-close", 1420)
	if err := dev.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	bufs := [][]byte{make([]byte, 64)}
	sizes := make([]int, 1)
	if _, err := dev.Read(bufs, sizes, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 Read err=%v, 期望 ErrClosed", err)
	}
	// Events 通道应被关闭。
	select {
	case _, ok := <-dev.Events():
		if ok {
			// EventUp 可能先发一个；再读应关闭。
			select {
			case _, ok2 := <-dev.Events():
				if ok2 {
					t.Fatal("Events 通道未关闭")
				}
			case <-time.After(time.Second):
				t.Fatal("超时等待 Events 关闭")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("超时等待 Events")
	}
}
