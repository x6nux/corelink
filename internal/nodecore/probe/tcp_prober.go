// tcp_prober.go 实现基于 TCP 连接的轻量探测器。
//
// TCPProber 通过 TCP 三次握手测量 RTT：向目标地址发起 TCP 连接并测量握手耗时，
// 连接成功后立即关闭。相比占位探测器（返回固定值），TCPProber 能反映真实网络延迟，
// 同时避免 UDP 探测在受限 NAT 环境下的可达性问题。
//
// 丢包率固定返回 0（TCP 探测无法直接测量丢包），ok=false 表示连接超时或拒绝。
package probe

import (
	"fmt"
	"net"
	"time"
)

// TCPProbeConfig 配置 TCP 探测器参数。
type TCPProbeConfig struct {
	// Timeout 单次 TCP 连接超时，默认 3 秒。
	Timeout time.Duration
}

// TCPProber 通过 TCP 三次握手测量延迟的轻量探测器。
//
// 线程安全：Probe 方法无状态，可并发调用。
type TCPProber struct {
	timeout time.Duration
}

// NewTCPProber 创建 TCP 探测器。
func NewTCPProber(cfg TCPProbeConfig) *TCPProber {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &TCPProber{timeout: timeout}
}

// Probe 实现 Prober 签名：对 ingressID（格式为 host:port）发起 TCP 连接并测量 RTT。
//
// ingressID 应为可拨号的 "host:port" 地址。若格式不合法或连接失败，返回 ok=false。
// 丢包率（lossPermille）固定返回 0——TCP 探测无法直接测量丢包。
func (p *TCPProber) Probe(ingressID string) (rttMs uint32, lossPermille uint32, ok bool) {
	// 校验 ingressID 格式：必须是 host:port
	host, port, err := net.SplitHostPort(ingressID)
	if err != nil || host == "" || port == "" {
		return 0, 0, false
	}
	addr := net.JoinHostPort(host, port)

	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, p.timeout)
	if err != nil {
		return 0, 0, false
	}
	rtt := time.Since(start)
	conn.Close() //nolint:errcheck

	ms := uint32(rtt.Milliseconds())
	if ms == 0 {
		ms = 1 // 最低 1ms，避免 0 被误判为无效
	}
	return ms, 0, true
}

// ProbeAddr 对任意 host:port 地址执行 TCP 探测（不依赖 Prober 签名）。
//
// 用于 multirelay 等需要探测 RelayEndpoint 的场景——上层从 RelayEndpoint 提取地址后
// 调用此方法，避免重复构造探测器。
func (p *TCPProber) ProbeAddr(addr string) (rttMs int, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return 0, false
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%s", host, port), p.timeout)
	if err != nil {
		return 0, false
	}
	rtt := time.Since(start)
	conn.Close() //nolint:errcheck

	ms := int(rtt.Milliseconds())
	if ms == 0 {
		ms = 1
	}
	return ms, true
}
