// portmap_live_test.go 实测：尝试在当前网络环境下通过 UPnP/NAT-PMP/PCP 获取入口信息。
//
// 运行：go test ./internal/nodecore/portmap/ -run TestLive -v -count=1
// 此测试依赖真实网络和路由器，CI 环境下可能全部跳过（无可用网关/IGD）。
package portmap

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// TestLiveGatewayDiscovery 实测默认网关推断。
func TestLiveGatewayDiscovery(t *testing.T) {
	gws := DefaultGateways(nil)
	t.Logf("推断的候选网关: %v", gws)
	if len(gws) == 0 {
		t.Skip("无私网网卡 IP，跳过")
	}
}

// TestLiveNATPMP 实测 NAT-PMP 映射（端口 18000 UDP）。
func TestLiveNATPMP(t *testing.T) {
	gws := DefaultGateways(nil)
	if len(gws) == 0 {
		t.Skip("无候选网关")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, gw := range gws {
		t.Logf("尝试 NAT-PMP → %s", gw)
		m, err := natpmpMap(ctx, gw, 18000, 18000, true, 60)
		if err != nil {
			t.Logf("  失败: %v", err)
			continue
		}
		t.Logf("  ✅ 成功! external=%s:%d, TTL=%s", m.ExternalIP, m.ExternalPort, m.TTL)
		// 清理
		if err := natpmpUnmap(ctx, gw, m); err != nil {
			t.Logf("  unmap: %v", err)
		}
		return
	}
	t.Log("所有网关 NAT-PMP 均失败（路由器可能不支持）")
}

// TestLivePCP 实测 PCP 映射（端口 18001 UDP）。
func TestLivePCP(t *testing.T) {
	gws := DefaultGateways(nil)
	if len(gws) == 0 {
		t.Skip("无候选网关")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, gw := range gws {
		t.Logf("尝试 PCP → %s", gw)
		m, err := pcpMap(ctx, gw, 18001, 18001, true, 60)
		if err != nil {
			t.Logf("  失败: %v", err)
			continue
		}
		t.Logf("  ✅ 成功! external=%s:%d, TTL=%s", m.ExternalIP, m.ExternalPort, m.TTL)
		if err := pcpUnmap(ctx, m); err != nil {
			t.Logf("  unmap: %v", err)
		}
		return
	}
	t.Log("所有网关 PCP 均失败（路由器可能不支持）")
}

// TestLiveUPnP 实测 UPnP-IGD SSDP 发现 + SOAP 映射（端口 18002 TCP）。
func TestLiveUPnP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	t.Log("SSDP 发现 IGD 设备...")
	locations, err := ssdpDiscover(ctx, 3*time.Second)
	if err != nil {
		t.Logf("SSDP 发现失败: %v", err)
	}
	t.Logf("发现 %d 个 IGD location: %v", len(locations), locations)
	if len(locations) == 0 {
		t.Log("无 IGD 设备（路由器可能不支持 UPnP）")
		return
	}

	for _, loc := range locations {
		t.Logf("获取 control URL: %s", loc)
		controlURL, serviceType, err := fetchControlURL(ctx, loc, nil)
		if err != nil {
			t.Logf("  fetch 失败: %v", err)
			continue
		}
		t.Logf("  controlURL=%s, serviceType=%s", controlURL, serviceType)

		// 获取本机出口 IP
		gws := DefaultGateways(nil)
		ic, _ := localOutboundIP(gws)
		if ic == "" {
			ic = "0.0.0.0"
		}
		t.Logf("  internalClient=%s", ic)

		m, err := igdMap(ctx, controlURL, serviceType, 18002, 18002, ic, false, 60, nil)
		if err != nil {
			t.Logf("  igdMap 失败: %v", err)
			continue
		}
		t.Logf("  ✅ 成功! external=%s:%d, TTL=%s, gateway=%s",
			m.ExternalIP, m.ExternalPort, m.TTL, m.Gateway)
		if err := igdUnmap(ctx, m, nil); err != nil {
			t.Logf("  unmap: %v", err)
		}
		return
	}
	t.Log("所有 IGD location SOAP 均失败")
}

// TestLiveDefaultMapper 实测 DefaultMapper 竞速（三协议并发，端口 18003）。
func TestLiveDefaultMapper(t *testing.T) {
	mapper := New(Config{
		DialTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Log("DefaultMapper.Map 竞速（NAT-PMP + PCP + UPnP 并发）...")
	m, err := mapper.Map(ctx, 18003, true, 120*time.Second)
	if err != nil {
		t.Logf("竞速全失败: %v", err)
		t.Log("（当前网络环境下路由器可能不支持任何端口映射协议）")
		return
	}

	t.Logf("✅ 竞速获胜协议: %s", m.Protocol)
	t.Logf("   外部地址: %s:%d", m.ExternalIP, m.ExternalPort)
	t.Logf("   内部端口: %d", m.InternalPort)
	t.Logf("   传输协议: %s", func() string {
		if m.TransportUDP {
			return "UDP"
		}
		return "TCP"
	}())
	t.Logf("   TTL:       %s", m.TTL)
	t.Logf("   网关:      %s", m.Gateway)

	// 清理
	if err := mapper.Unmap(ctx, m); err != nil {
		t.Logf("   unmap: %v", err)
	}
}

// TestLiveReachability 实测端口映射后的可达性验证：本地监听 → portmap → 外部连回来。
func TestLiveReachability(t *testing.T) {
	mapper := New(Config{DialTimeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. 本地监听 TCP
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("本地监听失败: %v", err)
	}
	defer ln.Close()
	localPort := uint16(ln.Addr().(*net.TCPAddr).Port)
	t.Logf("本地 TCP 监听: :%d", localPort)

	// echo server：接受连接后回写收到的数据
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				c.Write(buf[:n])
			}(conn)
		}
	}()

	// 2. portmap 映射该端口
	t.Log("portmap 映射中...")
	m, err := mapper.Map(ctx, localPort, false, 120*time.Second)
	if err != nil {
		t.Skipf("portmap 映射失败（路由器不支持）: %v", err)
	}
	t.Logf("映射成功: %s:%d → 本地:%d (协议=%s)", m.ExternalIP, m.ExternalPort, m.InternalPort, m.Protocol)
	defer mapper.Unmap(ctx, m)

	// 3. 通过公网地址连回来
	extAddr := fmt.Sprintf("%s:%d", m.ExternalIP, m.ExternalPort)
	t.Logf("尝试连接公网地址: %s", extAddr)

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()
	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", extAddr)
	if err != nil {
		t.Logf("❌ 连接公网地址失败: %v", err)
		t.Log("   可能原因: 路由器不支持 NAT 回环(hairpinning) / CGNAT / 防火墙")
		t.Log("   尝试通过外部 HTTP 探测验证...")

		// 4. 备选：用本地直连验证 echo server 本身是正常的
		localConn, localErr := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
		if localErr != nil {
			t.Fatalf("本地 echo server 也连不上: %v", localErr)
		}
		probe := []byte("corelink-probe")
		localConn.Write(probe)
		localConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 64)
		n, readErr := localConn.Read(buf)
		localConn.Close()
		if readErr != nil || string(buf[:n]) != string(probe) {
			t.Fatalf("本地 echo server 响应异常")
		}
		t.Log("   ✅ 本地 echo server 正常，映射可能有效但本机无法 hairpin 验证")
		t.Log("   从外部机器执行: nc " + extAddr + " 可验证")
		return
	}
	defer conn.Close()

	// 5. TCP 连接建立即证明端口映射有效（外部流量可到达本机监听端口）
	t.Logf("✅ TCP 连接建立成功！端口映射 %s 可达", extAddr)

	// 尝试 echo 验证（部分路由器 NAT hairpinning 回包路由不对称会导致超时）
	probe := []byte("corelink-portmap-reachability-test")
	conn.Write(probe)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Logf("   echo 回包超时（NAT hairpinning 回包路由不对称，预期行为）: %v", err)
		t.Log("   TCP 握手成功已证明端口映射有效，外部设备可正常连入")
	} else if string(buf[:n]) == string(probe) {
		t.Logf("   ✅ echo 回包也成功！完整双向验证通过")
	}
}

