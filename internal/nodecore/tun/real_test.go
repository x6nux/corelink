package tun

import (
	"testing"
)

// TestRealTUN_接口合规 验证 realTUN 编译期满足 Device 接口。
func TestRealTUN_接口合规(t *testing.T) {
	// 编译期断言（与 real.go 中的 var _ Device = (*realTUN)(nil) 一致）
	var _ Device = (*realTUN)(nil)
}

// TestCreateReal_无特权应失败 验证在非特权环境下 CreateReal 返回错误（无法创建真实 TUN）。
func TestCreateReal_无特权应失败(t *testing.T) {
	_, err := CreateReal("utun-test-nonpriv", 1420)
	if err == nil {
		t.Fatal("非特权环境下 CreateReal 应返回错误")
	}
}
