package geoip

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// BuildTestDat 构建一个最小的 geoip.dat 测试数据，包含 CN(1.0.0.0/8) 和 US(8.0.0.0/8)。
// 导出供其他包的测试使用。
func BuildTestDat(t *testing.T) []byte {
	t.Helper()

	encodeCIDR := func(ip [4]byte, prefix uint32) []byte {
		var b []byte
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendBytes(b, ip[:])
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(prefix))
		return b
	}

	encodeGeoIP := func(code string, cidrs ...[]byte) []byte {
		var b []byte
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendBytes(b, []byte(code))
		for _, c := range cidrs {
			b = protowire.AppendTag(b, 2, protowire.BytesType)
			b = protowire.AppendBytes(b, c)
		}
		return b
	}

	cn := encodeGeoIP("CN", encodeCIDR([4]byte{1, 0, 0, 0}, 8))
	us := encodeGeoIP("US", encodeCIDR([4]byte{8, 0, 0, 0}, 8))

	var dat []byte
	dat = protowire.AppendTag(dat, 1, protowire.BytesType)
	dat = protowire.AppendBytes(dat, cn)
	dat = protowire.AppendTag(dat, 1, protowire.BytesType)
	dat = protowire.AppendBytes(dat, us)
	return dat
}
