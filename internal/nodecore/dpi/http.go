package dpi

import (
	"bytes"
	"net"
	"strings"
)

// parseHTTPHost 从 HTTP 请求中提取 Host 头字段值（不含端口）。
// 返回 (host, ok)；ok=false 表示未找到 Host 头。
// 若 Host 值包含端口（如 "example.com:8080"），自动剥离端口部分。
func parseHTTPHost(payload []byte) (string, bool) {
	// 按行扫描，跳过请求行，查找 "Host:" 头。
	// 标准化行终止符：先将 \r\n 统一为 \n，再按 \n 拆分，
	// 兼容 bare LF 的非标 HTTP 实现。
	normalized := bytes.ReplaceAll(payload, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))
	for i, line := range lines {
		if i == 0 {
			continue // 跳过请求行
		}
		lower := bytes.ToLower(line)
		if bytes.HasPrefix(lower, []byte("host:")) {
			val := string(bytes.TrimSpace(line[5:]))
			// 剥离端口部分。IPv6 字面量形如 "[::1]:8080"，需特殊处理。
			if strings.HasPrefix(val, "[") {
				// IPv6 字面量：用 net.SplitHostPort 正确分离 host 和 port
				if host, _, err := net.SplitHostPort(val); err == nil {
					return host, true
				}
				// 无端口的纯 IPv6 字面量（如 "[::1]"），剥离方括号
				return strings.Trim(val, "[]"), true
			}
			// IPv4 或域名：简单按 ":" 分割
			if host, _, ok := strings.Cut(val, ":"); ok {
				return host, true
			}
			return val, true
		}
	}
	return "", false
}
