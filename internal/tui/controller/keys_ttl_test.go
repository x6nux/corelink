package controller

import (
	"testing"
)

// TestCreateKeyTTLOverflow 验证超长 TTL 数字串不再静默溢出 int64，而是报错且不发 RPC。
func TestCreateKeyTTLOverflow(t *testing.T) {
	tab := &KeysTab{}
	// 21 位数字串，远超 int64 上限（9223372036854775807，19 位）。
	tab.form.ttl = "99999999999999999999"

	cmd := tab.createKey()

	if cmd != nil {
		t.Fatalf("TTL 解析失败时不应发起 RPC（应返回 nil cmd），got non-nil cmd")
	}
	if tab.err == nil {
		t.Fatalf("超长 TTL 应设置 t.err 提示用户，got nil")
	}
}

// TestCreateKeyTTLEmpty 验证空 TTL 仍被接受（视为不过期，ttl_seconds=0）。
func TestCreateKeyTTLEmpty(t *testing.T) {
	tab := &KeysTab{}
	tab.form.ttl = ""

	// client 为 nil：空 TTL 解析成功后走到 client 检查处返回 nil，且不得设置 err。
	cmd := tab.createKey()

	if cmd != nil {
		t.Fatalf("client 为 nil 时应返回 nil cmd，got non-nil")
	}
	if tab.err != nil {
		t.Fatalf("空 TTL 不应视为错误，got err=%v", tab.err)
	}
}

// TestCreateKeyTTLInvalidRejected 验证非法字符/负值类输入被拒（防御性，输入侧应只放行数字）。
func TestCreateKeyTTLInvalidRejected(t *testing.T) {
	tab := &KeysTab{}
	tab.form.ttl = "12x3" // 含非数字字符

	cmd := tab.createKey()

	if cmd != nil {
		t.Fatalf("非法 TTL 不应发起 RPC，got non-nil cmd")
	}
	if tab.err == nil {
		t.Fatalf("非法 TTL 应设置 t.err，got nil")
	}
}
