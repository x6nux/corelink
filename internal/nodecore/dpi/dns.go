package dpi

// SniffDNS 检测 UDP payload 是否为 DNS 查询报文。
// 参考 sing-box common/sniff/dns.go 的判断逻辑。
func SniffDNS(payload []byte) bool {
	if len(payload) < 12 {
		return false
	}
	// QR 位（第 3 字节 bit 7）必须为 0（查询）
	if payload[2]&0x80 != 0 {
		return false
	}
	// QDCOUNT（字节 4-5）必须 > 0
	if payload[4] == 0 && payload[5] == 0 {
		return false
	}
	// ANCOUNT（字节 6-7）和 NSCOUNT（字节 8-9）应为 0（查询包无应答）
	for i := 6; i < 10; i++ {
		if payload[i] != 0 {
			return false
		}
	}
	return true
}
