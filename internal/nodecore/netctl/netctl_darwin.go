//go:build darwin

package netctl

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// ────────────────────────────────────────────────────────────────────
// DarwinRouteManager — 通过 BSD route socket (AF_ROUTE) 管理路由表
// ────────────────────────────────────────────────────────────────────

// DarwinRouteManager macOS 路由管理，使用 AF_ROUTE socket 直接操作内核路由表。
type DarwinRouteManager struct {
	mu        sync.Mutex
	installed []netip.Prefix // SetAutoRoute 安装的前缀，用于 UnsetAutoRoute 清理
	gw4       netip.Addr     // 当前 auto route 的 IPv4 网关
	gw6       netip.Addr     // 当前 auto route 的 IPv6 网关
}

// NewRouteManager 创建 macOS 路由管理器。
func NewRouteManager() RouteManager { return &DarwinRouteManager{} }

// AddRoute 通过 AF_ROUTE socket 添加一条路由。
func (m *DarwinRouteManager) AddRoute(dst netip.Prefix, gateway netip.Addr, ifName string) error {
	return execRouteOp(unix.RTM_ADD, dst, gateway, ifName)
}

// RemoveRoute 通过 AF_ROUTE socket 删除一条路由。
func (m *DarwinRouteManager) RemoveRoute(dst netip.Prefix, gateway netip.Addr) error {
	return execRouteOp(unix.RTM_DELETE, dst, gateway, "")
}

// SetAutoRoute 为 TUN 设备批量安装路由。
func (m *DarwinRouteManager) SetAutoRoute(tunName string, addrs []netip.Prefix, gw4, gw6 netip.Addr) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.installed = nil
	m.gw4 = gw4
	m.gw6 = gw6

	for _, prefix := range addrs {
		gw := gw4
		if prefix.Addr().Is6() {
			gw = gw6
		}
		if !gw.IsValid() {
			continue
		}
		if err := execRouteOp(unix.RTM_ADD, prefix, gw, tunName); err != nil {
			slog.Warn("netctl: 添加自动路由失败", "prefix", prefix, "err", err)
			continue
		}
		m.installed = append(m.installed, prefix)
	}
	slog.Info("netctl: macOS 自动路由已安装", "count", len(m.installed))
	return nil
}

// UnsetAutoRoute 清理 SetAutoRoute 安装的所有路由。
func (m *DarwinRouteManager) UnsetAutoRoute() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, prefix := range m.installed {
		gw := m.gw4
		if prefix.Addr().Is6() {
			gw = m.gw6
		}
		if err := execRouteOp(unix.RTM_DELETE, prefix, gw, ""); err != nil {
			slog.Debug("netctl: 删除自动路由失败（可能已不存在）", "prefix", prefix, "err", err)
		}
	}
	slog.Info("netctl: macOS 自动路由已清理", "count", len(m.installed))
	m.installed = nil
	return nil
}

