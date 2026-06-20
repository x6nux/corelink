// orchestrate_test.go 覆盖 orchestrate.go 中可测纯函数的单元测试。
package main

import (
	"testing"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// TestPeerFingerprintsFromAssignment 验证 peerFingerprintsFromAssignment 的转换逻辑：
//   - 有效指纹应进表；
//   - 空指纹应跳过；
//   - nil assignment 应返回空 map（非 nil）。
func TestPeerFingerprintsFromAssignment(t *testing.T) {
	asg := &genv1.TopologyAssignment{Neighbors: []*genv1.NeighborRef{
		{NodeId: "nodeB", Fingerprint: "fpB"},
		{NodeId: "nodeC", Fingerprint: ""}, // 空指纹跳过
	}}
	fps := peerFingerprintsFromAssignment(asg)
	if fps["nodeB"] != "fpB" {
		t.Fatalf("nodeB=%q", fps["nodeB"])
	}
	if _, ok := fps["nodeC"]; ok {
		t.Fatal("空指纹不应进表")
	}

	// nil assignment 返回空 map（非 nil，调用方无需判空）
	empty := peerFingerprintsFromAssignment(nil)
	if empty == nil {
		t.Fatal("nil assignment 应返回空 map，非 nil")
	}
}
