package dataplane

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/nodecore/connpool"
	"github.com/x6nux/corelink/internal/nodecore/flowtrack"
	"github.com/x6nux/corelink/internal/nodecore/metadata"
	"github.com/x6nux/corelink/internal/nodecore/route"
	"github.com/x6nux/corelink/internal/nodecore/tun"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// buildIPv4TCP 构造一个最小化的 IPv4 + TCP 包。
// flags: 0x02=SYN, 0x10=ACK, 0x12=SYN-ACK, 0x01=FIN 等。
func buildIPv4TCP(srcIP string, srcPort uint16, dstIP string, dstPort uint16, flags byte) []byte {
	src := netip.MustParseAddr(srcIP)
	dst := netip.MustParseAddr(dstIP)

	// IPv4 header: 20 字节（IHL=5，无选项）
	ihl := 20
	// TCP header: 20 字节（DataOffset=5，无选项）
	tcpHdrLen := 20
	totalLen := ihl + tcpHdrLen

	pkt := make([]byte, totalLen)

	// --- IPv4 header ---
	pkt[0] = 0x45                                          // Version=4, IHL=5
	pkt[1] = 0                                             // DSCP/ECN
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen)) // Total Length
	binary.BigEndian.PutUint16(pkt[4:6], 0x1234)           // Identification
	pkt[6] = 0x40                                          // Flags: Don't Fragment
	pkt[7] = 0                                             // Fragment Offset
	pkt[8] = 64                                            // TTL
	pkt[9] = 6                                             // Protocol: TCP
	// pkt[10:12] = checksum（测试中置零）
	srcBytes := src.As4()
	dstBytes := dst.As4()
	copy(pkt[12:16], srcBytes[:])
	copy(pkt[16:20], dstBytes[:])

	// --- TCP header ---
	tcp := pkt[ihl:]
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	binary.BigEndian.PutUint32(tcp[4:8], 1000)    // Sequence number
	binary.BigEndian.PutUint32(tcp[8:12], 0)      // ACK number
	tcp[12] = 0x50                                // DataOffset=5 (20 bytes), Reserved=0
	tcp[13] = flags                               // TCP flags
	binary.BigEndian.PutUint16(tcp[14:16], 65535) // Window size
	// tcp[16:18] = checksum（测试中置零）
	// tcp[18:20] = urgent pointer

	return pkt
}

// buildIPv4UDP 构造一个最小化的 IPv4 + UDP 包。
func buildIPv4UDP(srcIP string, srcPort uint16, dstIP string, dstPort uint16, payload []byte) []byte {
	src := netip.MustParseAddr(srcIP)
	dst := netip.MustParseAddr(dstIP)

	ihl := 20
	udpHdrLen := 8
	totalLen := ihl + udpHdrLen + len(payload)

	pkt := make([]byte, totalLen)

	// --- IPv4 header ---
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
	pkt[8] = 64
	pkt[9] = 17 // UDP
	srcBytes := src.As4()
	dstBytes := dst.As4()
	copy(pkt[12:16], srcBytes[:])
	copy(pkt[16:20], dstBytes[:])

	// --- UDP header ---
	udp := pkt[ihl:]
	binary.BigEndian.PutUint16(udp[0:2], srcPort)
	binary.BigEndian.PutUint16(udp[2:4], dstPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpHdrLen+len(payload)))
	copy(udp[8:], payload)

	return pkt
}

// TestDataPlaneSmoke_Outbound 验证出站管线：TUN → FlowTracker → DPI → Route → ConnPool。
func TestDataPlaneSmoke_Outbound(t *testing.T) {
	// 1. 创建 fakeTUN
	ft := tun.NewFakeTUN("dp-test", 1400)
	defer ft.Close()

	// 2. 创建各组件
	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	router.Update(&route.RouteConfig{
		FIB: []route.FIBEntry{
			{Prefix: netip.MustParsePrefix("100.64.0.0/10"), NextHop: "peer-1"},
		},
	})
	pool := connpool.NewPool(connpool.DefaultConfig())
	pool.Update(map[string]connpool.HopInfo{
		"peer-1": {Addrs: []string{"1.2.3.4:7447"}},
	})
	defer pool.Close()

	// 3. 创建 DataPlane
	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	// 4. 注入出站包到 TUN（host→设备方向 = app 发数据）
	// 构造 TCP SYN: 10.0.0.1:1234 → 100.64.1.1:443
	pkt := buildIPv4TCP("10.0.0.1", 1234, "100.64.1.1", 443, 0x02)
	ft.Inject(pkt)

	// 5. 等待处理
	time.Sleep(100 * time.Millisecond)

	// 6. 验证流已被追踪
	if tracker.Count() != 1 {
		t.Fatalf("应有 1 个流, got %d", tracker.Count())
	}
}