// execRouteOp 执行路由操作（RTM_ADD / RTM_DELETE），通过 AF_ROUTE socket 直接与内核通信。
// 参考 sing-tun tun_darwin.go:504-538 的 execRoute 实现。
func execRouteOp(rtmType int, dst netip.Prefix, gateway netip.Addr, ifName string) error {
	msg := route.RouteMessage{
		Type:    rtmType,
		Version: unix.RTM_VERSION,
		Flags:   unix.RTF_STATIC,
		Seq:     1,
	}
	if rtmType == unix.RTM_ADD {
		msg.Flags |= unix.RTF_UP
	}

	// 仅在网关有效时设置 RTF_GATEWAY 标志
	hasGateway := gateway.IsValid()
	if hasGateway {
		msg.Flags |= unix.RTF_GATEWAY
	}

	// 如果指定了接口名，获取接口索引并设置 RTF_IFSCOPE
	if ifName != "" {
		iface, err := net.InterfaceByName(ifName)
		if err == nil {
			msg.Index = iface.Index
			if rtmType == unix.RTM_ADD {
				msg.Flags |= unix.RTF_IFSCOPE
			}
		}
	}

	if dst.Addr().Is4() {
		msg.Addrs = make([]route.Addr, syscall.RTAX_NETMASK+1)
		msg.Addrs[syscall.RTAX_DST] = &route.Inet4Addr{IP: dst.Addr().As4()}
		msg.Addrs[syscall.RTAX_NETMASK] = &route.Inet4Addr{IP: prefixToIPv4Mask(dst)}
		if hasGateway {
			if !gateway.Is4() && !gateway.Is4In6() {
				return fmt.Errorf("netctl: IPv4 目标不可使用 IPv6 网关: %s", gateway)
			}
			msg.Addrs[syscall.RTAX_GATEWAY] = &route.Inet4Addr{IP: gateway.As4()}
		}
	} else {
		msg.Addrs = make([]route.Addr, syscall.RTAX_NETMASK+1)
		msg.Addrs[syscall.RTAX_DST] = &route.Inet6Addr{IP: dst.Addr().As16()}
		msg.Addrs[syscall.RTAX_NETMASK] = &route.Inet6Addr{IP: prefixToIPv6Mask(dst)}
		if hasGateway {
			msg.Addrs[syscall.RTAX_GATEWAY] = &route.Inet6Addr{IP: gateway.As16()}
		}
	}

	data, err := msg.Marshal()
	if err != nil {
		return fmt.Errorf("netctl: 序列化路由消息失败: %w", err)
	}

	// 打开 AF_ROUTE socket 并写入路由消息
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, 0)
	if err != nil {
		return fmt.Errorf("netctl: 创建路由 socket 失败: %w", err)
	}
	defer unix.Close(fd)

	_, err = unix.Write(fd, data)
	if err != nil {
		return fmt.Errorf("netctl: 写入路由消息失败 (type=%d, dst=%s): %w", rtmType, dst, err)
	}
	return nil
}

// prefixToIPv4Mask 将 netip.Prefix 转换为 [4]byte 子网掩码。
func prefixToIPv4Mask(p netip.Prefix) [4]byte {
	mask := net.CIDRMask(p.Bits(), 32)
	var out [4]byte
	copy(out[:], mask)
	return out
}

// prefixToIPv6Mask 将 netip.Prefix 转换为 [16]byte 子网掩码。
func prefixToIPv6Mask(p netip.Prefix) [16]byte {
	mask := net.CIDRMask(p.Bits(), 128)
	var out [16]byte
	copy(out[:], mask)
	return out
}

// ────────────────────────────────────────────────────────────────────
// DarwinInterfaceDetector — 解析 BSD 路由表检测默认接口
// ────────────────────────────────────────────────────────────────────

// DarwinInterfaceDetector macOS 接口检测器，通过 route.FetchRIB 解析路由表。
type DarwinInterfaceDetector struct{}

// NewInterfaceDetector 创建 macOS 接口检测器。
func NewInterfaceDetector() InterfaceDetector { return &DarwinInterfaceDetector{} }

// DefaultInterface 通过解析 BSD 路由表找到默认网关接口。
// 参考 sing-tun monitor_darwin.go 的 checkUpdate 实现。
func (d *DarwinInterfaceDetector) DefaultInterface() (string, netip.Addr, error) {
	// 获取路由信息库（Routing Information Base）
	rib, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return "", netip.Addr{}, fmt.Errorf("netctl: FetchRIB 失败: %w", err)
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return "", netip.Addr{}, fmt.Errorf("netctl: ParseRIB 失败: %w", err)
	}

	for _, msg := range msgs {
		rm, ok := msg.(*route.RouteMessage)
		if !ok {
			continue
		}
		// 必须是 UP + GATEWAY 路由
		if rm.Flags&unix.RTF_UP == 0 || rm.Flags&unix.RTF_GATEWAY == 0 {
			continue
		}
		if len(rm.Addrs) <= syscall.RTAX_NETMASK {
			continue
		}

		// 检查 destination 是否为 0.0.0.0（默认路由）
		dstAddr, ok := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		if !ok {
			continue
		}
		if dstAddr.IP != [4]byte{0, 0, 0, 0} {
			continue
		}

		// 检查 netmask 是否也为 /0。
		// 内核对默认路由常省略 netmask（nil），视为 /0 默认路由——不跳过。
		if rm.Addrs[syscall.RTAX_NETMASK] != nil {
			maskAddr, ok := rm.Addrs[syscall.RTAX_NETMASK].(*route.Inet4Addr)
			if !ok {
				continue
			}
			ones, _ := net.IPMask(maskAddr.IP[:]).Size()
			if ones != 0 {
				continue
			}
		}

		// 通过路由消息中的接口索引获取接口名
		iface, err := net.InterfaceByIndex(rm.Index)
		if err != nil {
			continue
		}

		// 提取网关 IP
		gwAddr, ok := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr)
		if !ok {
			slog.Warn("netctl: 默认路由网关地址无效，尝试下一条路由", "iface", iface.Name)
			continue
		}
		gateway := netip.AddrFrom4(gwAddr.IP)

		return iface.Name, gateway, nil
	}
	return "", netip.Addr{}, fmt.Errorf("netctl: 未找到默认路由")
}

