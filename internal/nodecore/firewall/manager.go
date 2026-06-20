//go:build linux

package firewall

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// Runner 执行系统命令的接口（便于测试注入 fake）。
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Manager 管理 CoreLink 防火墙规则的生命周期。
var _ FirewallManager = (*Manager)(nil)

type Manager struct {
	runner      Runner
	chainsReady bool
	mu          sync.Mutex
}

// NewManager 创建 firewall manager。
func NewManager(runner Runner) *Manager {
	return &Manager{runner: runner}
}

// EnsureChains 确保 CoreLink 自有链存在并挂入主链。只在首次调用时执行。
// 外部调用入口，自行加锁。
func (m *Manager) EnsureChains(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureChainsLocked(ctx)
}

// ensureChainsLocked 是 EnsureChains 的内部实现，调用方须持有 mu。
// ApplyDNS/ApplyEgress 在持锁状态下调用此方法以避免锁重入死锁。
func (m *Manager) ensureChainsLocked(ctx context.Context) error {
	if m.chainsReady {
		return nil
	}
	chains := []struct {
		table, chain, parent string
	}{
		{"nat", DNSChain, "PREROUTING"},
		{"nat", DNSOutChain, "OUTPUT"}, // 本机 DNS 查询走 OUTPUT
		{"nat", NATChain, "PREROUTING"},
		{"filter", FWDChain, "FORWARD"},
	}
	for _, c := range chains {
		// -N 创建链（已存在则忽略错误）
		_ = m.run(ctx, Command{Table: c.table, Args: []string{"-N", c.chain}})
		// 检查主链是否已有跳转，没有则添加
		err := m.run(ctx, Command{Table: c.table, Args: []string{"-C", c.parent, "-j", c.chain}})
		if err != nil {
			if insertErr := m.run(ctx, Command{Table: c.table, Args: []string{"-I", c.parent, "-j", c.chain}}); insertErr != nil {
				return fmt.Errorf("firewall: 挂入跳转 %s→%s 失败: %w", c.parent, c.chain, insertErr)
			}
		}
	}
	// MASQUERADE 链挂入 POSTROUTING
	_ = m.run(ctx, Command{Table: "nat", Args: []string{"-N", NATChain + "-POST"}})
	err := m.run(ctx, Command{Table: "nat", Args: []string{"-C", "POSTROUTING", "-j", NATChain + "-POST"}})
	if err != nil {
		if insertErr := m.run(ctx, Command{Table: "nat", Args: []string{"-I", "POSTROUTING", "-j", NATChain + "-POST"}}); insertErr != nil {
			return fmt.Errorf("firewall: 挂入跳转 POSTROUTING→%s 失败: %w", NATChain+"-POST", insertErr)
		}
	}
	m.chainsReady = true
	slog.Info("corelink-node: iptables CoreLink 链已创建")
	return nil
}

// ApplyDNS 应用 DNS 重定向规则：先清理旧规则，再应用新规则。
// 整个 flush+append 序列在 mu 保护下原子执行，防止并发配置下发产生规则撕裂。
func (m *Manager) ApplyDNS(ctx context.Context, cfg *genv1.DNSConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureChainsLocked(ctx); err != nil {
		return err
	}
	_ = m.run(ctx, Command{Table: "nat", Args: []string{"-F", DNSChain}})
	_ = m.run(ctx, Command{Table: "nat", Args: []string{"-F", DNSOutChain}})

	if cfg == nil || !cfg.Enabled {
		return nil
	}
	// 缓存生成结果，避免重复调用 GenerateDNSRedirect
	dnsRules := GenerateDNSRedirect(cfg)
	var firstErr error
	for _, cmd := range dnsRules {
		if err := m.run(ctx, cmd); err != nil {
			slog.Warn("firewall: DNS 规则应用失败", "cmd", cmd, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	slog.Info("corelink-node: DNS iptables 规则已应用", "rules", len(dnsRules))
	return firstErr
}

// ApplyEgress 应用出口转发规则：先清理旧规则，再应用新规则。
// 整个 flush+append 序列在 mu 保护下原子执行，防止并发配置下发产生规则撕裂。
func (m *Manager) ApplyEgress(ctx context.Context, rules []*genv1.EgressRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureChainsLocked(ctx); err != nil {
		return err
	}
	_ = m.run(ctx, Command{Table: "nat", Args: []string{"-F", NATChain}})
	_ = m.run(ctx, Command{Table: "nat", Args: []string{"-F", NATChain + "-POST"}})
	_ = m.run(ctx, Command{Table: "filter", Args: []string{"-F", FWDChain}})

	var firstErr error
	for _, cmd := range GenerateEgressRules(rules) {
		if err := m.run(ctx, cmd); err != nil {
			slog.Warn("firewall: 出口规则应用失败", "cmd", cmd, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	slog.Info("corelink-node: egress iptables 规则已应用", "rules", len(rules))
	return firstErr
}

// Cleanup 清理所有 CoreLink 规则并从主链摘除跳转。
// 顺序：flush 链 → 摘除主链跳转 → 删除自有链（-X 需要链为空且无引用）。
func (m *Manager) Cleanup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 1. flush 所有自有链
	for _, cmd := range GenerateCleanup() {
		_ = m.run(ctx, cmd)
	}
	// 2. 从主链摘除跳转（防止 corelink-node 停止后 DNS 黑洞）
	jumps := []struct{ table, parent, chain string }{
		{"nat", "PREROUTING", DNSChain},
		{"nat", "OUTPUT", DNSOutChain},
		{"nat", "PREROUTING", NATChain},
		{"nat", "POSTROUTING", NATChain + "-POST"},
		{"filter", "FORWARD", FWDChain},
	}
	for _, j := range jumps {
		_ = m.run(ctx, Command{Table: j.table, Args: []string{"-D", j.parent, "-j", j.chain}})
	}
	// 3. 删除自有链（-X 要求链已为空且无引用，前两步已保证）
	for _, chain := range []string{DNSChain, DNSOutChain, NATChain, NATChain + "-POST"} {
		_ = m.run(ctx, Command{Table: "nat", Args: []string{"-X", chain}})
	}
	_ = m.run(ctx, Command{Table: "filter", Args: []string{"-X", FWDChain}})
	m.chainsReady = false
	slog.Info("corelink-node: iptables CoreLink 规则已清理")
	return nil
}

func (m *Manager) run(ctx context.Context, cmd Command) error {
	args := append([]string{"-t", cmd.Table}, cmd.Args...)
	// 优先尝试绝对路径（systemd 环境下 PATH 可能不含 /usr/sbin）
	var lastErr error
	for _, bin := range []string{"/usr/sbin/iptables", "/sbin/iptables", "iptables"} {
		if err := m.runner.Run(ctx, bin, args...); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}
