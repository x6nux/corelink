package configsvc

import (
	"testing"
)

// 测试 NodeInfo 结构体与 NewConfigGRPC 构造函数。

func TestNodeInfoFields(t *testing.T) {
	// 验证 NodeInfo 字段赋值与读取。
	info := &NodeInfo{Generation: 42}
	if info.Generation != 42 {
		t.Errorf("Generation = %d, 期望 42", info.Generation)
	}
}

func TestNewConfigGRPCEpochNil(t *testing.T) {
	// NewConfigGRPC 构造的实例 epoch 为 nil（向后兼容）。
	bumper := newStubBumper()
	n := NewNotify(bumper)
	defer n.Close()
	getter := newStubNodeInfoGetter()
	g := NewConfigGRPC(n, getter)
	if g.epoch != nil {
		t.Error("NewConfigGRPC 的 epoch 应为 nil")
	}
	// loadEpoch 返回 0。
	if g.loadEpoch() != 0 {
		t.Errorf("loadEpoch = %d, 期望 0", g.loadEpoch())
	}
}

func TestNewConfigGRPCNotNil(t *testing.T) {
	// 验证构造函数返回非 nil。
	g := NewConfigGRPC(nil, nil)
	if g == nil {
		t.Fatal("NewConfigGRPC 不应返回 nil")
	}
}

func TestNodeIDFromGRPCPeerNoContext(t *testing.T) {
	// 无 peer 信息的 context 应返回错误。
	_, err := NodeIDFromGRPCPeer(t.Context())
	if err == nil {
		t.Fatal("无 peer 信息应返回错误")
	}
}
