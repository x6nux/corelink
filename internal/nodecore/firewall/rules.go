// Package firewall 提供 Linux iptables/nftables 规则生成与管理抽象。
package firewall

import (
	"fmt"
	"net"
	"strings"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

const (
	ChainPrefix = "CORELINK"
	NATChain    = ChainPrefix + "-NAT"
	FWDChain    = ChainPrefix + "-FWD"
	DNSChain    = ChainPrefix + "-DNS"
	DNSOutChain = ChainPrefix + "-DNS-OUT" // 本机发起的 DNS 查询走 OUTPUT 链
)

// Command 表示一条 iptables/nftables 命令。
type Command struct {
	Table string
	Args  []string
}

func (c Command) String() string {
	return fmt.Sprintf("iptables -t %s %s", c.Table, joinArgs(c.Args))
}

// GenerateCleanup 生成清理 CoreLink 自有链的命令。
// GenerateCleanup 生成清理 CoreLink 自有链的命令。
// 每条链只在其所属的表中 flush（DNSChain/NATChain → nat, FWDChain → filter）。
func GenerateCleanup() []Command {
	var cmds []Command
	// nat 表中的链
	for _, chain := range []string{NATChain, NATChain + "-POST", DNSChain, DNSOutChain} {
		cmds = append(cmds, Command{Table: "nat", Args: []string{"-F", chain}})
	}
	// filter 表中的链
	cmds = append(cmds, Command{Table: "filter", Args: []string{"-F", FWDChain}})
	return cmds
}

// GenerateDNSRedirect 生成 DNS 重定向规则。
func GenerateDNSRedirect(cfg *genv1.DNSConfig) []Command {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	target := fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.ListenPort)
	var cmds []Command

	switch cfg.InterceptMode {
	case "local":
		port := fmt.Sprintf("%d", cfg.ListenPort)
		// PREROUTING: 捕获转发来的 DNS（LAN 客户端经本机转发）
		cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSChain, "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port}})
		cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSChain, "-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port}})
		// OUTPUT: 捕获本机发起的 DNS 查询。
		// 关键：必须先 RETURN 上游 DNS（防止 proxy → upstream → REDIRECT → proxy 无限循环）
		for _, upstream := range cfg.Upstreams {
			// 使用 net.SplitHostPort 正确处理 IPv6 地址（如 "[::1]:53"）
			ip, _, err := net.SplitHostPort(upstream)
			if err != nil {
				// 无端口的纯地址，直接使用
				ip = strings.Trim(upstream, "[]")
			}
			// UDP 和 TCP 都需要 RETURN，否则 TCP DNS 查询会被 REDIRECT 回 proxy 形成循环
			cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSOutChain, "-d", ip, "-p", "udp", "--dport", "53", "-j", "RETURN"}})
			cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSOutChain, "-d", ip, "-p", "tcp", "--dport", "53", "-j", "RETURN"}})
		}
		// 同时跳过发往 proxy 自身的流量（UDP + TCP）
		cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSOutChain, "-d", "127.0.0.1", "-p", "udp", "--dport", fmt.Sprintf("%d", cfg.ListenPort), "-j", "RETURN"}})
		cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSOutChain, "-d", "127.0.0.1", "-p", "tcp", "--dport", fmt.Sprintf("%d", cfg.ListenPort), "-j", "RETURN"}})
		cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSOutChain, "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port}})
		cmds = append(cmds, Command{Table: "nat", Args: []string{"-A", DNSOutChain, "-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port}})
	case "lan":
		for _, iface := range cfg.LanInterfaces {
			cmds = append(cmds, Command{
				Table: "nat",
				Args:  []string{"-A", DNSChain, "-i", iface, "-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", target},
			})
		}
		for _, cidr := range cfg.LanCidrs {
			cmds = append(cmds, Command{
				Table: "nat",
				Args:  []string{"-A", DNSChain, "-s", cidr, "-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", target},
			})
		}
	}
	return cmds
}

// GenerateEgressRules 生成出口 DNAT/SNAT/FORWARD 规则。
func GenerateEgressRules(rules []*genv1.EgressRule) []Command {
	var cmds []Command
	for _, r := range rules {
		switch r.Kind {
		case "direct":
			cmds = append(cmds, Command{
				Table: "filter",
				Args:  []string{"-A", FWDChain, "-d", r.VipPrefix, "-j", "ACCEPT"},
			})
			if r.Snat {
				cmds = append(cmds, Command{
					Table: "nat",
					Args:  []string{"-A", NATChain + "-POST", "-s", r.VipPrefix, "-j", "MASQUERADE"},
				})
			}
		case "static_mapping":
			cmds = append(cmds, subnetDNAT(r.VipPrefix, r.TargetPrefix))
			cmds = append(cmds, Command{
				Table: "filter",
				Args:  []string{"-A", FWDChain, "-d", r.TargetPrefix, "-j", "ACCEPT"},
			})
			// NETMAP/DNAT 流量回程必须经本机 conntrack 反向翻译，
			// 对目标子网做 MASQUERADE 使回包走本机而非直连源 VIP。
			cmds = append(cmds, Command{
				Table: "nat",
				Args:  []string{"-A", NATChain + "-POST", "-d", r.TargetPrefix, "-j", "MASQUERADE"},
			})
			if r.Snat {
				cmds = append(cmds, Command{
					Table: "nat",
					Args:  []string{"-A", NATChain + "-POST", "-s", r.TargetPrefix, "-j", "MASQUERADE"},
				})
			}
		case "discovered_mapping":
			cmds = append(cmds, subnetDNAT(r.VipPrefix, r.TargetPrefix))
			cmds = append(cmds, Command{
				Table: "filter",
				Args:  []string{"-A", FWDChain, "-d", r.TargetPrefix, "-j", "ACCEPT"},
			})
			cmds = append(cmds, Command{
				Table: "nat",
				Args:  []string{"-A", NATChain + "-POST", "-d", r.TargetPrefix, "-j", "MASQUERADE"},
			})
			if r.Snat {
				cmds = append(cmds, Command{
					Table: "nat",
					Args:  []string{"-A", NATChain + "-POST", "-s", r.TargetPrefix, "-j", "MASQUERADE"},
				})
			}
		}
	}
	return cmds
}

// subnetDNAT 生成子网级 NAT 规则：/32 用 DNAT，子网用 NETMAP。
func subnetDNAT(vipPrefix, targetPrefix string) Command {
	if strings.HasSuffix(vipPrefix, "/32") {
		target := strings.TrimSuffix(targetPrefix, "/32")
		return Command{Table: "nat", Args: []string{"-A", NATChain, "-d", vipPrefix, "-j", "DNAT", "--to-destination", target}}
	}
	return Command{Table: "nat", Args: []string{"-A", NATChain, "-d", vipPrefix, "-j", "NETMAP", "--to", targetPrefix}}
}

func joinArgs(args []string) string {
	s := ""
	for i, a := range args {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}
