//go:build windows

package splittunnel

import (
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
)

// Windows 策略路由常量（Windows 不支持 Linux 策略路由表）
const (
	PolicyTableID  = 0
	PolicyRulePrio = 0
	FwMarkBypass   = 0
	FwMarkRulePrio = 0
	SubnetRulePrio = 0
)

// tunIfIndex 通过接口名查询 Windows 接口索引（route add 的 if 参数需要整数索引）。
func tunIfIndex(tunName string) (string, error) {
	iface, err := net.InterfaceByName(tunName)
	if err != nil {
		return "", fmt.Errorf("splittunnel: 查询接口索引失败 %q: %w", tunName, err)
	}
	return strconv.Itoa(iface.Index), nil
}

// InstallPolicyRoutes 在 Windows 上通过 route 命令添加路由。
// Windows route add 的 if 参数需要接口索引（整数），而非接口名称。
func InstallPolicyRoutes(tunName, physIfce string) error {
	ifIdx, err := tunIfIndex(tunName)
	if err != nil {
		slog.Warn("splittunnel: 无法获取 TUN 接口索引，跳过路由安装", "tun", tunName, "err", err)
		return err
	}
	// 添加覆盖默认路由（0.0.0.0/1 + 128.0.0.0/1）
	cmds := [][]string{
		{"route", "add", "0.0.0.0", "mask", "128.0.0.0", "0.0.0.0", "if", ifIdx},
		{"route", "add", "128.0.0.0", "mask", "128.0.0.0", "0.0.0.0", "if", ifIdx},
	}
	for _, args := range cmds {
		runWindows(args[0], args[1:]...)
	}
	slog.Info("splittunnel: Windows 路由已安装", "tun", tunName, "ifIndex", ifIdx)
	return nil
}

// RemovePolicyRoutes 清理 Windows 上的路由。
func RemovePolicyRoutes() {
	cmds := [][]string{
		{"route", "delete", "0.0.0.0", "mask", "128.0.0.0"},
		{"route", "delete", "128.0.0.0", "mask", "128.0.0.0"},
	}
	for _, args := range cmds {
		runWindows(args[0], args[1:]...)
	}
	slog.Info("splittunnel: Windows 路由已清理")
}

// InstallMasquerade 在 Windows 上启用 Internet Connection Sharing（ICS）。
func InstallMasquerade(physIfce string) error {
	out, err := exec.Command("netsh", "routing", "ip", "nat", "install").CombinedOutput()
	if err != nil {
		slog.Warn("splittunnel: Windows NAT 安装失败（可能需要管理员权限）",
			"err", err, "out", string(out))
		return err
	}
	slog.Info("splittunnel: Windows NAT 已安装", "ifce", physIfce)
	return nil
}

// RemoveMasquerade 在 Windows 上卸载 NAT。
func RemoveMasquerade(physIfce string) {
	_ = exec.Command("netsh", "routing", "ip", "nat", "uninstall").Run()
	slog.Info("splittunnel: Windows NAT 已清理", "ifce", physIfce)
}

// runWindows 执行命令，忽略错误（清理阶段允许失败）。
func runWindows(name string, args ...string) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		slog.Debug("splittunnel: Windows 命令执行",
			"cmd", name, "args", args, "err", err, "out", string(out))
	}
}