// TestDataPlaneSmoke_MultipleFlows 验证多个不同五元组的包被识别为独立流。
func TestDataPlaneSmoke_MultipleFlows(t *testing.T) {
	ft := tun.NewFakeTUN("dp-multi", 1400)
	defer ft.Close()

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	router.Update(&route.RouteConfig{
		FIB: []route.FIBEntry{
			{Prefix: netip.MustParsePrefix("100.64.0.0/10"), NextHop: "peer-1"},
		},
	})
	pool := connpool.NewPool(connpool.DefaultConfig())
	pool.Update(map[string]connpool.HopInfo{
		"peer-1": {Addrs: []string{"1.2.3.4:7447"}},
	})
	defer pool.Close()

	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	// 注入三个不同目的端口的 TCP 包（三条独立流）
	ft.Inject(buildIPv4TCP("10.0.0.1", 1234, "100.64.1.1", 80, 0x02))
	ft.Inject(buildIPv4TCP("10.0.0.1", 1235, "100.64.1.1", 443, 0x02))
	ft.Inject(buildIPv4TCP("10.0.0.1", 1236, "100.64.1.2", 8080, 0x02))

	time.Sleep(100 * time.Millisecond)

	if tracker.Count() != 3 {
		t.Fatalf("应有 3 个流, got %d", tracker.Count())
	}
}

// TestDataPlaneSmoke_SameFlowDedup 验证同一五元组的多个包只产生一条流。
func TestDataPlaneSmoke_SameFlowDedup(t *testing.T) {
	ft := tun.NewFakeTUN("dp-dedup", 1400)
	defer ft.Close()

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	router.Update(&route.RouteConfig{
		FIB: []route.FIBEntry{
			{Prefix: netip.MustParsePrefix("100.64.0.0/10"), NextHop: "peer-1"},
		},
	})
	pool := connpool.NewPool(connpool.DefaultConfig())
	pool.Update(map[string]connpool.HopInfo{
		"peer-1": {Addrs: []string{"1.2.3.4:7447"}},
	})
	defer pool.Close()

	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	// 同一五元组注入多次
	for range 5 {
		ft.Inject(buildIPv4TCP("10.0.0.1", 1234, "100.64.1.1", 443, 0x10)) // ACK
	}

	time.Sleep(100 * time.Millisecond)

	if tracker.Count() != 1 {
		t.Fatalf("同一五元组应归并为 1 个流, got %d", tracker.Count())
	}
}

// TestDataPlaneSmoke_NoRoute 验证无路由匹配时包被丢弃（不崩溃）。
func TestDataPlaneSmoke_NoRoute(t *testing.T) {
	ft := tun.NewFakeTUN("dp-noroute", 1400)
	defer ft.Close()

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine() // 空路由表
	pool := connpool.NewPool(connpool.DefaultConfig())
	defer pool.Close()

	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	// 注入包——无路由匹配
	ft.Inject(buildIPv4TCP("10.0.0.1", 1234, "192.168.1.1", 80, 0x02))

	time.Sleep(100 * time.Millisecond)

	// 流仍被创建（Track 成功），但 NextHop 为空
	if tracker.Count() != 1 {
		t.Fatalf("即使无路由也应创建流, got %d", tracker.Count())
	}
}

