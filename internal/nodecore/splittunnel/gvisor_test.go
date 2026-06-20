package splittunnel

import (
	"testing"
)

func TestGVisorStackCreate(t *testing.T) {
	gs, err := NewGVisorStack("lo0", nil)
	if err != nil {
		t.Fatalf("NewGVisorStack: %v", err)
	}
	defer gs.Close()
	t.Log("gVisor 协议栈创建成功")
}

func TestGVisorStackInjectShortPacket(t *testing.T) {
	gs, err := NewGVisorStack("lo0", nil)
	if err != nil {
		t.Fatalf("NewGVisorStack: %v", err)
	}
	defer gs.Close()

	// 短包应被安全忽略
	gs.InjectPacket(nil)
	gs.InjectPacket([]byte{0x45, 0x00})
	t.Log("短包注入安全通过")
}

func TestGVisorStackHandleICMPNilTUN(t *testing.T) {
	gs, err := NewGVisorStack("lo0", nil)
	if err != nil {
		t.Fatalf("NewGVisorStack: %v", err)
	}
	defer gs.Close()

	// tunDev 为 nil 时 HandleICMP 不应 panic
	gs.HandleICMP(nil)
	gs.HandleICMP(make([]byte, 10))
	t.Log("ICMP nil TUN 安全通过")
}

func TestGVisorStackDoubleClose(t *testing.T) {
	gs, err := NewGVisorStack("lo0", nil)
	if err != nil {
		t.Fatalf("NewGVisorStack: %v", err)
	}

	// 双重 Close 不应 panic
	gs.Close()
	gs.Close()
	t.Log("双重 Close 安全通过")
}

func TestBuildICMPReplyPacket(t *testing.T) {
	// 构造一个最小的 IP 包头（20 字节）
	origPkt := make([]byte, 28) // IP(20) + ICMP(8)
	origPkt[0] = 0x45           // version=4, IHL=5
	// 源 IP: 10.0.0.1
	origPkt[12] = 10
	origPkt[13] = 0
	origPkt[14] = 0
	origPkt[15] = 1
	// 目标 IP: 8.8.8.8
	origPkt[16] = 8
	origPkt[17] = 8
	origPkt[18] = 8
	origPkt[19] = 8

	icmpReply := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01} // Echo Reply

	reply, err := buildICMPReplyPacket(origPkt, icmpReply)
	if err != nil {
		t.Fatalf("buildICMPReplyPacket: %v", err)
	}

	// 验证回复包的方向：源 IP 应为 8.8.8.8，目标 IP 应为 10.0.0.1
	if reply[12] != 8 || reply[13] != 8 || reply[14] != 8 || reply[15] != 8 {
		t.Errorf("回复包源 IP 错误: %d.%d.%d.%d", reply[12], reply[13], reply[14], reply[15])
	}
	if reply[16] != 10 || reply[17] != 0 || reply[18] != 0 || reply[19] != 1 {
		t.Errorf("回复包目标 IP 错误: %d.%d.%d.%d", reply[16], reply[17], reply[18], reply[19])
	}

	// 验证 IP 头校验和不为零
	cs := uint16(reply[10])<<8 | uint16(reply[11])
	if cs == 0 {
		t.Error("IP 头校验和不应为零")
	}
	t.Logf("回复包长度=%d, 校验和=0x%04x", len(reply), cs)
}
