//go:build linux

package splittunnel

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	// PolicyTableID 策略路由表编号
	PolicyTableID = 2022
	// PolicyRulePrio 主路由规则优先级
	PolicyRulePrio = 9000
	// FwMarkBypass corelink-node 出站 fwmark（命中此标记的包走 main 表绕过 TUN）
	FwMarkBypass = 0x2022
	// FwMarkRulePrio fwmark 规则优先级（高于主规则）
	FwMarkRulePrio = 8999
	// SubnetRulePrio 本地子网排除规则优先级
	SubnetRulePrio = 8998
)

var ipBin = findIPCmd()

func findIPCmd() string {
	for _, p := range []string{"/sbin/ip", "/usr/sbin/ip", "/bin/ip"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "ip"
}

// InstallPolicyRoutes 安装策略路由：独立路由表 + ip rule 链。
// 参考 sing-box/sing-tun 的 Linux 实现，不污染主路由表。
func InstallPolicyRoutes(tunName, physIfce string) error {
	tableStr := strconv.Itoa(PolicyTableID)
	fwmarkStr := fmt.Sprintf("0x%x", FwMarkBypass)

	// 1. 在独立路由表中添加默认路由指向 TUN（关键命令，失败则返回错误）
	if err := run(ipBin, "route", "add", "default", "dev", tunName, "table", tableStr); err != nil {
		return fmt.Errorf("splittunnel: 添加 TUN 默认路由失败: %w", err)
	}

	// 2. ip rule: fwmark → main（corelink-node 出站绕过 TUN，关键命令）
	if err := run(ipBin, "rule", "add", "fwmark", fwmarkStr, "lookup", "main",
		"priority", strconv.Itoa(FwMarkRulePrio)); err != nil {
		return fmt.Errorf("splittunnel: 添加 fwmark 规则失败: %w", err)
	}

	// 3. ip rule: loopback + 本地子网 → main（保护 SSH 等已有连接）
	_ = run(ipBin, "rule", "add", "to", "127.0.0.0/8", "lookup", "main",
		"priority", strconv.Itoa(SubnetRulePrio-1))
	if subnet := DetectLocalSubnet(physIfce); subnet != "" {
		_ = run(ipBin, "rule", "add", "to", subnet, "lookup", "main",
			"priority", strconv.Itoa(SubnetRulePrio))
	}

	// 6. ip rule: 其余流量 → table 2022（进 TUN 分流，关键命令）
	if err := run(ipBin, "rule", "add", "lookup", tableStr,
		"priority", strconv.Itoa(PolicyRulePrio)); err != nil {
		return fmt.Errorf("splittunnel: 添加主路由规则失败: %w", err)
	}

	// 7. 覆盖 DNS 配置——让系统 DNS 查询走 TUN 被拦截（防 DNS 污染）
	OverrideResolv()

	slog.Info("splittunnel: 策略路由已安装",
		"table", PolicyTableID, "fwmark", fwmarkStr, "tun", tunName)
	return nil
}

// RemovePolicyRoutes 清理策略路由。清理阶段忽略错误。
func RemovePolicyRoutes() {
	tableStr := strconv.Itoa(PolicyTableID)
	fwmarkStr := fmt.Sprintf("0x%x", FwMarkBypass)
	// 删除 ip rule（按优先级），清理阶段忽略错误
	_ = run(ipBin, "rule", "del", "priority", strconv.Itoa(SubnetRulePrio-1))
	_ = run(ipBin, "rule", "del", "priority", strconv.Itoa(SubnetRulePrio))
	_ = run(ipBin, "rule", "del", "priority", strconv.Itoa(FwMarkRulePrio))
	_ = run(ipBin, "rule", "del", "priority", strconv.Itoa(PolicyRulePrio))

	// 清空路由表
	_ = run(ipBin, "route", "flush", "table", tableStr)

	// 恢复 DNS 配置
	RestoreResolv()

	slog.Info("splittunnel: 策略路由已清理", "table", PolicyTableID, "fwmark", fwmarkStr)
}

// run 执行命令并返回错误。
// "File exists" 不视为错误（幂等添加），其他失败原样返回。
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(out))
		// "File exists" 表示规则/路由已存在，幂等操作不视为错误
		if strings.Contains(outStr, "File exists") {
			return nil
		}
		slog.Debug("splittunnel: 命令执行", "cmd", name+" "+strings.Join(args, " "),
			"err", err, "out", outStr)
		return fmt.Errorf("%s: %s", strings.Join(append([]string{name}, args...), " "), outStr)
	}
	return nil
}

// InstallMasquerade 安装 MASQUERADE 规则（VIP 源 NAT）。
func InstallMasquerade(physIfce string) error {
	// 检查规则是否已存在
	err := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING",
		"-o", physIfce, "-s", "100.64.0.0/10", "-j", "MASQUERADE").Run()
	if err != nil {
		out, err := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING",
			"-o", physIfce, "-s", "100.64.0.0/10", "-j", "MASQUERADE").CombinedOutput()
		if err != nil {
			return fmt.Errorf("splittunnel: MASQUERADE: %s: %w", string(out), err)
		}
	}
	slog.Info("splittunnel: MASQUERADE 已安装", "ifce", physIfce)
	return nil
}

// RemoveMasquerade 删除 MASQUERADE 规则。
func RemoveMasquerade(physIfce string) {
	_ = exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING",
		"-o", physIfce, "-s", "100.64.0.0/10", "-j", "MASQUERADE").Run()
	slog.Info("splittunnel: MASQUERADE 已清理", "ifce", physIfce)
}