// LocalSubnet 获取默认接口的首个 IPv4 子网前缀。
func (d *DarwinInterfaceDetector) LocalSubnet() (netip.Prefix, error) {
	ifName, _, err := d.DefaultInterface()
	if err != nil {
		return netip.Prefix{}, err
	}
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("netctl: 获取接口 %s 失败: %w", ifName, err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("netctl: 获取接口 %s 地址失败: %w", ifName, err)
	}
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

// SystemDNSServers 通过 scutil --dns 获取系统 DNS 服务器列表。
// 若 scutil 失败则回退到 /etc/resolv.conf。
func (d *DarwinInterfaceDetector) SystemDNSServers() ([]netip.Addr, error) {
	// 优先尝试 scutil --dns（macOS 原生方式，可获取 mDNSResponder 实际使用的 DNS）
	out, err := exec.Command("scutil", "--dns").Output()
	if err == nil {
		servers := parseScutilDNS(string(out))
		if len(servers) > 0 {
			return servers, nil
		}
	}

	// 回退到 /etc/resolv.conf
	return parseResolvConf()
}

// parseScutilDNS 从 scutil --dns 输出中提取 nameserver 地址。
// scutil 输出格式示例：
//
//	resolver #1
//	  nameserver[0] : 8.8.8.8
//	  nameserver[1] : 8.8.4.4
func parseScutilDNS(output string) []netip.Addr {
	seen := make(map[netip.Addr]bool)
	var servers []netip.Addr
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		addrStr := strings.TrimSpace(parts[1])
		addr, err := netip.ParseAddr(addrStr)
		if err != nil {
			continue
		}
		if !seen[addr] {
			seen[addr] = true
			servers = append(servers, addr)
		}
	}
	// scanner.Err() 对于 strings.NewReader 输入不会返回错误，但保持一致性检查。
	_ = scanner.Err()
	return servers
}

// parseResolvConf 解析 /etc/resolv.conf 中的 nameserver 行。
func parseResolvConf() ([]netip.Addr, error) {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return nil, fmt.Errorf("netctl: 读取 /etc/resolv.conf 失败: %w", err)
	}
	defer f.Close()
	var servers []netip.Addr
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if addr, err := netip.ParseAddr(fields[1]); err == nil {
			servers = append(servers, addr)
		}
	}
	if err := scanner.Err(); err != nil {
		return servers, fmt.Errorf("netctl: 扫描 /etc/resolv.conf 失败: %w", err)
	}
	return servers, nil
}

// ────────────────────────────────────────────────────────────────────
// DarwinDNSManager — macOS DNS 管理
// ────────────────────────────────────────────────────────────────────

// DarwinDNSManager macOS DNS 管理器。
type DarwinDNSManager struct{}

// NewDNSManager 创建 macOS DNS 管理器。
func NewDNSManager() DNSManager { return &DarwinDNSManager{} }

// FlushCache 刷新 macOS DNS 缓存（通过 dscacheutil -flushcache）。
func (m *DarwinDNSManager) FlushCache() error {
	return exec.Command("dscacheutil", "-flushcache").Run()
}

// SetTUNDNS 在 macOS 上无需修改系统 DNS 设置，TUN 栈内拦截处理 DNS 请求。
func (m *DarwinDNSManager) SetTUNDNS(ifName string, servers []netip.Addr) error {
	return nil
}

// RestoreDNS 在 macOS 上无需恢复系统 DNS 设置。
func (m *DarwinDNSManager) RestoreDNS() error {
	return nil
}
