package tun

// ConfigureAddress 给 TUN 接口添加 VIP 地址（CIDR 格式，如 "100.64.0.1/32"）。
//
// 平台实现：
//   - Linux: ip addr add
//   - macOS: ioctl SIOCAIFADDR / SIOCAIFADDR_IN6
//   - Windows: winipcfg LUID.SetIPAddressesForFamily
//   - 其他平台: 返回不支持错误
//
// 幂等：若地址已存在则跳过。
