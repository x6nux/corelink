//go:build darwin

package tun

import (
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// siocaifaddrIn6 = SIOCAIFADDR_IN6 (x/sys/unix 未导出，值来自 netinet6/in6_var.h)。
	siocaifaddrIn6      = 0x8080691a
	in6IffNodad         = 0x0020
	in6IffSecured       = 0x0400
	nd6InfiniteLifetime = 0xFFFFFFFF
)

// ifAliasReq 是 macOS SIOCAIFADDR ioctl 的请求结构体（IPv4）。
type ifAliasReq struct {
	Name    [unix.IFNAMSIZ]byte
	Addr    unix.RawSockaddrInet4
	Dstaddr unix.RawSockaddrInet4
	Mask    unix.RawSockaddrInet4
}

// ifAliasReq6 是 macOS SIOCAIFADDR_IN6 ioctl 的请求结构体（IPv6）。
type ifAliasReq6 struct {
	Name     [unix.IFNAMSIZ]byte
	Addr     unix.RawSockaddrInet6
	Dstaddr  unix.RawSockaddrInet6
	Mask     unix.RawSockaddrInet6
	Flags    uint32
	Lifetime addrLifetime6
}

// addrLifetime6 描述 IPv6 地址的生存期参数。
// macOS 内核的 in6_addrlifetime 使用 time_t（int64）表示过期时间，非浮点数。
type addrLifetime6 struct {
	Expire    int64
	Preferred int64
	Vltime    uint32
	Pltime    uint32
}

// ConfigureAddress 给 TUN 接口添加 VIP 地址（macOS: ioctl SIOCAIFADDR）。
// 支持 IPv4 和 IPv6，addrCIDR 格式如 "100.64.0.1/10" 或 "fd00::1/64"。
func ConfigureAddress(tunName string, addrCIDR string) error {
	ip, ipNet, err := net.ParseCIDR(addrCIDR)
	if err != nil {
		return fmt.Errorf("tun: 解析地址 %q 失败: %w", addrCIDR, err)
	}
	if ip4 := ip.To4(); ip4 != nil {
		return configureIPv4(tunName, ip4, ipNet.Mask)
	}
	return configureIPv6(tunName, ip, ipNet.Mask)
}

// configureIPv4 通过 SIOCAIFADDR ioctl 为接口设置 IPv4 地址。
// 使用 unix.SIOCAIFADDR（golang.org/x/sys/unix 已导出，值 0x8040691a）。
func configureIPv4(tunName string, ip net.IP, mask net.IPMask) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("tun: 创建 socket 失败: %w", err)
	}
	defer unix.Close(fd)

	var req ifAliasReq
	copy(req.Name[:], tunName)
	req.Addr.Len = unix.SizeofSockaddrInet4
	req.Addr.Family = unix.AF_INET
	copy(req.Addr.Addr[:], ip)
	req.Dstaddr.Len = unix.SizeofSockaddrInet4
	req.Dstaddr.Family = unix.AF_INET
	copy(req.Dstaddr.Addr[:], ip)
	req.Mask.Len = unix.SizeofSockaddrInet4
	req.Mask.Family = unix.AF_INET
	copy(req.Mask.Addr[:], mask)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
		uintptr(unix.SIOCAIFADDR), uintptr(unsafe.Pointer(&req))); errno != 0 {
		return fmt.Errorf("tun: SIOCAIFADDR %s 失败: %w", tunName, errno)
	}
	return nil
}

// configureIPv6 通过 SIOCAIFADDR_IN6 ioctl 为接口设置 IPv6 地址。
func configureIPv6(tunName string, ip net.IP, mask net.IPMask) error {
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("tun: 创建 IPv6 socket 失败: %w", err)
	}
	defer unix.Close(fd)

	var req ifAliasReq6
	copy(req.Name[:], tunName)
	req.Addr.Len = unix.SizeofSockaddrInet6
	req.Addr.Family = unix.AF_INET6
	copy(req.Addr.Addr[:], ip.To16())
	req.Dstaddr.Len = unix.SizeofSockaddrInet6
	req.Dstaddr.Family = unix.AF_INET6
	copy(req.Dstaddr.Addr[:], ip.To16())
	var maskBytes [16]byte
	copy(maskBytes[:], mask)
	req.Mask.Len = unix.SizeofSockaddrInet6
	req.Mask.Family = unix.AF_INET6
	copy(req.Mask.Addr[:], maskBytes[:])
	req.Flags = in6IffNodad | in6IffSecured
	req.Lifetime.Vltime = nd6InfiniteLifetime
	req.Lifetime.Pltime = nd6InfiniteLifetime

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
		uintptr(siocaifaddrIn6), uintptr(unsafe.Pointer(&req))); errno != 0 {
		return fmt.Errorf("tun: SIOCAIFADDR_IN6 %s 失败: %w", tunName, errno)
	}
	return nil
}
