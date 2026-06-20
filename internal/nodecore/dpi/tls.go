package dpi

import "encoding/binary"

// parseTLSClientHello 解析 TLS ClientHello 握手报文，提取 SNI 域名。
// 返回 (sni, ok)；ok=false 表示不是合法的 ClientHello 或不含 SNI。
func parseTLSClientHello(payload []byte) (string, bool) {
	// TLS Record 头：ContentType(1) + Version(2) + Length(2) = 5
	if len(payload) < 5 {
		return "", false
	}
	if payload[0] != 0x16 { // ContentType: Handshake
		return "", false
	}
	// Handshake 头：Type(1) + Length(3) + ClientHello
	if len(payload) < 9 {
		return "", false
	}
	if payload[5] != 0x01 { // HandshakeType: ClientHello
		return "", false
	}

	// ClientHello 结构：Version(2) + Random(32) + SessionIDLen(1)
	offset := 9
	if len(payload) < offset+2+32+1 {
		return "", false
	}
	offset += 2 + 32 // 跳过 Version + Random
	sessionIDLen := int(payload[offset])
	offset++
	if len(payload) < offset+sessionIDLen {
		return "", false
	}
	offset += sessionIDLen

	// CipherSuites
	if len(payload) < offset+2 {
		return "", false
	}
	cipherLen := int(binary.BigEndian.Uint16(payload[offset:]))
	offset += 2
	if len(payload) < offset+cipherLen {
		return "", false
	}
	offset += cipherLen

	// CompressionMethods
	if len(payload) < offset+1 {
		return "", false
	}
	compLen := int(payload[offset])
	offset++
	if len(payload) < offset+compLen {
		return "", false
	}
	offset += compLen

	// Extensions
	if len(payload) < offset+2 {
		return "", false
	}
	extTotal := int(binary.BigEndian.Uint16(payload[offset:]))
	offset += 2
	end := offset + extTotal
	if len(payload) < end {
		return "", false
	}

	for offset+4 <= end {
		extType := binary.BigEndian.Uint16(payload[offset:])
		extLen := int(binary.BigEndian.Uint16(payload[offset+2:]))
		offset += 4
		if offset+extLen > end {
			break
		}
		if extType == 0x0000 { // SNI extension
			// SNI list length(2) + entry type(1) + name length(2) + name
			if extLen < 5 {
				break
			}
			nameLen := int(binary.BigEndian.Uint16(payload[offset+3:]))
			if offset+5+nameLen > end {
				break
			}
			return string(payload[offset+5 : offset+5+nameLen]), true
		}
		offset += extLen
	}
	return "", false
}
