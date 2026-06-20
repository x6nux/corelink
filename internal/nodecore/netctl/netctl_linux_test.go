//go:build linux

package netctl

import (
	"net/netip"
	"testing"
)

func TestParseHexIPv4(t *testing.T) {
	// 0100A8C0 = 192.168.0.1，小端序存储
	addr, err := parseHexIPv4("0100A8C0")
	if err != nil {
		t.Fatal(err)
	}
	if addr != netip.MustParseAddr("192.168.0.1") {
		t.Errorf("got %v, want 192.168.0.1", addr)
	}
}
