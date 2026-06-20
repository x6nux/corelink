//go:build darwin

package splittunnel

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

// macOS 不支持 Linux 策略路由（ip rule / 独立路由表），
// 但保持 API 兼容。路由管理由 netctl.DarwinRouteManager 负责。

const (
	// PolicyTableID macOS 不使用策略路由表。
	PolicyTableID = 0
	// PolicyRulePrio macOS 不使用策略路由规则。
	PolicyRulePrio = 0
	// FwMarkBypass macOS 不支持 fwmark。
	FwMarkBypass = 0
	// FwMarkRulePrio macOS 不使用 fwmark 规则。
	FwMarkRulePrio = 0
	// SubnetRulePrio macOS 不使用子网排除规则。
	SubnetRulePrio = 0
)

// InstallPolicyRoutes 在 macOS 上通过 route 命令添加默认路由到 TUN 设备。
// macOS 不支持 Linux 的 ip rule 策略路由表，使用直接路由替代。
func InstallPolicyRoutes(tunName, physIfce string) error {
	// 在 macOS 上，通过添加更具体的路由（0.0.0.0/1 + 128.0.0.0/1）覆盖默认路由，
	// 而不是使用策略路由表。这是 VPN 客户端在 macOS 上的标准做法。
	cmds := [][]string{
		{"route", "-n", "add", "-net", "0.0.0.0/1", "-interface", tunName},
		{"route", "-n", "add", "-net", "128.0.0.0/1", "-interface", tunName},
	}
	for _, args := range cmds {
		runDarwin(args[0], args[1:]...)
	}
	slog.Info("splittunnel: macOS 策略路由已安装（覆盖默认路由）", "tun", tunName)
	return nil
}

// RemovePolicyRoutes 清理 macOS 上的策略路由。
func RemovePolicyRoutes() {
	cmds := [][]string{
		{"route", "-n", "delete", "-net", "0.0.0.0/1"},
		{"route", "-n", "delete", "-net", "128.0.0.0/1"},
	}
	for _, args := range cmds {
		runDarwin(args[0], args[1:]...)
	}
	slog.Info("splittunnel: macOS 策略路由已清理")
}

// InstallMasquerade 在 macOS 上使用 pfctl 添加 NAT 规则。
// macOS 使用 pf (Packet Filter) 防火墙作为 iptables 的替代。
func InstallMasquerade(physIfce string) error {
	rule := fmt.Sprintf("nat on %s from 100.64.0.0/10 to any -> (%s)\n", physIfce, physIfce)
	// 使用唯一临时文件名，避免 /tmp 符号链接攻击
	tmpFile, err := os.CreateTemp("", "corelink-nat-*.conf")
	if err != nil {
		return fmt.Errorf("splittunnel: 创建临时 pf 规则文件失败: %w", err)
	}
	confPath := tmpFile.Name()
	defer os.Remove(confPath) // pfctl 加载后即可删除
	if _, err := tmpFile.WriteString(rule); err != nil {
		tmpFile.Close()
		return fmt.Errorf("splittunnel: 写入 pf 规则失败: %w", err)
	}
	tmpFile.Close()

	// 加载 NAT 规则到 corelink-nat anchor
	out, err := exec.Command("pfctl", "-a", "corelink-nat", "-f", confPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("splittunnel: 加载 pf NAT 规则失败: %s: %w", string(out), err)
	}

	// 确保主 pf 规则集引用了 corelink-nat anchor，否则 anchor 内的规则不生效。
	// 先检查是否已存在引用，避免重复添加。
	existing, _ := exec.Command("pfctl", "-s", "nat").CombinedOutput()
	if !contains(string(existing), "corelink-nat") {
		// 仅追加 anchor 引用到主规则集（不覆盖现有规则）。
		// 使用 echo + pfctl -f /dev/stdin 的方式只追加 nat-anchor 到当前规则。
		anchorConf := string(existing) + "nat-anchor \"corelink-nat\"\n"
		anchorFile, wErr := os.CreateTemp("", "corelink-pf-anchor-*.conf")
		if wErr == nil {
			anchorPath := anchorFile.Name()
			anchorFile.WriteString(anchorConf)
			anchorFile.Close()
			// 仅加载 NAT 规则（-N 标志），不覆盖 filter/scrub 等其他规则类型。
			if mOut, mErr := exec.Command("pfctl", "-N", "-f", anchorPath).CombinedOutput(); mErr != nil {
				slog.Warn("splittunnel: 加载 anchor 引用失败", "err", mErr, "out", string(mOut))
			}
			os.Remove(anchorPath)
		}
	}

	// 确保 pf 已启用
	_ = exec.Command("pfctl", "-e").Run()

	slog.Info("splittunnel: macOS MASQUERADE 已安装（pfctl anchor）", "ifce", physIfce)
	return nil
}

// RemoveMasquerade 删除 macOS 上的 pf NAT 规则。
func RemoveMasquerade(physIfce string) {
	_ = exec.Command("pfctl", "-a", "corelink-nat", "-F", "all").Run()
	// 临时配置文件已在 InstallMasquerade 中 defer 删除，无需再清理
	slog.Info("splittunnel: macOS MASQUERADE 已清理", "ifce", physIfce)
}

// runDarwin 执行命令，忽略错误（清理阶段允许失败）。
func runDarwin(name string, args ...string) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		outStr := string(out)
		// 忽略 "路由已存在" 或 "路由不存在" 的错误
		if outStr != "" && !contains(outStr, "exists", "not in table") {
			slog.Debug("splittunnel: macOS 命令执行", "cmd", name, "args", args,
				"err", err, "out", outStr)
		}
	}
}

// contains 检查字符串是否包含任一子串。
func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
