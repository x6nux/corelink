//go:build windows

package netctl

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/x6nux/corelink/internal/platform/windnsapi"
	"golang.org/x/sys/windows"
)

// ─────────────────────── 路由管理 ───────────────────────

// WindowsRouteManager 使用 Windows route 命令管理路由。
type WindowsRouteManager struct {
	mu        sync.Mutex
	installed []installedRoute // SetAutoRoute 安装的路由，用于 UnsetAutoRoute 清理
}

// installedRoute 记录一条已安装的路由。
type installedRoute struct {
	dst netip.Prefix
	gw  netip.Addr
}

// NewRouteManager 返回 Windows 平台的路由管理器。
func NewRouteManager() RouteManager { return &WindowsRouteManager{} }

func (m *WindowsRouteManager) AddRoute(dst netip.Prefix, gateway netip.Addr, ifName string) error {
	// IPv6 路由使用 netsh interface ipv6 add route
	if dst.Addr().Is6() {
		return m.addRouteV6(dst, gateway, ifName)
	}
	mask := prefixToMask(dst)
	args := []string{"add", dst.Masked().Addr().String(), "mask", mask}
	if gateway.IsValid() {
		args = append(args, gateway.String())
	}
	cmd := routeCmd(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netctl: route add 失败: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// addRouteV6 使用 netsh 添加 IPv6 路由。
func (m *WindowsRouteManager) addRouteV6(dst netip.Prefix, gateway netip.Addr, ifName string) error {
	system32, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("netctl: 获取系统目录失败: %w", err)
	}
	args := []string{"interface", "ipv6", "add", "route", dst.String()}
	if ifName != "" {
		args = append(args, "interface="+ifName)
	}
	if gateway.IsValid() {
		args = append(args, "nexthop="+gateway.String())
	}
	cmd := exec.Command(filepath.Join(system32, "netsh.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netctl: netsh ipv6 route add 失败: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *WindowsRouteManager) RemoveRoute(dst netip.Prefix, gateway netip.Addr) error {
	// IPv6 路由使用 netsh interface ipv6 delete route
	if dst.Addr().Is6() {
		return m.removeRouteV6(dst, gateway)
	}
	// Windows route delete 需要 mask 参数才能精确匹配子网，
	// 否则 /32 以外的前缀可能删错路由或无法匹配。
	args := []string{"delete", dst.Masked().Addr().String(), "mask", prefixToMask(dst)}
	if gateway.IsValid() {
		args = append(args, gateway.String())
	}
	cmd := routeCmd(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netctl: route delete 失败: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// removeRouteV6 使用 netsh 删除 IPv6 路由。
func (m *WindowsRouteManager) removeRouteV6(dst netip.Prefix, gateway netip.Addr) error {
	system32, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("netctl: 获取系统目录失败: %w", err)
	}
	args := []string{"interface", "ipv6", "delete", "route", dst.String()}
	if gateway.IsValid() {
		args = append(args, "nexthop="+gateway.String())
	}
	cmd := exec.Command(filepath.Join(system32, "netsh.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netctl: netsh ipv6 route delete 失败: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *WindowsRouteManager) SetAutoRoute(tunName string, addrs []netip.Prefix, gw4, gw6 netip.Addr) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.installed = nil
	for _, addr := range addrs {
		gw := gw4
		if addr.Addr().Is6() {
			gw = gw6
		}
		if err := m.AddRoute(addr, gw, tunName); err != nil {
			slog.Warn("netctl: 添加自动路由失败", "prefix", addr, "err", err)
			continue
		}
		m.installed = append(m.installed, installedRoute{dst: addr, gw: gw})
	}
	slog.Info("netctl: Windows 自动路由已安装", "count", len(m.installed))
	return nil
}

func (m *WindowsRouteManager) UnsetAutoRoute() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.installed {
		if err := m.RemoveRoute(r.dst, r.gw); err != nil {
			slog.Debug("netctl: 删除自动路由失败（可能已不存在）", "prefix", r.dst, "err", err)
		}
	}
	slog.Info("netctl: Windows 自动路由已清理", "count", len(m.installed))
	m.installed = nil
	return nil
}

// prefixToMask 将前缀长度转换为 Windows route 命令所需的子网掩码格式。
func prefixToMask(p netip.Prefix) string {
	ones := p.Bits()
	if p.Addr().Is4() {
		mask := net.CIDRMask(ones, 32)
		return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
	}
	// IPv6 在 route 命令中使用前缀表示
	return fmt.Sprintf("/%d", ones)
}

// routeCmd 构造隐藏控制台窗口的 route 命令。
func routeCmd(args ...string) *exec.Cmd {
	system32, err := windows.GetSystemDirectory()
	if err != nil {
		// fallback 到不带完整路径
		cmd := exec.Command("route", args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return cmd
	}
	cmd := exec.Command(filepath.Join(system32, "route.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// ─────────────────────── 接口检测 ───────────────────────

// WindowsInterfaceDetector 使用 route print 和 net 标准库检测网络信息。
type WindowsInterfaceDetector struct{}

// NewInterfaceDetector 返回 Windows 平台的接口检测器。
func NewInterfaceDetector() InterfaceDetector { return &WindowsInterfaceDetector{} }

func (d *WindowsInterfaceDetector) DefaultInterface() (string, netip.Addr, error) {
	cmd := routeCmd("print", "0.0.0.0")
	out, err := cmd.Output()
	if err != nil {
		return "", netip.Addr{}, fmt.Errorf("netctl: route print 失败: %w", err)
	}
	// 解析 route print 输出找默认网关。
	// 典型输出行格式: "0.0.0.0          0.0.0.0     192.168.1.1   192.168.1.100      25"
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			gw, err := netip.ParseAddr(fields[2])
			if err != nil {
				continue
			}
			// 通过接口地址找接口名
			ifAddr, err := netip.ParseAddr(fields[3])
			if err != nil {
				continue
			}
			ifName := findInterfaceByAddr(ifAddr)
			if ifName == "" {
				continue // 未找到匹配接口，尝试下一条路由
			}
			return ifName, gw, nil
		}
	}
	return "", netip.Addr{}, fmt.Errorf("netctl: 未找到默认路由")
}

// findInterfaceByAddr 通过 IP 地址查找对应的网络接口名。
// 排除 corelink 接口，避免将 TUN 设备误认为物理出口接口。
func findInterfaceByAddr(addr netip.Addr) string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		// 跳过 corelink TUN 接口
		if strings.HasPrefix(iface.Name, "corelink") {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok {
				if ipNet.IP.Equal(addr.AsSlice()) {
					return iface.Name
				}
			}
		}
	}
	return ""
}

func (d *WindowsInterfaceDetector) LocalSubnet() (netip.Prefix, error) {
	// 优先使用 DefaultInterface() 定位默认出口接口，避免遍历所有接口
	if defName, _, defErr := d.DefaultInterface(); defErr == nil && defName != "" {
		if iface, err := net.InterfaceByName(defName); err == nil {
			if prefix, ok := firstIPv4Prefix(iface); ok {
				return prefix, nil
			}
		}
	}
	// 回退：遍历所有接口
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		// 跳过非活跃、回环和隧道接口
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if strings.HasPrefix(iface.Name, "corelink") {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
				ones, _ := ipNet.Mask.Size()
				masked := ipNet.IP.Mask(ipNet.Mask)
				// 显式使用 4 字节构造，避免 netip.AddrFromSlice 对超长切片的 fragility
				ip4 := masked.To4()
				if ip4 == nil {
					continue
				}
				addr, ok := netip.AddrFromSlice(ip4)
				if !ok {
					continue
				}
				return netip.PrefixFrom(addr, ones), nil
			}
		}
	}
	return netip.Prefix{}, fmt.Errorf("netctl: 未找到本地子网")
}

func (d *WindowsInterfaceDetector) SystemDNSServers() ([]netip.Addr, error) {
	system32, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("netctl: 获取系统目录失败: %w", err)
	}
	cmd := exec.Command(filepath.Join(system32, "netsh.exe"), "interface", "ip", "show", "dns")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("netctl: netsh 查询 DNS 失败: %w", err)
	}
	seen := make(map[netip.Addr]bool)
	var servers []netip.Addr
	addUnique := func(addr netip.Addr) {
		if !seen[addr] {
			seen[addr] = true
			servers = append(servers, addr)
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// 直接尝试解析每行为 IP 地址
		if addr, err := netip.ParseAddr(line); err == nil {
			addUnique(addr)
			continue
		}
		// 检查 "xxx Servers:" 或 "DNS 服务器:" 格式
		for _, sep := range []string{"Servers:", "服务器:"} {
			if idx := strings.Index(line, sep); idx >= 0 {
				rest := strings.TrimSpace(line[idx+len(sep):])
				if addr, err := netip.ParseAddr(rest); err == nil {
					addUnique(addr)
				}
			}
		}
	}
	return servers, nil
}

// ─────────────────────── DNS 管理 ───────────────────────

// WindowsDNSManager 使用 windnsapi 和 netsh 管理 DNS。
type WindowsDNSManager struct{}

// NewDNSManager 返回 Windows 平台的 DNS 管理器。
func NewDNSManager() DNSManager { return &WindowsDNSManager{} }

func (m *WindowsDNSManager) FlushCache() error {
	return windnsapi.FlushResolverCache()
}

func (m *WindowsDNSManager) SetTUNDNS(ifName string, servers []netip.Addr) error {
	if len(servers) == 0 {
		return nil
	}
	system32, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("netctl: 获取系统目录失败: %w", err)
	}

	// 设置主 DNS 服务器
	primary := servers[0].String()
	cmd := exec.Command(filepath.Join(system32, "netsh.exe"),
		"interface", "ip", "set", "dns", ifName, "static", primary)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netctl: 设置主 DNS 失败: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	// 添加备用 DNS 服务器
	for _, s := range servers[1:] {
		cmd = exec.Command(filepath.Join(system32, "netsh.exe"),
			"interface", "ip", "add", "dns", ifName, s.String())
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = cmd.Run()
	}
	return nil
}

func (m *WindowsDNSManager) RestoreDNS() error {
	// DNS 设置随 TUN 接口销毁自动还原；
	// Windows 会在适配器移除时回退到其他适配器的 DNS 设置。
	return nil
}

// firstIPv4Prefix 从接口中提取第一个 IPv4 子网前缀。
func firstIPv4Prefix(iface *net.Interface) (netip.Prefix, bool) {
	addrs, _ := iface.Addrs()
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() {
			ones, _ := ipNet.Mask.Size()
			masked := ipNet.IP.Mask(ipNet.Mask)
			addr, ok := netip.AddrFromSlice(masked)
			if !ok {
				continue
			}
			return netip.PrefixFrom(addr, ones), true
		}
	}
	return netip.Prefix{}, false
}