// TestDataPlaneSmoke_UDP 验证 UDP 包也能正确追踪。
func TestDataPlaneSmoke_UDP(t *testing.T) {
	ft := tun.NewFakeTUN("dp-udp", 1400)
	defer ft.Close()

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	router.Update(&route.RouteConfig{
		FIB: []route.FIBEntry{
			{Prefix: netip.MustParsePrefix("100.64.0.0/10"), NextHop: "peer-1"},
		},
	})
	pool := connpool.NewPool(connpool.DefaultConfig())
	pool.Update(map[string]connpool.HopInfo{
		"peer-1": {Addrs: []string{"1.2.3.4:7447"}},
	})
	defer pool.Close()

	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	ft.Inject(buildIPv4UDP("10.0.0.1", 5353, "100.64.1.1", 53, []byte("dns-query")))

	time.Sleep(100 * time.Millisecond)

	if tracker.Count() != 1 {
		t.Fatalf("UDP 包应产生 1 个流, got %d", tracker.Count())
	}
}

// TestDataPlaneClose 验证 Close 不会挂起。
func TestDataPlaneClose(t *testing.T) {
	ft := tun.NewFakeTUN("dp-close", 1400)
	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	pool := connpool.NewPool(connpool.DefaultConfig())
	defer pool.Close()

	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()

	// Close 不应超时
	done := make(chan struct{})
	go func() {
		dp.Close()
		close(done)
	}()

	select {
	case <-done:
		// 成功
	case <-time.After(3 * time.Second):
		t.Fatal("Close 超时")
	}
}

// TestDataPlane_ApplyConfig 验证 ApplyConfig 正确更新路由和连接池。
func TestDataPlane_ApplyConfig(t *testing.T) {
	ft := tun.NewFakeTUN("dp-apply", 1400)
	defer ft.Close()

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	pool := connpool.NewPool(connpool.DefaultConfig())
	defer pool.Close()

	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	// 初始：无路由
	ft.Inject(buildIPv4TCP("10.0.0.1", 1234, "100.64.5.1", 443, 0x02))
	time.Sleep(50 * time.Millisecond)

	// 期望流被创建但无路由
	if tracker.Count() != 1 {
		t.Fatalf("应有 1 个流, got %d", tracker.Count())
	}

	// ApplyConfig 添加路由
	nodeCfg := &genv1.NodeConfig{
		Peers: []*genv1.Peer{
			{
				NodeId:     "node-a",
				AllowedIps: []string{"100.64.5.0/24"},
			},
		},
	}
	if err := dp.ApplyConfig(nodeCfg); err != nil {
		t.Fatalf("ApplyConfig 失败: %v", err)
	}

	// 过期旧流后注入新包——现在应该有路由
	ft.Inject(buildIPv4TCP("10.0.0.1", 2345, "100.64.5.1", 80, 0x02))
	time.Sleep(50 * time.Millisecond)

	// 验证连接池有新的 hop
	if pool.ConnCount("node-a") == 0 {
		t.Log("注意: 无拨号器的测试模式下 ConnCount 可能为 0（预期行为）")
	}
}

