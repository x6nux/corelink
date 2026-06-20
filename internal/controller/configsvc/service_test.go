package configsvc

import (
	"net/http"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// 测试 Services 聚合构造与 handler 方法。

func TestServicesNew(t *testing.T) {
	// 验证 New 构造返回完整的 Services 实例。
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	crl := CRLProviderFunc(func(dur time.Duration) ([]byte, error) {
		return []byte("test-crl"), nil
	})
	svc := New(st, crl, nil)
	if svc == nil {
		t.Fatal("New 不应返回 nil")
	}
	if svc.Notify == nil {
		t.Error("Notify 不应为 nil")
	}
	if svc.ConfigGRPC == nil {
		t.Error("ConfigGRPC 不应为 nil")
	}
	if svc.ConfigWS == nil {
		t.Error("ConfigWS 不应为 nil")
	}
	if svc.ConfigHTTP == nil {
		t.Error("ConfigHTTP 不应为 nil")
	}
	svc.Notify.Close()
}

func TestServicesHTTPHandler(t *testing.T) {
	// 验证 HTTPHandler 返回非 nil http.Handler。
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	crl := CRLProviderFunc(func(dur time.Duration) ([]byte, error) {
		return []byte("crl"), nil
	})
	svc := New(st, crl, nil)
	defer svc.Notify.Close()

	var h http.Handler = svc.HTTPHandler()
	if h == nil {
		t.Fatal("HTTPHandler 不应返回 nil")
	}
}

func TestServicesWSHandler(t *testing.T) {
	// 验证 WSHandler 返回非 nil http.Handler。
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	crl := CRLProviderFunc(func(dur time.Duration) ([]byte, error) {
		return []byte("crl"), nil
	})
	svc := New(st, crl, nil)
	defer svc.Notify.Close()

	var h http.Handler = svc.WSHandler()
	if h == nil {
		t.Fatal("WSHandler 不应返回 nil")
	}
}

func TestServicesEpochInitialZero(t *testing.T) {
	// 未调 SetEpoch 时 Epoch 应为 0。
	var svc Services
	if svc.Epoch() != 0 {
		t.Errorf("初始 Epoch = %d, 期望 0", svc.Epoch())
	}
}
