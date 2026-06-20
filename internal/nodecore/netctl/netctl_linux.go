//go:build linux

package netctl

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// LinuxRouteManager 使用 ip 命令管理路由。
type LinuxRouteManager struct {
	mu        sync.Mutex
	installed []netip.Prefix // SetAutoRoute 安装的前缀，用于 UnsetAutoRoute 清理
	tunName   string         // 当前 auto route 的 TUN 接口名
}

func NewRouteManager() RouteManager { return &LinuxRouteManager{} }

func (m *LinuxRouteManager) AddRoute(dst netip.Prefix, gateway netip.Addr, ifName string) error {
	args := []string{"route", "add", dst.String()}
	if gateway.IsValid() {
		args = append(args, "via", gateway.String())
	}
	if ifName != "" {
		args = append(args, "dev", ifName)
	}
	return runIP(args...)
}

func (m *LinuxRouteManager) RemoveRoute(dst netip.Prefix, gateway netip.Addr) error {
	args := []string{"route", "del", dst.String()}
	if gateway.IsValid() {
		args = append(args, "via", gateway.String())
	}
	return runIP(args...)
}

func (m *LinuxRouteManager) SetAutoRoute(tunName string, tunAddrs []netip.Prefix, gw4, gw6 netip.Addr) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.installed = nil
	m.tunName = tunName
	for _, addr := range tunAddrs {
		if err := m.AddRoute(addr, netip.Addr{}, tunName); err != nil {
			slog.Warn("netctl: 添加自动路由失败", "prefix", addr, "err", err)
			continue
		}
		m.installed = append(m.installed, addr)
	}
	slog.Info("netctl: Linux 自动路由已安装", "count", len(m.installed))
	return nil
}

func (m *LinuxRouteManager) UnsetAutoRoute() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, prefix := range m.installed {
		if err := m.RemoveRoute(prefix, netip.Addr{}); err != nil {
			slog.Debug("netctl: 删除自动路由失败（可能已不存在）", "prefix", prefix, "err", err)
		}
	}
	slog.Info("netctl: Linux 自动路由已清理", "count", len(m.installed))
	m.installed = nil
	return nil
}

func runIP(args ...string) error {
	for _, path := range []string{"/usr/sbin/ip", "/sbin/ip", "ip"} {
		if _, err := exec.LookPath(path); err == nil {
			out, runErr := exec.Command(path, args...).CombinedOutput()
			if runErr != nil {
				return fmt.Errorf("netctl: ip %v 失败: %s (%w)", args, strings.TrimSpace(string(out)), runErr)
			}
			return nil
		}
	}
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("netctl: ip %v 失败: %s (%w)", args, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// LinuxInterfaceDetector 从 /proc/net/route 和 /etc/resolv.conf 检测网络信息。
type LinuxInterfaceDetector struct{}

func NewInterfaceDetector() InterfaceDetector { return &LinuxInterfaceDetector{} }

func (d *LinuxInterfaceDetector) DefaultInterface() (string, netip.Addr, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", netip.Addr{}, fmt.Errorf("netctl: 读取 /proc/net/route 失败: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		dst := fields[1]
		if dst != "00000000" {
			continue
		}
		ifName := fields[0]
		gwHex := fields[2]
		gw, err := parseHexIPv4(gwHex)
		if err != nil {
			continue
		}
		return ifName, gw, nil
	}
	return "", netip.Addr{}, fmt.Errorf("netctl: 未找到默认路由")
}

func (d *LinuxInterfaceDetector) LocalSubnet() (netip.Prefix, error) {
	ifName, _, err := d.DefaultInterface()
	if err != nil {
		return netip.Prefix{}, err
	}
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return netip.Prefix{}, err
	}
	addrs, err := iface.Addrs()
	if err != nil || len(addrs) == 0 {
		return netip.Prefix{}, fmt.Errorf("netctl: 接口 %s 无地址", ifName)
	}
	// 遍历所有地址，找到第一个 IPv4 地址（跳过 IPv6）
	for _, addr := range addrs {
		prefix, err := netip.ParsePrefix(addr.String())
		if err != nil {
			continue
		}
		if prefix.Addr().Is4() {
			return prefix.Masked(), nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("netctl: 接口 %s 无 IPv4 地址", ifName)
}

func (d *LinuxInterfaceDetector) SystemDNSServers() ([]netip.Addr, error) {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := make(map[netip.Addr]bool)
	var servers []netip.Addr
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if addr, err := netip.ParseAddr(parts[1]); err == nil {
			if !seen[addr] {
				seen[addr] = true
				servers = append(servers, addr)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return servers, fmt.Errorf("netctl: 扫描 /etc/resolv.conf 失败: %w", err)
	}
	return servers, nil
}

// parseHexIPv4 将 /proc/net/route 中的小端序十六进制 IPv4 地址解析为 netip.Addr。
func parseHexIPv4(s string) (netip.Addr, error) {
	if len(s) != 8 {
		return netip.Addr{}, fmt.Errorf("无效的十六进制 IPv4: %s", s)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return netip.Addr{}, err
	}
	// /proc/net/route 使用小端序，需反转字节顺序
	return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]}), nil
}

// LinuxDNSManager 管理 Linux DNS 配置。
type LinuxDNSManager struct{}

func NewDNSManager() DNSManager { return &LinuxDNSManager{} }

func (m *LinuxDNSManager) FlushCache() error {
	return nil // Linux 通常无系统 DNS 缓存需要刷新
}

func (m *LinuxDNSManager) SetTUNDNS(ifName string, servers []netip.Addr) error {
	return nil // Linux 通过 TUN 栈内拦截处理 DNS，无需修改系统设置
}

func (m *LinuxDNSManager) RestoreDNS() error {
	return nil
}
