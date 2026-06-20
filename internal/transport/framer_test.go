package transport

import (
	"bytes"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestFramerStreamRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	f1 := NewStreamFramer(c1)
	f2 := NewStreamFramer(c2)

	dstVIP := netip.MustParseAddr("100.64.0.1")
	var dstRelay uint16 = 99
	var ttl uint8 = 5
	payload := []byte("framer roundtrip test")

	errCh := make(chan error, 1)
	go func() {
		errCh <- f1.WritePacket(dstVIP, dstRelay, ttl, payload)
	}()

	gotVIP, gotRelay, gotTTL, gotPayload, err := f2.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != dstRelay {
		t.Errorf("Relay: got %d, want %d", gotRelay, dstRelay)
	}
	if gotTTL != ttl {
		t.Errorf("TTL: got %d, want %d", gotTTL, ttl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, payload)
	}
}

func TestFramerStreamIPv6(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	f1 := NewStreamFramer(c1)
	f2 := NewStreamFramer(c2)

	dstVIP := netip.MustParseAddr("fd00::dead:beef")
	var dstRelay uint16 = 1
	var ttl uint8 = 64
	payload := []byte("ipv6 framer test")

	errCh := make(chan error, 1)
	go func() {
		errCh <- f1.WritePacket(dstVIP, dstRelay, ttl, payload)
	}()

	gotVIP, gotRelay, gotTTL, gotPayload, err := f2.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != dstRelay {
		t.Errorf("Relay: got %d, want %d", gotRelay, dstRelay)
	}
	if gotTTL != ttl {
		t.Errorf("TTL: got %d, want %d", gotTTL, ttl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, payload)
	}
}

func TestFramerKeepalive(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	f1 := NewStreamFramer(c1)

	var seq uint64 = 12345

	errCh := make(chan error, 1)
	go func() {
		errCh <- f1.WriteKeepalive(seq)
	}()

	// 直接用 ReadStreamFrame 读取原始帧，绕过 ReadPacket 的自动 echo 回复（会在 net.Pipe 上死锁）。
	_, _, _, gotPayload, err := ReadStreamFrame(c2)
	if err != nil {
		t.Fatalf("ReadStreamFrame: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteKeepalive: %v", err)
	}
	if len(gotPayload) != 8 {
		t.Fatalf("keepalive payload 长度: got %d, want 8", len(gotPayload))
	}
}

func TestFramerConcurrentWrites(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	f1 := NewStreamFramer(c1)
	f2 := NewStreamFramer(c2)

	const goroutines = 10
	const packetsPerGoroutine = 50

	dstVIP := netip.MustParseAddr("100.64.0.1")
	payload := bytes.Repeat([]byte("X"), 100)

	// 启动并发写
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(relay uint16) {
			defer wg.Done()
			for range packetsPerGoroutine {
				if err := f1.WritePacket(dstVIP, relay, 7, payload); err != nil {
					t.Errorf("WritePacket: %v", err)
					return
				}
			}
		}(uint16(g))
	}

	// 读取所有包
	total := goroutines * packetsPerGoroutine
	received := 0
	errCh := make(chan error, 1)
	go func() {
		for range total {
			gotVIP, _, _, gotPayload, err := f2.ReadPacket()
			if err != nil {
				errCh <- err
				return
			}
			if gotVIP != dstVIP {
				errCh <- fmt.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
				return
			}
			if len(gotPayload) != 100 {
				errCh <- fmt.Errorf("payload 长度: got %d, want 100", len(gotPayload))
				return
			}
			received++
		}
		errCh <- nil
	}()

	wg.Wait()

	if err := <-errCh; err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if received != total {
		t.Errorf("接收数量: got %d, want %d", received, total)
	}
}

func TestFramerClose(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()

	f1 := NewStreamFramer(c1)
	if err := f1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 关闭后写入应失败
	err := f1.WritePacket(netip.MustParseAddr("100.64.0.1"), 0, 0, []byte("test"))
	if err == nil {
		t.Error("关闭后 WritePacket 应返回错误")
	}
}

// fakePacketConn 模拟 UDP PacketConn 用于测试 datagram framer。
type fakePacketConn struct {
	buf    bytes.Buffer
	mu     sync.Mutex
	closed bool
}

func (f *fakePacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, nil, net.ErrClosed
	}
	n, err = f.buf.Read(p)
	return n, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}, err
}

func (f *fakePacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, net.ErrClosed
	}
	return f.buf.Write(p)
}

func (f *fakePacketConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakePacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5678}
}

func (f *fakePacketConn) SetDeadline(_ time.Time) error      { return nil }
func (f *fakePacketConn) SetReadDeadline(_ time.Time) error  { return nil }
func (f *fakePacketConn) SetWriteDeadline(_ time.Time) error { return nil }

func TestFramerDatagramRoundTrip(t *testing.T) {
	pc := &fakePacketConn{}
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}

	f := NewDatagramFramer(pc, addr)

	dstVIP := netip.MustParseAddr("100.64.0.1")
	var dstRelay uint16 = 7
	var ttl uint8 = 3
	payload := []byte("datagram framer test")

	if err := f.WritePacket(dstVIP, dstRelay, ttl, payload); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	gotVIP, gotRelay, gotTTL, gotPayload, err := f.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if gotVIP != dstVIP {
		t.Errorf("VIP: got %v, want %v", gotVIP, dstVIP)
	}
	if gotRelay != dstRelay {
		t.Errorf("Relay: got %d, want %d", gotRelay, dstRelay)
	}
	if gotTTL != ttl {
		t.Errorf("TTL: got %d, want %d", gotTTL, ttl)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("Payload: got %q, want %q", gotPayload, payload)
	}
}
