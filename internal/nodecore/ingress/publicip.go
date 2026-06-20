package ingress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
)

// DefaultPublicIPURLs 默认的公网 IP 查询 URL 列表（响应体即纯 IP 文本）。
// 供调用方使用；QueryPublicIP 本身不内置，URL 由调用方显式传入。
var DefaultPublicIPURLs = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

// publicIPMaxBody 读取响应体的上限，防止异常超大响应。
const publicIPMaxBody = 256

// QueryPublicIP 逐个 GET urls，将响应体 trim 后解析为 IP，返回首个成功的公网 IP 字符串。
//
// 容错：单个 URL 请求失败或返回非法/非可用内容时跳过，继续尝试下一个。
// 全部失败或 urls 为空时返回错误。httpClient 为 nil 时回退 http.DefaultClient。
//
// 本函数仅返回 IP 字符串；转为 genv1.Ingress（source=URL, confidence 低）由 Task 1.4
// 合并阶段处理。
func QueryPublicIP(ctx context.Context, httpClient *http.Client, urls []string) (string, error) {
	if len(urls) == 0 {
		return "", errors.New("ingress: 公网 IP 查询 URL 列表为空")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	var lastErr error
	for _, u := range urls {
		ip, err := fetchPublicIP(ctx, httpClient, u)
		if err != nil {
			lastErr = err
			continue
		}
		return ip, nil
	}
	if lastErr == nil {
		lastErr = errors.New("ingress: 所有公网 IP 查询均失败")
	}
	return "", fmt.Errorf("ingress: 公网 IP 查询失败: %w", lastErr)
}

// fetchPublicIP 请求单个 URL 并解析响应体为公网 IP。
func fetchPublicIP(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ingress: %s 返回状态码 %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, publicIPMaxBody))
	if err != nil {
		return "", err
	}

	text := strings.TrimSpace(string(body))
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return "", fmt.Errorf("ingress: %s 响应非合法 IP %q: %w", url, text, err)
	}
	addr = addr.Unmap()
	// 出口地址应为公网；私有/回环/链路本地说明拿到的不是真实出口 IP。
	if !isUsablePublicIP(addr) {
		return "", fmt.Errorf("ingress: %s 返回非公网 IP %s", url, addr)
	}
	return addr.String(), nil
}

// isUsablePublicIP 判定地址是否为可用公网 IP。
//
// 委托共享的 isPubliclyRoutable：除私有/回环/链路本地/multicast/未指定外，
// 还排除 CGNAT(100.64/10)、Benchmark(198.18/15)、保留(240/4) 等保留段（bug #8）。
func isUsablePublicIP(addr netip.Addr) bool {
	return isPubliclyRoutable(addr)
}
