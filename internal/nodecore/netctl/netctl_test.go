package netctl

import (
	"runtime"
	"testing"
)

func TestNewRouteManager(t *testing.T) {
	rm := NewRouteManager()
	if rm == nil {
		t.Fatal("NewRouteManager returned nil")
	}
}

func TestNewInterfaceDetector(t *testing.T) {
	det := NewInterfaceDetector()
	if det == nil {
		t.Fatal("NewInterfaceDetector returned nil")
	}
}

func TestNewDNSManager(t *testing.T) {
	dm := NewDNSManager()
	if dm == nil {
		t.Fatal("NewDNSManager returned nil")
	}
}

func TestDefaultInterface_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	det := NewInterfaceDetector()
	name, gw, err := det.DefaultInterface()
	if err != nil {
		t.Skipf("no default route: %v", err)
	}
	if name == "" {
		t.Error("empty interface name")
	}
	if !gw.IsValid() {
		t.Error("invalid gateway")
	}
	t.Logf("default interface: %s, gateway: %v", name, gw)
}

func TestSystemDNSServers_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	det := NewInterfaceDetector()
	servers, err := det.SystemDNSServers()
	if err != nil {
		t.Skipf("cannot read DNS: %v", err)
	}
	t.Logf("DNS servers: %v", servers)
}
