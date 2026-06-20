package geoip

import (
	"fmt"
	"net/netip"

	"google.golang.org/protobuf/encoding/protowire"
)

// parseGeoIPList 是入口函数，解析 geoip.dat 的原始字节。
func parseGeoIPList(data []byte) (*geoIPList, error) {
	return parseGeoIPListWire(data)
}

// parseGeoIPListWire 解码 GeoIPList message。
// wire 格式: field 1 (bytes) = repeated GeoIP 条目。
func parseGeoIPListWire(data []byte) (*geoIPList, error) {
	list := &geoIPList{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("geoip: 解析 GeoIPList tag 失败")
		}
		data = data[n:]

		if num == 1 && typ == protowire.BytesType {
			// field 1: GeoIP 条目（length-delimited）
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 解析 GeoIPList field 1 bytes 失败")
			}
			data = data[n:]

			entry, err := parseGeoIPWire(val)
			if err != nil {
				return nil, fmt.Errorf("geoip: 解析 GeoIP 条目: %w", err)
			}
			list.Entry = append(list.Entry, entry)
		} else {
			// 跳过未知字段
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 跳过 GeoIPList 未知字段 %d 失败", num)
			}
			data = data[n:]
		}
	}
	return list, nil
}

// parseGeoIPWire 解码单个 GeoIP message。
// wire 格式: field 1 (bytes) = country_code, field 2 (bytes) = repeated CIDR。
func parseGeoIPWire(data []byte) (*geoIP, error) {
	entry := &geoIP{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("geoip: 解析 GeoIP tag 失败")
		}
		data = data[n:]

		switch {
		case num == 1 && typ == protowire.BytesType:
			// field 1: country_code
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 解析 GeoIP country_code 失败")
			}
			data = data[n:]
			entry.CountryCode = string(val)

		case num == 2 && typ == protowire.BytesType:
			// field 2: CIDR 条目
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 解析 GeoIP CIDR bytes 失败")
			}
			data = data[n:]

			cidr, err := parseCIDRWire(val)
			if err != nil {
				return nil, fmt.Errorf("geoip: 解析 CIDR: %w", err)
			}
			entry.CIDR = append(entry.CIDR, cidr)

		default:
			// 跳过未知字段
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 跳过 GeoIP 未知字段 %d 失败", num)
			}
			data = data[n:]
		}
	}
	return entry, nil
}

// parseCIDRWire 解码单个 CIDR message。
// wire 格式: field 1 (bytes) = ip bytes, field 2 (varint) = prefix length。
func parseCIDRWire(data []byte) (*cidrEntry, error) {
	entry := &cidrEntry{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("geoip: 解析 CIDR tag 失败")
		}
		data = data[n:]

		switch {
		case num == 1 && typ == protowire.BytesType:
			// field 1: IP 字节
			val, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 解析 CIDR ip bytes 失败")
			}
			data = data[n:]
			entry.IP = make([]byte, len(val))
			copy(entry.IP, val)

		case num == 2 && typ == protowire.VarintType:
			// field 2: 前缀长度
			val, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 解析 CIDR prefix varint 失败")
			}
			data = data[n:]
			entry.Prefix = uint32(val)

		default:
			// 跳过未知字段
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: 跳过 CIDR 未知字段 %d 失败", num)
			}
			data = data[n:]
		}
	}
	return entry, nil
}

// cidrToPrefix 将 cidrEntry 转换为 Go 标准 netip.Prefix。
// 4 字节视为 IPv4，16 字节视为 IPv6，其他长度返回 false。
func cidrToPrefix(c *cidrEntry) (netip.Prefix, bool) {
	var addr netip.Addr
	switch len(c.IP) {
	case 4:
		addr = netip.AddrFrom4([4]byte(c.IP))
	case 16:
		addr = netip.AddrFrom16([16]byte(c.IP))
	default:
		return netip.Prefix{}, false
	}
	pfx, err := addr.Prefix(int(c.Prefix))
	if err != nil {
		return netip.Prefix{}, false
	}
	return pfx, true
}
