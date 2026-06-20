package portmap

import (
	"errors"
	"net"
	"reflect"
	"testing"
)

// strAddr 是仅实现 String() 的 fake net.Addr，用于走 addrToIP 的字符串 fallback。
type strAddr string

func (s strAddr) Network() string { return "fake" }
func (s strAddr) String() string  { return string(s) }

func ipnet(cidr string) net.Addr {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func listerOf(addrs []net.Addr, err error) InterfaceLister {
	return func() ([]net.Addr, error) { return addrs, err }
}

func TestDefaultGateways_Heuristic(t *testing.T) {
	addrs := []net.Addr{
		ipnet("192.168.1.50/24"),
		ipnet("10.0.0.5/8"),
		ipnet("172.16.3.7/16"),
		ipnet("127.0.0.1/8"),    // 回环 → 跳过
		ipnet("2001:db8::1/64"), // IPv6 → 跳过
		ipnet("8.8.8.8/32"),     // 公网 → 跳过
	}
	got := DefaultGateways(listerOf(addrs, nil))

	want := []string{"10.0.0.1", "172.16.3.1", "192.168.1.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	for _, bad := range []string{"127.0.0.1", "8.8.8.1", "8.8.8.8"} {
		for _, g := range got {
			if g == bad {
				t.Errorf("候选不应包含 %s，得到 %v", bad, got)
			}
		}
	}
}

func TestDefaultGateways_EmptyAddrs(t *testing.T) {
	got := DefaultGateways(listerOf(nil, nil))
	if len(got) != 0 {
		t.Fatalf("空 addrs 应返回空列表，得到 %v", got)
	}
}

func TestDefaultGateways_ListerError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("lister error 不应 panic: %v", r)
		}
	}()
	got := DefaultGateways(listerOf(nil, errors.New("boom")))
	if len(got) != 0 {
		t.Fatalf("lister error 应返回空列表，得到 %v", got)
	}
}

func TestDefaultGateways_NilLister(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil lister 不应 panic: %v", r)
		}
	}()
	got := DefaultGateways(nil)
	if got == nil {
		// 允许为空切片或 nil，类型即可；这里仅断言不 panic 且类型正确。
		_ = got
	}
}

func TestDefaultGateways_Dedup(t *testing.T) {
	addrs := []net.Addr{
		ipnet("192.168.1.10/24"),
		ipnet("192.168.1.20/24"),
	}
	got := DefaultGateways(listerOf(addrs, nil))
	want := []string{"192.168.1.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("去重失败，got %v, want %v", got, want)
	}
}

func TestDefaultGateways_StringFallbackAddr(t *testing.T) {
	// 走 addrToIP 字符串 fallback（CIDR 与裸 IP）。
	addrs := []net.Addr{
		strAddr("192.168.5.50/24"),
		strAddr("10.1.2.3"),
	}
	got := DefaultGateways(listerOf(addrs, nil))
	want := []string{"10.0.0.1", "10.1.2.1", "192.168.5.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
