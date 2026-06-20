// Package geoip 解析 V2Ray geoip.dat（protobuf 格式）并提供 IP → 国家查询。
package geoip

// V2Ray GeoIP protobuf 结构（使用 protowire 手动解码，不依赖 v2ray-core）。

// geoIPList 对应 GeoIPList message，包含多个 GeoIP 条目。
type geoIPList struct {
	Entry []*geoIP
}

// geoIP 对应 GeoIP message，包含国家代码和 CIDR 列表。
type geoIP struct {
	CountryCode string
	CIDR        []*cidrEntry
}

// cidrEntry 对应 CIDR message，包含 IP 字节和前缀长度。
type cidrEntry struct {
	IP     []byte // 4 字节 IPv4 或 16 字节 IPv6
	Prefix uint32
}
