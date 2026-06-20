package relayclient

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// loopbackRelay 是回环的伪 relay 隧道端点：accept 连接并保持，
// 可被 close 以模拟断开。
type loopbackRelay struct {
	ln    tunnel.Listener
	conns chan net.Conn
}

func newLoopbackRelay(t *testing.T) *loopbackRelay {
	t.Helper()
	ln, err := tunnel.Listen(&tunnel.Config{Protocol: tunnel.TCP}, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	r := &loopbackRelay{ln: ln, conns: make(chan net.Conn, 8)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.conns <- c
		}
	}()
	return r
}

func (r *loopbackRelay) endpoint() *genv1.RelayEndpoint {
	host, portStr, _ := net.SplitHostPort(r.ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return &genv1.RelayEndpoint{
		RelayId: "relay-loop",
		Tunnel:  &genv1.Endpoint{Host: host, Port: uint32(port)},
	}
}

func (r *loopbackRelay) Close() { r.ln.Close() }

// TestDialConnectorConnectAndDisconnect 验证真实连接器：
//   - Connect 成功拨到回环 relay；
//   - relay 关闭连接后 Wait 返回（感知断开）。
func TestDialConnectorConnectAndDisconnect(t *testing.T) {
	relay := newLoopbackRelay(t)
	defer relay.Close()

	conn, err := NewDialConnector()
	if err != nil {
		t.Fatalf("NewDialConnector: %v", err)
	}

	ctx := context.Background()
	if err := conn.Connect(ctx, relay.endpoint()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// 取到 relay 侧 accept 的连接并关闭，模拟 relay 断开。
	var serverConn net.Conn
	select {
	case serverConn = <-relay.conns:
	case <-time.After(2 * time.Second):
		t.Fatal("relay 未 accept 到连接")
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- conn.Wait(ctx) }()

	serverConn.Close()

	select {
	case err := <-waitDone:
		if err == nil {
			t.Fatal("Wait 应在断开时返回非 nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait 未感知断开")
	}
}

// TestDialConnectorWithClientReconnect 端到端：用真实连接器 + Client，
// relay 断开后被动重连成功（relay 再次 accept 到新连接）。
func TestDialConnectorWithClientReconnect(t *testing.T) {
	relay := newLoopbackRelay(t)
	defer relay.Close()

	conn, err := NewDialConnector()
	if err != nil {
		t.Fatalf("NewDialConnector: %v", err)
	}
	c := New(Config{
		Connector: conn,
		Backoff:   BackoffConfig{Base: 10 * time.Millisecond, Max: 50 * time.Millisecond, Factor: 2},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, relay.endpoint())

	// 第一次接入。
	var first net.Conn
	select {
	case first = <-relay.conns:
	case <-time.After(2 * time.Second):
		t.Fatal("首次未连接")
	}
	waitState(t, c, StateConnected)

	// relay 断开 → 应被动重连，relay 再次 accept。
	first.Close()
	select {
	case <-relay.conns:
	case <-time.After(2 * time.Second):
		t.Fatal("断开后未重连")
	}
}