// TestLive10RandomPorts 实测：尝试映射 10 个随机端口（外部端口由路由器分配）。
func TestLive10RandomPorts(t *testing.T) {
	mapper := New(Config{DialTimeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	type mapped struct {
		m  *Mapping
		ln net.Listener
	}
	var results []mapped

	t.Log("=== 尝试映射 10 个随机端口 ===")
	for i := range 10 {
		// 本地监听随机端口
		ln, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Logf("[%d] 本地监听失败: %v", i, err)
			continue
		}
		localPort := uint16(ln.Addr().(*net.TCPAddr).Port)

		m, err := mapper.Map(ctx, localPort, false, 120*time.Second)
		if err != nil {
			ln.Close()
			t.Logf("[%d] :%d → 映射失败: %v", i, localPort, err)
			continue
		}
		t.Logf("[%d] ✅ :%d → %s:%d (协议=%s, 外部端口%s)",
			i, localPort, m.ExternalIP, m.ExternalPort, m.Protocol,
			func() string {
				if m.ExternalPort == localPort {
					return "=内部"
				}
				return "≠内部(路由器分配)"
			}())
		results = append(results, mapped{m: m, ln: ln})
	}

	t.Logf("\n成功映射 %d/10 个端口", len(results))

	// 清理
	for _, r := range results {
		r.ln.Close()
		mapper.Unmap(ctx, r.m)
	}
}

// TestLiveFullIngress 实测完整入口发现流程（模拟 corelink-node 启动时的 portmap 路径）。
func TestLiveFullIngress(t *testing.T) {
	mapper := New(Config{DialTimeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Log("=== 完整入口发现 ===")

	// 1. 网关推断
	gws := DefaultGateways(nil)
	t.Logf("候选网关: %v", gws)

	// 2. 出口 IP
	outIP, err := localOutboundIP(gws)
	if err != nil {
		t.Logf("出口 IP: 未知 (%v)", err)
	} else {
		t.Logf("出口 IP: %s", outIP)
	}

	// 3. UDP 映射（模拟 WireGuard 端口）
	t.Log("\n--- UDP 映射（WireGuard）---")
	udpM, udpErr := mapper.Map(ctx, 18010, true, 120*time.Second)
	if udpErr != nil {
		t.Logf("UDP 映射失败: %v", udpErr)
	} else {
		t.Logf("UDP ✅ %s → %s:%d (协议=%s, TTL=%s)",
			fmt.Sprintf(":%d", udpM.InternalPort), udpM.ExternalIP, udpM.ExternalPort,
			udpM.Protocol, udpM.TTL)
	}

	// 4. TCP 映射（模拟隧道端口）
	t.Log("\n--- TCP 映射（隧道）---")
	tcpM, tcpErr := mapper.Map(ctx, 18011, false, 120*time.Second)
	if tcpErr != nil {
		t.Logf("TCP 映射失败: %v", tcpErr)
	} else {
		t.Logf("TCP ✅ %s → %s:%d (协议=%s, TTL=%s)",
			fmt.Sprintf(":%d", tcpM.InternalPort), tcpM.ExternalIP, tcpM.ExternalPort,
			tcpM.Protocol, tcpM.TTL)
	}

	// 5. 汇总
	t.Log("\n=== 汇总 ===")
	if udpErr != nil && tcpErr != nil {
		t.Log("无可用端口映射（路由器不支持 UPnP/NAT-PMP/PCP 或不在 NAT 后）")
	} else {
		if udpM != nil {
			t.Logf("UPnP 入口 (UDP): %s:%d", udpM.ExternalIP, udpM.ExternalPort)
		}
		if tcpM != nil {
			t.Logf("UPnP 入口 (TCP): %s:%d", tcpM.ExternalIP, tcpM.ExternalPort)
		}
	}

	// 清理
	if udpM != nil {
		mapper.Unmap(ctx, udpM)
	}
	if tcpM != nil {
		mapper.Unmap(ctx, tcpM)
	}
}
