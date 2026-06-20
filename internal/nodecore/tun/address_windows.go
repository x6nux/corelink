//go:build windows

package tun

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/x6nux/corelink/internal/platform/winipcfg"
	"golang.org/x/sys/windows"
)

// ConfigureAddress 使用 winipcfg LUID API 配置 Windows TUN 设备地址。
//
// 通过接口索引查找 LUID，然后使用 IP Helper API 设置单播地址。
// 同时禁用 DAD、路由器发现和 DHCP，以避免与 VPN 隧道地址冲突。
// 幂等：若地址已存在则跳过。
func ConfigureAddress(tunName string, addrCIDR string) error {
	ip, ipNet, err := net.ParseCIDR(addrCIDR)
	if err != nil {
		return fmt.Errorf("tun: 解析地址 %q 失败: %w", addrCIDR, err)
	}

	// 查找 TUN 接口
	iface, err := net.InterfaceByName(tunName)
	if err != nil {
		return fmt.Errorf("tun: 查找接口 %q 失败: %w", tunName, err)
	}
	luid, err := winipcfg.LUIDFromIndex(uint32(iface.Index))
	if err != nil {
		return fmt.Errorf("tun: 获取 LUID 失败: %w", err)
	}

	ones, _ := ipNet.Mask.Size()
	prefix := netip.PrefixFrom(ipToAddr(ip), ones)

	if ip.To4() != nil {
		// 配置 IPv4 地址
		err = luid.SetIPAddressesForFamily(winipcfg.AddressFamily(windows.AF_INET), []netip.Prefix{prefix})
		if err != nil {
			return fmt.Errorf("tun: 配置 IPv4 地址失败: %w", err)
		}
		// 禁用 DAD、Router Discovery、DHCP（VPN 隧道不需要这些）
		inetIf, err := luid.IPInterface(winipcfg.AddressFamily(windows.AF_INET))
		if err == nil {
			inetIf.DadTransmits = 0
			inetIf.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
			inetIf.ManagedAddressConfigurationSupported = false
			inetIf.OtherStatefulConfigurationSupported = false
			_ = inetIf.Set()
		}
	} else {
		// 配置 IPv6 地址
		err = luid.SetIPAddressesForFamily(winipcfg.AddressFamily(windows.AF_INET6), []netip.Prefix{prefix})
		if err != nil {
			return fmt.Errorf("tun: 配置 IPv6 地址失败: %w", err)
		}
		// 禁用 IPv6 DAD 和路由器发现
		inet6If, err := luid.IPInterface(winipcfg.AddressFamily(windows.AF_INET6))
		if err == nil {
			inet6If.DadTransmits = 0
			inet6If.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
			_ = inet6If.Set()
		}
	}

	// 禁用 DNS 注册（TUN 接口不应注册到 DNS）
	_ = luid.DisableDNSRegistration()
	return nil
}

// ipToAddr 将 net.IP 转换为 netip.Addr。
// 添加 nil 保护，避免 To16() 返回 nil 时 panic。
func ipToAddr(ip net.IP) netip.Addr {
	if ip4 := ip.To4(); ip4 != nil {
		return netip.AddrFrom4([4]byte(ip4))
	}
	if ip16 := ip.To16(); ip16 != nil {
		return netip.AddrFrom16([16]byte(ip16))
	}
	return netip.Addr{}
}
