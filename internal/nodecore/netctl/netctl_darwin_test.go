//go:build darwin

package netctl

import (
	"testing"
)

func TestDarwinDefaultInterface(t *testing.T) {
	det := NewInterfaceDetector()
	name, gw, err := det.DefaultInterface()
	if err != nil {
		t.Skipf("无默认路由: %v", err)
	}
	if name == "" {
		t.Error("接口名为空")
	}
	if !gw.IsValid() {
		t.Error("网关无效")
	}
	t.Logf("默认接口: %s, 网关: %v", name, gw)
}

func TestDarwinLocalSubnet(t *testing.T) {
	det := NewInterfaceDetector()
	subnet, err := det.LocalSubnet()
	if err != nil {
		t.Skipf("无法获取本地子网: %v", err)
	}
	t.Logf("本地子网: %v", subnet)
}

func TestDarwinSystemDNSServers(t *testing.T) {
	det := NewInterfaceDetector()
	servers, err := det.SystemDNSServers()
	if err != nil {
		t.Skipf("无法获取 DNS 服务器: %v", err)
	}
	if len(servers) == 0 {
		t.Log("未发现 DNS 服务器（正常于某些环境）")
	}
	t.Logf("DNS 服务器: %v", servers)
}

func TestDarwinFlushCache(t *testing.T) {
	dm := NewDNSManager()
	// FlushCache 需要 dscacheutil 命令，CI 可能没有
	err := dm.FlushCache()
	if err != nil {
		t.Skipf("FlushCache 失败（可能无 dscacheutil）: %v", err)
	}
}

func TestDarwinParseScutilDNS(t *testing.T) {
	output := `
DNS configuration

resolver #1
  nameserver[0] : 8.8.8.8
  nameserver[1] : 8.8.4.4

resolver #2
  nameserver[0] : 192.168.1.1
`
	servers := parseScutilDNS(output)
	if len(servers) != 3 {
		t.Fatalf("期望 3 个 DNS 服务器, 得到 %d", len(servers))
	}
	t.Logf("解析结果: %v", servers)
}
