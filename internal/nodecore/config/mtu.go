// Package config 提供 agent/relay 引导配置（S3-P1）。
package config

import "fmt"

// MTU 预设档位常量。
const (
	// MTUCompat 兼容模式 MTU，适用于多层封装（overlay over overlay）或保守网络环境。
	MTUCompat uint32 = 1400
	// MTUStandard 标准以太网 MTU，适用于本地直连或已知无额外封装的网络。
	MTUStandard uint32 = 1500
	// MTUJumbo 巨帧 MTU，适用于数据中心内部高性能链路。
	MTUJumbo uint32 = 9000
	// MTUMax 最大允许 MTU。
	MTUMax uint32 = 65535
	// DefaultMTU 缺省 MTU，0 值时自动使用该档位。
	DefaultMTU = MTUCompat
)

// validMTUs 允许的 MTU 枚举集合。0 表示"使用默认值"，不作为实际 MTU 下发。
var validMTUs = map[uint32]bool{
	0:           true,
	MTUCompat:   true,
	MTUStandard: true,
	MTUJumbo:    true,
	MTUMax:      true,
}

// ValidateMTU 校验 mtu 是否为允许的预设档位。
// 0 表示使用默认值（1400），也视为合法输入。
func ValidateMTU(mtu uint32) error {
	if validMTUs[mtu] {
		return nil
	}
	return fmt.Errorf("config: 不支持的 MTU 值 %d，可选: 0(默认1400)/1400/1500/9000/65535", mtu)
}

// ResolveMTU 将配置值转换为实际使用的 MTU 整数。
// mtu == 0 时返回 DefaultMTU(1400)，否则原样返回。
func ResolveMTU(mtu uint32) int {
	if mtu == 0 {
		return int(DefaultMTU)
	}
	return int(mtu)
}
