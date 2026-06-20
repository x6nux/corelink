//go:build darwin

package splittunnel

import "testing"

func TestDarwinDetectPhysicalInterface(t *testing.T) {
	ifce := DetectPhysicalInterface()
	if ifce == "" {
		t.Skip("未检测到物理接口")
	}
	t.Logf("物理接口: %s", ifce)
}

func TestDarwinDetectGateway(t *testing.T) {
	gw := DetectGateway()
	if gw == "" {
		t.Skip("未检测到默认网关")
	}
	t.Logf("默认网关: %s", gw)
}

func TestDarwinDetectLocalSubnet(t *testing.T) {
	ifce := DetectPhysicalInterface()
	if ifce == "" {
		t.Skip("未检测到物理接口")
	}
	subnet := DetectLocalSubnet(ifce)
	if subnet == "" {
		t.Errorf("未检测到本地子网")
	}
	t.Logf("本地子网 (%s): %s", ifce, subnet)
}
