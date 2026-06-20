//go:build integration

package tunnel

import (
	"context"
	"os"
	"testing"
	"time"
)

// 需本机或环境提供 SOCKS5：CORELINK_TEST_SOCKS5="127.0.0.1:1080"
func TestDialThroughSocks5(t *testing.T) {
	socks := os.Getenv("CORELINK_TEST_SOCKS5")
	if socks == "" {
		t.Skip("未设置 CORELINK_TEST_SOCKS5")
	}
	d, err := NewDialer(&Config{Protocol: TCP, Proxy: &ProxyOptions{URL: "socks5://" + socks}})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := d.Dial(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("经代理拨号失败: %v", err)
	}
	c.Close()
}
