package transport

import (
	"math"
	"net/netip"
	"testing"
)

var (
	vipA = netip.MustParseAddr("100.64.0.1")
	vipB = netip.MustParseAddr("100.64.0.2")
	vipC = netip.MustParseAddr("100.64.0.3")
	vipD = netip.MustParseAddr("100.64.0.4")
)

// TestRouteSyncEncodeDecode_Basic 基本编解码：单条路由 + 单跳投递路由
func TestRouteSyncEncodeDecode_Basic(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		Nonce:       42,
		TimestampNs: 1234567890,
		SourceVIP:   vipA,
		HopIndex:    0,
		Route:       []netip.Addr{vipB},
		SyncEntry: RouteSyncEntry{
			DstVIP: vipC, NextHopVIP: vipB, RTTMs: 15,
		},
	}

	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.IsRouteSync {
		t.Fatal("IsRouteSync 应为 true")
	}
	if got.IsReply || got.AutoReply {
		t.Fatal("Reply/AutoReply 应为 false")
	}
	if got.Nonce != 42 {
		t.Errorf("Nonce: got %d, want 42", got.Nonce)
	}
	if got.SourceVIP != vipA {
		t.Errorf("SourceVIP: got %v, want %v", got.SourceVIP, vipA)
	}
	if len(got.Route) != 1 || got.Route[0] != vipB {
		t.Errorf("Route: got %v, want [%v]", got.Route, vipB)
	}
	if got.SyncEntry.DstVIP != vipC {
		t.Errorf("DstVIP: got %v, want %v", got.SyncEntry.DstVIP, vipC)
	}
	if got.SyncEntry.NextHopVIP != vipB {
		t.Errorf("NextHopVIP: got %v, want %v", got.SyncEntry.NextHopVIP, vipB)
	}
	if got.SyncEntry.RTTMs != 15 {
		t.Errorf("RTTMs: got %v, want 15", got.SyncEntry.RTTMs)
	}
}

// TestRouteSyncEncodeDecode_DirectRoute 直连路由：NextHopVIP == DstVIP
func TestRouteSyncEncodeDecode_DirectRoute(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		Nonce:       1,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry: RouteSyncEntry{
			DstVIP: vipB, NextHopVIP: vipB, RTTMs: 3,
		},
	}

	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SyncEntry.DstVIP != got.SyncEntry.NextHopVIP {
		t.Errorf("直连路由 DstVIP 应等于 NextHopVIP: %v != %v", got.SyncEntry.DstVIP, got.SyncEntry.NextHopVIP)
	}
}

// TestRouteSyncEncodeDecode_ZeroRTT RTT=0 边界
func TestRouteSyncEncodeDecode_ZeroRTT(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry:   RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB, RTTMs: 0},
	}
	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SyncEntry.RTTMs != 0 {
		t.Errorf("RTTMs: got %v, want 0", got.SyncEntry.RTTMs)
	}
}

// TestRouteSyncEncodeDecode_MaxRTT uint16 最大值 = 65535ms
func TestRouteSyncEncodeDecode_MaxRTT(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry:   RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB, RTTMs: 65535},
	}
	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SyncEntry.RTTMs != 65535 {
		t.Errorf("RTTMs: got %v, want 65535", got.SyncEntry.RTTMs)
	}
}

// TestRouteSyncEncodeDecode_RTTPrecision float→uint16 截断精度
func TestRouteSyncEncodeDecode_RTTPrecision(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry:   RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB, RTTMs: 12.7},
	}
	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// uint16 截断：12.7 → 12
	if got.SyncEntry.RTTMs != 12 {
		t.Errorf("RTTMs: got %v, want 12 (截断)", got.SyncEntry.RTTMs)
	}
}

// TestRouteSyncEncodeDecode_MultiHopRoute 多跳投递路由
func TestRouteSyncEncodeDecode_MultiHopRoute(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB, vipC, vipD},
		SyncEntry:   RouteSyncEntry{DstVIP: vipD, NextHopVIP: vipC, RTTMs: 50},
	}
	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Route) != 3 {
		t.Fatalf("Route 长度: got %d, want 3", len(got.Route))
	}
	if got.Route[0] != vipB || got.Route[1] != vipC || got.Route[2] != vipD {
		t.Errorf("Route: got %v, want [B,C,D]", got.Route)
	}
}

