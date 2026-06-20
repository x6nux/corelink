package splittunnel

import (
	"bytes"
	"strings"
)

// SniffTLS 解析 TLS ClientHello，提取 SNI（Server Name Indication）。
// 若非 TLS 或无 SNI 扩展则返回空字符串。
// 预留给未来基于域名的分流策略使用。
func SniffTLS(payload []byte) string {
	// 最短合法 TLS record: type(1) + version(2) + length(2) = 5
	if len(payload) < 5 {
		return ""
	}

	// 检查 ContentType = Handshake (0x16)
	if payload[0] != 0x16 {
		return ""
	}

	// 检查版本 >= TLS 1.0 (0x0301)
	major, minor := payload[1], payload[2]
	if major != 0x03 || minor < 0x01 {
		return ""
	}

	// TLS record 长度
	recordLen := int(payload[3])<<8 | int(payload[4])
	if len(payload) < 5+recordLen {
		return ""
	}

	// Handshake 层：type(1) + length(3) + ...
	hs := payload[5 : 5+recordLen]
	if len(hs) < 4 {
		return ""
	}

	// Handshake type = ClientHello (0x01)
	if hs[0] != 0x01 {
		return ""
	}

	hsLen := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
	if len(hs) < 4+hsLen {
		return ""
	}
	hs = hs[4 : 4+hsLen]

	// ClientHello: version(2) + random(32) = 34 字节
	if len(hs) < 34 {
		return ""
	}
	pos := 34

	// Session ID: length(1) + data
	if pos >= len(hs) {
		return ""
	}
	sessionIDLen := int(hs[pos])
	pos++
	pos += sessionIDLen
	if pos > len(hs) {
		return ""
	}

	// Cipher Suites: length(2) + data
	if pos+2 > len(hs) {
		return ""
	}
	cipherLen := int(hs[pos])<<8 | int(hs[pos+1])
	pos += 2
	pos += cipherLen
	if pos > len(hs) {
		return ""
	}

	// Compression Methods: length(1) + data
	if pos >= len(hs) {
		return ""
	}
	compLen := int(hs[pos])
	pos++
	pos += compLen
	if pos > len(hs) {
		return ""
	}

	// Extensions: length(2) + data
	if pos+2 > len(hs) {
		return ""
	}
	extLen := int(hs[pos])<<8 | int(hs[pos+1])
	pos += 2
	if pos+extLen > len(hs) {
		return ""
	}
	extData := hs[pos : pos+extLen]

	return parseSNIExtension(extData)
}

// parseSNIExtension 从 TLS 扩展数据中查找 server_name 扩展（type 0x0000）并提取域名。
func parseSNIExtension(data []byte) string {
	pos := 0
	for pos+4 <= len(data) {
		extType := int(data[pos])<<8 | int(data[pos+1])
		extLen := int(data[pos+2])<<8 | int(data[pos+3])
		pos += 4
		if pos+extLen > len(data) {
			return ""
		}

		if extType == 0x0000 {
			// server_name 扩展
			return parseServerNameList(data[pos : pos+extLen])
		}
		pos += extLen
	}
	return ""
}

// parseServerNameList 解析 ServerNameList，提取 host_name（type 0）。
func parseServerNameList(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	listLen := int(data[0])<<8 | int(data[1])
	if len(data) < 2+listLen {
		return ""
	}
	list := data[2 : 2+listLen]

	pos := 0
	for pos+3 <= len(list) {
		nameType := list[pos]
		nameLen := int(list[pos+1])<<8 | int(list[pos+2])
		pos += 3
		if pos+nameLen > len(list) {
			return ""
		}
		if nameType == 0 {
			// host_name
			return string(list[pos : pos+nameLen])
		}
		pos += nameLen
	}
	return ""
}

// httpMethods 是 HTTP 请求方法列表，用于快速判断是否为 HTTP 请求。
var httpMethods = [][]byte{
	[]byte("GET "),
	[]byte("POST "),
	[]byte("PUT "),
	[]byte("DELETE "),
	[]byte("HEAD "),
	[]byte("PATCH "),
	[]byte("OPTIONS "),
	[]byte("CONNECT "),
}

// SniffHTTP 解析 HTTP 请求，提取 Host 头。
// 若非 HTTP 请求或无 Host 头则返回空字符串。
// 预留给未来基于域名的分流策略使用。
func SniffHTTP(payload []byte) string {
	// 检查是否以 HTTP 方法开头
	isHTTP := false
	for _, method := range httpMethods {
		if bytes.HasPrefix(payload, method) {
			isHTTP = true
			break
		}
	}
	if !isHTTP {
		return ""
	}

	// 逐行扫描查找 Host 头
	// HTTP 头部以 \r\n 分隔，头部结束标记为 \r\n\r\n
	lines := bytes.Split(payload, []byte("\r\n"))
	for _, line := range lines[1:] { // 跳过请求行
		if len(line) == 0 {
			// 空行表示头部结束
			break
		}
		// 查找 "Host:" 前缀（大小写不敏感）
		if len(line) > 5 && strings.EqualFold(string(line[:5]), "Host:") {
			return strings.TrimSpace(string(line[5:]))
		}
	}
	return ""
}
