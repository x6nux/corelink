package ingress

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc/peer"
)

// ─── sourceAddrFromContext 测试 ──────────────────────────────────────────────

func TestSourceAddrFromContext_NoPeer(t *testing.T) {
	// 空 context 无 peer 信息。
	ctx := context.Background()
	_, err := sourceAddrFromContext(ctx)
	if !errors.Is(err, errNoPeer) {
		t.Fatalf("期望 errNoPeer，实际 %v", err)
	}
}

func TestSourceAddrFromContext_NilAddr(t *testing.T) {
	// peer 存在但 Addr 为 nil。
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: nil})
	_, err := sourceAddrFromContext(ctx)
	if !errors.Is(err, errNoPeer) {
		t.Fatalf("期望 errNoPeer，实际 %v", err)
	}
}

func TestSourceAddrFromContext_OK(t *testing.T) {
	// 正常 TCP 地址。
	addr, _ := net.ResolveTCPAddr("tcp", "192.168.1.100:12345")
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
	sa, err := sourceAddrFromContext(ctx)
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if sa.Host != "192.168.1.100" {
		t.Errorf("Host = %q, 期望 192.168.1.100", sa.Host)
	}
	if sa.Port != 12345 {
		t.Errorf("Port = %d, 期望 12345", sa.Port)
	}
}

func TestSourceAddrFromContext_IPv6(t *testing.T) {
	addr, _ := net.ResolveTCPAddr("tcp", "[::1]:443")
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
	sa, err := sourceAddrFromContext(ctx)
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if sa.Host != "::1" {
		t.Errorf("Host = %q, 期望 ::1", sa.Host)
	}
	if sa.Port != 443 {
		t.Errorf("Port = %d, 期望 443", sa.Port)
	}
}

// badAddr 是一个不含端口的地址实现（用于测试 SplitHostPort 失败场景）。
type badAddr struct{ s string }

func (a badAddr) Network() string { return "tcp" }
func (a badAddr) String() string  { return a.s }

func TestSourceAddrFromContext_BadAddr(t *testing.T) {
	// 无法 SplitHostPort 的地址字符串。
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: badAddr{s: "no-port-here"}})
	_, err := sourceAddrFromContext(ctx)
	if err == nil {
		t.Fatal("无效地址格式应返回错误")
	}
}

func TestSourceAddrFromContext_PortZero(t *testing.T) {
	addr, _ := net.ResolveTCPAddr("tcp", "10.0.0.1:0")
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
	sa, err := sourceAddrFromContext(ctx)
	if err != nil {
		t.Fatalf("端口 0 应成功: %v", err)
	}
	if sa.Port != 0 {
		t.Errorf("Port = %d, 期望 0", sa.Port)
	}
}

func TestSourceAddrFromContext_HighPort(t *testing.T) {
	addr, _ := net.ResolveTCPAddr("tcp", "10.0.0.1:65535")
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
	sa, err := sourceAddrFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sa.Port != 65535 {
		t.Errorf("Port = %d, 期望 65535", sa.Port)
	}
}