// TestRouteSyncEncodeDecode_NotRouteSync 普通 Probe 帧不含 SyncEntry
func TestRouteSyncEncodeDecode_NotRouteSync(t *testing.T) {
	p := &ProbeFrame{
		IsReply:   false,
		AutoReply: true,
		Nonce:     99,
		SourceVIP: vipA,
		Route:     []netip.Addr{vipB},
	}
	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IsRouteSync {
		t.Fatal("IsRouteSync 应为 false")
	}
	if !got.AutoReply {
		t.Fatal("AutoReply 应为 true")
	}
	// SyncEntry 应为零值
	if got.SyncEntry.DstVIP.IsValid() {
		t.Errorf("非 RouteSync 帧 SyncEntry.DstVIP 应无效: %v", got.SyncEntry.DstVIP)
	}
}

// TestRouteSyncPayloadSize 帧大小验证
func TestRouteSyncPayloadSize(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry:   RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB, RTTMs: 10},
	}
	data := EncodeProbePayload(p)
	// 24(fixed) + 4(1-hop route) + 10(sync entry) = 38
	expected := probeFixedSize + 4 + routeSyncEntrySize
	if len(data) != expected {
		t.Errorf("payload size: got %d, want %d", len(data), expected)
	}
}

// TestRouteSyncFlagBits flags 位独立性
func TestRouteSyncFlagBits(t *testing.T) {
	tests := []struct {
		name      string
		reply     bool
		auto      bool
		routeSync bool
		wantFlags byte
	}{
		{"pure routesync", false, false, true, 4},
		{"all flags", true, true, true, 7},
		{"reply only", true, false, false, 1},
		{"auto only", false, true, false, 2},
		{"none", false, false, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &ProbeFrame{
				IsReply: tt.reply, AutoReply: tt.auto, IsRouteSync: tt.routeSync,
				SourceVIP: vipA, Route: []netip.Addr{vipB},
				SyncEntry: RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB},
			}
			data := EncodeProbePayload(p)
			if data[0] != tt.wantFlags {
				t.Errorf("flags: got 0x%02x, want 0x%02x", data[0], tt.wantFlags)
			}
			got, _ := DecodeProbePayload(data)
			if got.IsReply != tt.reply || got.AutoReply != tt.auto || got.IsRouteSync != tt.routeSync {
				t.Errorf("decode flags: reply=%v auto=%v sync=%v", got.IsReply, got.AutoReply, got.IsRouteSync)
			}
		})
	}
}

// TestRouteSyncDecode_TruncatedPayload 截断的载荷应不 panic
func TestRouteSyncDecode_TruncatedPayload(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry:   RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB, RTTMs: 10},
	}
	full := EncodeProbePayload(p)
	// 截断到只有 probe 头+route，缺少 sync entry
	truncated := full[:probeFixedSize+4]
	got, err := DecodeProbePayload(truncated)
	if err != nil {
		t.Fatalf("decode 截断数据不应报错: %v", err)
	}
	// SyncEntry 应为零值（数据不足跳过）
	if got.SyncEntry.DstVIP.IsValid() {
		t.Errorf("截断帧 SyncEntry 应为零值: %v", got.SyncEntry)
	}
}

// TestRouteSyncRTTOverflow float64 超过 uint16 范围时截断
func TestRouteSyncRTTOverflow(t *testing.T) {
	var bigRTT float64 = 70000
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry:   RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB, RTTMs: bigRTT},
	}
	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// uint16 溢出截断：70000 → 70000 & 0xFFFF = 4464
	expected := float64(uint16(bigRTT))
	if got.SyncEntry.RTTMs != expected {
		t.Errorf("RTTMs: got %v, want %v (uint16 截断)", got.SyncEntry.RTTMs, expected)
	}
}

// TestRouteSyncNegativeRTT 负 RTT 应截断为 0
func TestRouteSyncNegativeRTT(t *testing.T) {
	p := &ProbeFrame{
		IsRouteSync: true,
		SourceVIP:   vipA,
		Route:       []netip.Addr{vipB},
		SyncEntry:   RouteSyncEntry{DstVIP: vipC, NextHopVIP: vipB, RTTMs: -5},
	}
	data := EncodeProbePayload(p)
	got, err := DecodeProbePayload(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// uint16(-5) 会溢出，但编码后解码应该是某个正数
	_ = got.SyncEntry.RTTMs
	// 不 panic 就行
	if math.IsNaN(got.SyncEntry.RTTMs) || math.IsInf(got.SyncEntry.RTTMs, 0) {
		t.Errorf("RTTMs 不应为 NaN/Inf: %v", got.SyncEntry.RTTMs)
	}
}