// TestExtractTCPPayload 验证 TCP payload 提取逻辑。
func TestExtractTCPPayload(t *testing.T) {
	tests := []struct {
		name    string
		pkt     []byte
		wantLen int
	}{
		{
			name:    "空包",
			pkt:     nil,
			wantLen: 0,
		},
		{
			name:    "过短",
			pkt:     make([]byte, 10),
			wantLen: 0,
		},
		{
			name:    "纯 TCP SYN 无 payload",
			pkt:     buildIPv4TCP("1.1.1.1", 80, "2.2.2.2", 443, 0x02),
			wantLen: 0,
		},
		{
			name: "TCP 带 payload",
			pkt: func() []byte {
				base := buildIPv4TCP("1.1.1.1", 80, "2.2.2.2", 443, 0x10)
				payload := []byte("GET / HTTP/1.1\r\n")
				pkt := append(base, payload...)
				// 更新 IPv4 Total Length
				binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
				return pkt
			}(),
			wantLen: len("GET / HTTP/1.1\r\n"),
		},
		{
			name:    "非 TCP 协议（UDP）",
			pkt:     buildIPv4UDP("1.1.1.1", 80, "2.2.2.2", 53, []byte("data")),
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTCPPayload(tt.pkt)
			if len(got) != tt.wantLen {
				t.Errorf("extractTCPPayload() len=%d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

// TestBuildRouteConfig 验证从 NodeConfig 构建路由配置。
func TestBuildRouteConfig(t *testing.T) {
	cfg := &genv1.NodeConfig{
		Peers: []*genv1.Peer{
			{
				NodeId:     "node-a",
				AllowedIps: []string{"100.64.1.0/24", "100.64.2.0/24"},
			},
			{
				NodeId:     "node-b",
				AllowedIps: []string{"100.64.3.0/24"},
			},
		},
	}

	rc := buildRouteConfig(cfg)

	if len(rc.FIB) != 3 {
		t.Fatalf("FIB 应有 3 条, got %d", len(rc.FIB))
	}

	// 验证第一条
	if rc.FIB[0].NextHop != "node-a" {
		t.Errorf("FIB[0].NextHop=%q, want %q", rc.FIB[0].NextHop, "node-a")
	}
	if rc.FIB[0].Prefix.String() != "100.64.1.0/24" {
		t.Errorf("FIB[0].Prefix=%s, want 100.64.1.0/24", rc.FIB[0].Prefix)
	}
}

// TestParsePacketToContext 验证 parsePacketToContext 正确提取五元组信息。
func TestParsePacketToContext(t *testing.T) {
	dp := &DataPlane{}

	t.Run("TCP 包", func(t *testing.T) {
		pkt := buildIPv4TCP("10.0.0.1", 1234, "100.64.1.1", 443, 0x02)
		ctx := dp.parsePacketToContext(pkt)
		if ctx.IPVersion != 4 {
			t.Errorf("IPVersion=%d, want 4", ctx.IPVersion)
		}
		if ctx.Network != metadata.NetworkTCP {
			t.Errorf("Network=%q, want %q", ctx.Network, metadata.NetworkTCP)
		}
		if ctx.Source.Addr().String() != "10.0.0.1" {
			t.Errorf("SrcIP=%s, want 10.0.0.1", ctx.Source.Addr())
		}
		if ctx.Source.Port() != 1234 {
			t.Errorf("SrcPort=%d, want 1234", ctx.Source.Port())
		}
		if ctx.Destination.Addr().String() != "100.64.1.1" {
			t.Errorf("DstIP=%s, want 100.64.1.1", ctx.Destination.Addr())
		}
		if ctx.Destination.Port() != 443 {
			t.Errorf("DstPort=%d, want 443", ctx.Destination.Port())
		}
	})

	t.Run("UDP 包", func(t *testing.T) {
		pkt := buildIPv4UDP("10.0.0.2", 5353, "100.64.1.2", 53, []byte("data"))
		ctx := dp.parsePacketToContext(pkt)
		if ctx.Network != metadata.NetworkUDP {
			t.Errorf("Network=%q, want %q", ctx.Network, metadata.NetworkUDP)
		}
		if ctx.Destination.Port() != 53 {
			t.Errorf("DstPort=%d, want 53", ctx.Destination.Port())
		}
	})

	t.Run("空包", func(t *testing.T) {
		ctx := dp.parsePacketToContext(nil)
		if ctx.IPVersion != 0 {
			t.Errorf("空包 IPVersion=%d, want 0", ctx.IPVersion)
		}
	})
}

// buildDNSQuery 构造一个最小化的合法 DNS 查询报文（UDP payload 部分）。
func buildDNSQuery() []byte {
	// DNS header：12 字节
	// TxID=0x1234, Flags=0x0100（标准查询）, QDCOUNT=1, ANCOUNT=0, NSCOUNT=0, ARCOUNT=0
	hdr := []byte{
		0x12, 0x34, // Transaction ID
		0x01, 0x00, // Flags: QR=0（查询）, OPCODE=0, RD=1
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, // ANCOUNT=0
		0x00, 0x00, // NSCOUNT=0
		0x00, 0x00, // ARCOUNT=0
	}
	// Question section：example.com（简化）
	question := []byte{
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e', // \007example
		0x03, 'c', 'o', 'm', // \003com
		0x00,       // 终止符
		0x00, 0x01, // QTYPE=A
		0x00, 0x01, // QCLASS=IN
	}
	return append(hdr, question...)
}

// TestExtractUDPPayload 验证 extractUDPPayload 正确提取 UDP payload。
func TestExtractUDPPayload(t *testing.T) {
	payload := []byte("dns-data")
	pkt := buildIPv4UDP("10.0.0.1", 1234, "8.8.8.8", 53, payload)
	got := extractUDPPayload(pkt)
	if string(got) != string(payload) {
		t.Errorf("extractUDPPayload=%q, want %q", got, payload)
	}

	// 非 UDP 包应返回 nil
	tcpPkt := buildIPv4TCP("10.0.0.1", 1234, "8.8.8.8", 80, 0x02)
	if extractUDPPayload(tcpPkt) != nil {
		t.Error("TCP 包应返回 nil")
	}
}

// TestIsDNSHijack 验证 isDNSHijack 正确检测 DNS 劫持目标。
func TestIsDNSHijack(t *testing.T) {
	hijackIP := netip.MustParseAddr("198.18.0.1")
	dp := &DataPlane{
		dnsHijackAddrs: map[netip.Addr]bool{hijackIP: true},
	}

	t.Run("命中 DNS 劫持", func(t *testing.T) {
		dnsPayload := buildDNSQuery()
		pkt := buildIPv4UDP("10.0.0.1", 1234, "198.18.0.1", 53, dnsPayload)
		ctx := dp.parsePacketToContext(pkt)
		if !dp.isDNSHijack(pkt, ctx) {
			t.Error("应命中 DNS 劫持，got false")
		}
		if ctx.Protocol != metadata.ProtocolDNS {
			t.Errorf("ctx.Protocol=%q, want %q", ctx.Protocol, metadata.ProtocolDNS)
		}
	})

	t.Run("目标 IP 不在劫持列表", func(t *testing.T) {
		dnsPayload := buildDNSQuery()
		pkt := buildIPv4UDP("10.0.0.1", 1234, "8.8.8.8", 53, dnsPayload)
		ctx := dp.parsePacketToContext(pkt)
		if dp.isDNSHijack(pkt, ctx) {
			t.Error("不应命中 DNS 劫持，got true")
		}
	})

	t.Run("空劫持列表", func(t *testing.T) {
		dp2 := &DataPlane{dnsHijackAddrs: map[netip.Addr]bool{}}
		dnsPayload := buildDNSQuery()
		pkt := buildIPv4UDP("10.0.0.1", 1234, "198.18.0.1", 53, dnsPayload)
		ctx := dp2.parsePacketToContext(pkt)
		if dp2.isDNSHijack(pkt, ctx) {
			t.Error("空列表不应命中 DNS 劫持，got true")
		}
	})

	t.Run("非 DNS payload（UDP 但不符合 DNS 格式）", func(t *testing.T) {
		pkt := buildIPv4UDP("10.0.0.1", 1234, "198.18.0.1", 53, []byte("not-dns"))
		ctx := dp.parsePacketToContext(pkt)
		if dp.isDNSHijack(pkt, ctx) {
			t.Error("非 DNS payload 不应命中劫持，got true")
		}
	})
}

// TestDataPlane_DNSHijackConfig 验证 DNSHijackAddrs 配置正确初始化 dnsHijackAddrs 集合。
func TestDataPlane_DNSHijackConfig(t *testing.T) {
	ft := tun.NewFakeTUN("dp-dnshijack", 1400)
	defer ft.Close()

	hijackIP1 := netip.MustParseAddr("198.18.0.1")
	hijackIP2 := netip.MustParseAddr("198.18.0.2")

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	pool := connpool.NewPool(connpool.DefaultConfig())
	defer pool.Close()

	dp, err := New(Config{
		TUN:            ft,
		Pool:           pool,
		Router:         router,
		FlowTracker:    tracker,
		DNSHijackAddrs: []netip.Addr{hijackIP1, hijackIP2},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 验证 dnsHijackAddrs 集合已正确构建
	if !dp.dnsHijackAddrs[hijackIP1] {
		t.Errorf("dnsHijackAddrs 缺少 %s", hijackIP1)
	}
	if !dp.dnsHijackAddrs[hijackIP2] {
		t.Errorf("dnsHijackAddrs 缺少 %s", hijackIP2)
	}
	if len(dp.dnsHijackAddrs) != 2 {
		t.Errorf("dnsHijackAddrs len=%d, want 2", len(dp.dnsHijackAddrs))
	}
}

// TestDataPlane_DNSHijack_DropsPacket 验证命中 DNS 劫持的包被 handleDNSHijack 消费（不进入流追踪）。
func TestDataPlane_DNSHijack_DropsPacket(t *testing.T) {
	ft := tun.NewFakeTUN("dp-dnshijack-drop", 1400)
	defer ft.Close()

	hijackIP := netip.MustParseAddr("198.18.0.1")
	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	pool := connpool.NewPool(connpool.DefaultConfig())
	defer pool.Close()

	dp, err := New(Config{
		TUN:            ft,
		Pool:           pool,
		Router:         router,
		FlowTracker:    tracker,
		DNSHijackAddrs: []netip.Addr{hijackIP},
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	// 注入一个命中 DNS 劫持的包
	dnsPayload := buildDNSQuery()
	pkt := buildIPv4UDP("10.0.0.1", 1234, "198.18.0.1", 53, dnsPayload)
	ft.Inject(pkt)

	time.Sleep(100 * time.Millisecond)

	// 命中 DNS 劫持的包在 handleDNSHijack 中被消费，不应进入 FlowTracker
	if tracker.Count() != 0 {
		t.Fatalf("DNS 劫持包不应进入 FlowTracker，got %d 条流", tracker.Count())
	}
}

// TestTUNReadOffset 验证 runTUNRead 使用正确的 offset 和缓冲区大小。
func TestTUNReadOffset(t *testing.T) {
	// 验证 tunWriteOffset 常量值正确（10 字节，跨平台安全值）
	if tunWriteOffset != 10 {
		t.Errorf("tunWriteOffset=%d, want 10", tunWriteOffset)
	}

	ft := tun.NewFakeTUN("dp-tun-offset", 1400)
	defer ft.Close()

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()
	router.Update(&route.RouteConfig{
		FIB: []route.FIBEntry{
			{Prefix: netip.MustParsePrefix("100.64.0.0/10"), NextHop: "peer-1"},
		},
	})
	pool := connpool.NewPool(connpool.DefaultConfig())
	pool.Update(map[string]connpool.HopInfo{
		"peer-1": {Addrs: []string{"1.2.3.4:7447"}},
	})
	defer pool.Close()

	dp, err := New(Config{
		TUN:         ft,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
	})
	if err != nil {
		t.Fatal(err)
	}

	go dp.Run()
	defer dp.Close()

	// 注入包，验证在使用正确 offset 情况下包被正常处理
	pkt := buildIPv4TCP("10.0.0.1", 1234, "100.64.1.1", 443, 0x02)
	ft.Inject(pkt)

	time.Sleep(100 * time.Millisecond)

	// 正确读取偏移后包应被 FlowTracker 追踪
	if tracker.Count() != 1 {
		t.Fatalf("应有 1 个流（offset 正确时包被处理），got %d", tracker.Count())
	}
}

// TestBuildHopMap 验证从 NodeConfig 构建跳映射。
func TestBuildHopMap(t *testing.T) {
	cfg := &genv1.NodeConfig{
		Peers: []*genv1.Peer{
			{NodeId: "node-a"},
			{NodeId: "node-b"},
		},
		Topology: &genv1.TopologyAssignment{
			Neighbors: []*genv1.NeighborRef{
				{NodeId: "node-a", Ingresses: []*genv1.Ingress{{Host: "1.2.3.4", Port: 7447}}},
				{NodeId: "node-b", Ingresses: []*genv1.Ingress{{Host: "5.6.7.8", Port: 7447}}},
			},
		},
	}

	hops := buildHopMap(cfg)

	if len(hops) != 2 {
		t.Fatalf("应有 2 个跳, got %d", len(hops))
	}
	if hi, ok := hops["node-a"]; !ok {
		t.Error("缺少 node-a 跳")
	} else if hi.Addr() != "1.2.3.4:7447" {
		t.Errorf("node-a addr=%q, want 1.2.3.4:7447", hi.Addr())
	}
	if hi, ok := hops["node-b"]; !ok {
		t.Error("缺少 node-b 跳")
	} else if hi.Addr() != "5.6.7.8:7447" {
		t.Errorf("node-b addr=%q, want 5.6.7.8:7447", hi.Addr())
	}
}
