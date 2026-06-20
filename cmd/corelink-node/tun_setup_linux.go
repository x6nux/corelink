//go:build linux

package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// cleanupBeforeStart 启动前清理：杀残留的 corelink-node 进程 + 删遗留 TUN 接口。
func cleanupBeforeStart(tunNamePattern string) {
	// 计算 TUN 接口名前缀（corelink%d → corelink）
	prefix := tunNamePattern
	if idx := strings.Index(prefix, "%"); idx >= 0 {
		prefix = prefix[:idx]
	}
	if prefix == "" {
		prefix = "corelink"
	}

	// 杀残留进程（排除自己）
	myPID := fmt.Sprintf("%d", os.Getpid())
	out, err := exec.Command("pgrep", "-f", "corelink-node").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			pid := strings.TrimSpace(line)
			if pid == "" || pid == myPID {
				continue
			}
			slog.Info("清理残留进程", "pid", pid)
			_ = exec.Command("kill", "-9", pid).Run()
		}
	}

	// 删遗留 TUN 接口（corelink0, corelink1, ...）
	out2, err := exec.Command("ip", "-br", "link", "show", "type", "tun").Output()
	if err == nil {
		for _, line := range strings.Split(string(out2), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			ifName := fields[0]
			if strings.HasPrefix(ifName, prefix) {
				slog.Info("清理遗留 TUN 接口", "iface", ifName)
				_ = exec.Command("ip", "link", "delete", ifName).Run()
			}
		}
	}
}

// initSysctl 统一设置 Node 运行所需的全部 sysctl 内核参数。
// 在节点启动时调用一次，TUN 创建之前。
func initSysctl(tunName string) {
	params := map[string]string{
		// IP 转发（overlay 中继必需）
		"net.ipv4.ip_forward": "1",
		// 禁用 ICMP Redirect（overlay 中继转发时内核误发 Redirect 会破坏路由）
		"net.ipv4.conf.all.send_redirects":       "0",
		"net.ipv4.conf.all.accept_redirects":     "0",
		"net.ipv4.conf.default.send_redirects":   "0",
		"net.ipv4.conf.default.accept_redirects": "0",
		// 禁用反向路径过滤（overlay 的 VIP 源地址不在物理接口路由表中）
		"net.ipv4.conf.all.rp_filter":     "0",
		"net.ipv4.conf.default.rp_filter": "0",
	}
	// TUN 接口专属参数（tunName 非空时设置）
	if tunName != "" {
		params["net.ipv4.conf."+tunName+".send_redirects"] = "0"
		params["net.ipv4.conf."+tunName+".accept_redirects"] = "0"
		params["net.ipv4.conf."+tunName+".rp_filter"] = "0"
	}

	for key, val := range params {
		if err := exec.Command("sysctl", "-w", fmt.Sprintf("%s=%s", key, val)).Run(); err != nil {
			slog.Warn("sysctl 设置失败", "key", key, "val", val, "err", err)
		}
	}
	slog.Info("sysctl 内核参数已初始化", "params", len(params))
}

// configureLinkUpAndRoute 在 Linux 上配置 TUN 链路和路由。
func configureLinkUpAndRoute(tunName, vipCIDR string) {
	if err := exec.Command("ip", "link", "set", tunName, "up").Run(); err != nil {
		slog.Warn("启动 TUN 链路失败", "tun", tunName, "err", err)
	}
	if err := exec.Command("ip", "route", "replace", vipCIDR, "dev", tunName).Run(); err != nil {
		slog.Warn("安装 TUN 默认路由失败", "err", err)
	}
	slog.Info("TUN 链路配置完成", "tun", tunName, "cidr", vipCIDR)
}
