//go:build linux

package tun

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// ConfigureAddress 给 TUN 接口添加 VIP 地址（Linux: ip addr add）。
// 幂等：地址已存在时（RTNETLINK answers: File exists）不报错。
func ConfigureAddress(tunName string, addrCIDR string) error {
	if _, _, err := net.ParseCIDR(addrCIDR); err != nil {
		return fmt.Errorf("tun: 解析地址 %q 失败: %w", addrCIDR, err)
	}
	cmd := exec.Command("ip", "addr", "add", addrCIDR, "dev", tunName)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// 幂等：地址已存在不算错误
		if strings.Contains(string(out), "File exists") {
			return nil
		}
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("tun: 'ip' 命令未找到，需安装 iproute2: %w", err)
		}
		return fmt.Errorf("tun: ip addr add %s dev %s: %s (%w)", addrCIDR, tunName, strings.TrimSpace(string(out)), err)
	}
	return nil
}
