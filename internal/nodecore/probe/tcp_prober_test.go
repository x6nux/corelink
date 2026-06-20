package probe

import (
	"net"
	"testing"
	"time"
)

// TestTCPProber_ProbeLocalListener 验证 TCPProber 对本地监听端口的探测返回合理 RTT。
func TestTCPProber_ProbeLocalListener(t *testing.T) {
	// 起一个本地 TCP 监听
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("无法监听本地端口: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	p := NewTCPProber(TCPProbeConfig{Timeout: 2 * time.Second})
	addr := ln.Addr().String()

	rtt, loss, ok := p.Probe(addr)
	if !ok {
		t.Fatalf("本地探测应成功，got ok=false")
	}
	if rtt == 0 {
		t.Fatalf("RTT 不应为 0（最低 1ms）")
	}
	if loss != 0 {
		t.Fatalf("TCP 探测丢包率应为 0，got %d", loss)
	}
	// 本地延迟应小于 500ms
	if rtt > 500 {
		t.Fatalf("本地探测 RTT 异常（>500ms）: %d", rtt)
	}
}

// TestTCPProber_ProbeUnreachable 验证对不可达地址返回 ok=false。
func TestTCPProber_ProbeUnreachable(t *testing.T) {
	p := NewTCPProber(TCPProbeConfig{Timeout: 200 * time.Millisecond})

	// 对一个不可达地址探测（端口 1 通常不监听且拒绝连接）
	_, _, ok := p.Probe("127.0.0.1:1")
	if ok {
		t.Fatalf("不可达地址应返回 ok=false")
	}
}

// TestTCPProber_ProbeInvalidAddr 验证无效地址返回 ok=false。
func TestTCPProber_ProbeInvalidAddr(t *testing.T) {
	p := NewTCPProber(TCPProbeConfig{})

	cases := []string{
		"",
		"invalid",
		"host-no-port",
		":1234", // 空 host
		"host:", // 空 port
	}
	for _, addr := range cases {
		_, _, ok := p.Probe(addr)
		if ok {
			t.Errorf("无效地址 %q 应返回 ok=false", addr)
		}
	}
}

// TestTCPProber_DefaultTimeout 验证默认超时配置。
func TestTCPProber_DefaultTimeout(t *testing.T) {
	p := NewTCPProber(TCPProbeConfig{})
	if p.timeout != 3*time.Second {
		t.Fatalf("默认 timeout 应为 3s，got %v", p.timeout)
	}
}

// TestTCPProber_ProbeAddr 验证 ProbeAddr 方法（multirelay 适配）。
func TestTCPProber_ProbeAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("无法监听: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	p := NewTCPProber(TCPProbeConfig{Timeout: 2 * time.Second})
	rtt, ok := p.ProbeAddr(ln.Addr().String())
	if !ok {
		t.Fatalf("ProbeAddr 本地应成功")
	}
	if rtt == 0 {
		t.Fatalf("RTT 不应为 0")
	}
}

// TestTCPProber_ImplementsProber 验证 TCPProber.Probe 匹配 Prober 签名。
func TestTCPProber_ImplementsProber(t *testing.T) {
	p := NewTCPProber(TCPProbeConfig{})
	// 编译期确认签名兼容
	var _ Prober = p.Probe
	_ = p
}
