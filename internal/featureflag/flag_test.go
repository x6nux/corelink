package featureflag

import "testing"

func TestFlags_DefaultOff(t *testing.T) {
	f := New()
	if f.Enabled(VIPRouting) {
		t.Error("VIPRouting should be off by default")
	}
	if f.Enabled(TLS0RTT) {
		t.Error("TLS0RTT should be off by default")
	}
}

func TestFlags_Enable(t *testing.T) {
	f := New()
	f.Set(VIPRouting, true)
	if !f.Enabled(VIPRouting) {
		t.Error("VIPRouting should be on after Set")
	}
	if f.Enabled(TLS0RTT) {
		t.Error("TLS0RTT should remain off")
	}
}

func TestFlags_FromMap(t *testing.T) {
	f := FromMap(map[string]bool{"vip_routing": true, "tls_0rtt": false})
	if !f.Enabled(VIPRouting) {
		t.Error("VIPRouting should be on from config")
	}
	if f.Enabled(TLS0RTT) {
		t.Error("TLS0RTT should be off from config")
	}
}

func TestFlags_ConcurrentAccess(t *testing.T) {
	f := New()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			f.Set(VIPRouting, i%2 == 0)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = f.Enabled(VIPRouting)
	}
	<-done
}
