// Package routepolicy 提供路由策略校验与解析。
package routepolicy

import (
	"fmt"
	"net/netip"
	"strings"
)

// ValidateAlias 校验别名 name 和 FQDN。
func ValidateAlias(name, fqdn, kind string) error {
	if name == "" {
		return fmt.Errorf("alias name 不能为空")
	}
	if fqdn == "" {
		return fmt.Errorf("alias fqdn 不能为空")
	}
	if kind != "internal" && kind != "external" {
		return fmt.Errorf("alias kind 必须是 internal 或 external，得到 %q", kind)
	}
	if err := validateDomain(fqdn); err != nil {
		return fmt.Errorf("alias fqdn 无效: %w", err)
	}
	return nil
}

// ValidatePublishedRoute 校验发布路由的参数合法性。
func ValidatePublishedRoute(kind, routeCIDR, vipCIDR, targetCIDR string) error {
	switch kind {
	case "direct":
		if routeCIDR == "" {
			return fmt.Errorf("direct route 必须指定 route_cidr")
		}
		if _, err := netip.ParsePrefix(routeCIDR); err != nil {
			return fmt.Errorf("route_cidr 无效: %w", err)
		}
	case "static_mapping":
		if vipCIDR == "" || targetCIDR == "" {
			return fmt.Errorf("static_mapping 必须指定 vip_cidr 和 target_cidr")
		}
		vip, err := netip.ParsePrefix(vipCIDR)
		if err != nil {
			return fmt.Errorf("vip_cidr 无效: %w", err)
		}
		target, err := netip.ParsePrefix(targetCIDR)
		if err != nil {
			return fmt.Errorf("target_cidr 无效: %w", err)
		}
		if vip.Addr().Is4() != target.Addr().Is4() {
			return fmt.Errorf("vip_cidr 与 target_cidr 地址族不一致")
		}
		if vip.Bits() != target.Bits() {
			return fmt.Errorf("vip_cidr 与 target_cidr 前缀长度不一致: /%d vs /%d", vip.Bits(), target.Bits())
		}
	case "discovered_mapping":
		if vipCIDR == "" || targetCIDR == "" {
			return fmt.Errorf("discovered_mapping 必须指定 vip_cidr 和 target_cidr")
		}
		if _, err := netip.ParsePrefix(vipCIDR); err != nil {
			return fmt.Errorf("vip_cidr 无效: %w", err)
		}
		if _, err := netip.ParsePrefix(targetCIDR); err != nil {
			return fmt.Errorf("target_cidr 无效: %w", err)
		}
	default:
		return fmt.Errorf("未知 route kind: %q", kind)
	}
	return nil
}

// CheckStaticMappingOverlap 检测新增 static_mapping 的 VIP CIDR 是否与现有路由或 VIP 池冲突。
func CheckStaticMappingOverlap(newVIP string, existingVIPs []string, nodeVIPPool string) error {
	np, err := netip.ParsePrefix(newVIP)
	if err != nil {
		return err
	}
	if nodeVIPPool != "" {
		pool, err := netip.ParsePrefix(nodeVIPPool)
		if err == nil && prefixOverlaps(np, pool) {
			return fmt.Errorf("vip_cidr %s 与节点 VIP 池 %s 重叠", newVIP, nodeVIPPool)
		}
	}
	for _, ev := range existingVIPs {
		ep, err := netip.ParsePrefix(ev)
		if err != nil {
			continue
		}
		if prefixOverlaps(np, ep) {
			return fmt.Errorf("vip_cidr %s 与现有路由 %s 重叠", newVIP, ev)
		}
	}
	return nil
}

// CheckDirectRouteOverlap 检测 direct route 的 CIDR 是否与已有 direct route 重叠。
func CheckDirectRouteOverlap(newCIDR string, existingCIDRs []string) error {
	np, err := netip.ParsePrefix(newCIDR)
	if err != nil {
		return err
	}
	for _, ec := range existingCIDRs {
		ep, err := netip.ParsePrefix(ec)
		if err != nil {
			continue
		}
		if prefixOverlaps(np, ep) {
			return fmt.Errorf("direct route %s 与现有 direct route %s 重叠", newCIDR, ec)
		}
	}
	return nil
}

func prefixOverlaps(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

func validateDomain(fqdn string) error {
	if len(fqdn) > 253 {
		return fmt.Errorf("域名超过 253 字符")
	}
	labels := strings.Split(fqdn, ".")
	if len(labels) < 2 {
		return fmt.Errorf("域名至少需要两个标签")
	}
	for _, label := range labels {
		if len(label) == 0 {
			return fmt.Errorf("域名包含空标签")
		}
		if len(label) > 63 {
			return fmt.Errorf("域名标签超过 63 字符")
		}
		for _, c := range label {
			if !isValidDomainChar(c) {
				return fmt.Errorf("域名标签包含非法字符: %c", c)
			}
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("域名标签不能以连字符开头或结尾")
		}
	}
	return nil
}

func isValidDomainChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '-'
}
