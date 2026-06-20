package ingress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryPublicIP_Valid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("  203.0.113.7\n"))
	}))
	defer srv.Close()

	ip, err := QueryPublicIP(context.Background(), srv.Client(), []string{srv.URL})
	if err != nil {
		t.Fatalf("意外错误: %v", err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("ip = %q, 期望 203.0.113.7", ip)
	}
}

func TestQueryPublicIP_InvalidContentSkipped(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-an-ip"))
	}))
	defer bad.Close()

	_, err := QueryPublicIP(context.Background(), bad.Client(), []string{bad.URL})
	if err == nil {
		t.Fatalf("非法内容应导致全部失败并返回错误")
	}
}

func TestQueryPublicIP_FallthroughOnFailure(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("198.51.100.9"))
	}))
	defer good.Close()

	// 首个 URL 指向已关闭端口（连接失败），次个成功 → 容错穿透。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	ip, err := QueryPublicIP(context.Background(), good.Client(), []string{deadURL, good.URL})
	if err != nil {
		t.Fatalf("应穿透到成功 URL, err=%v", err)
	}
	if ip != "198.51.100.9" {
		t.Errorf("ip = %q, 期望 198.51.100.9", ip)
	}
}

func TestQueryPublicIP_EmptyURLs(t *testing.T) {
	if _, err := QueryPublicIP(context.Background(), http.DefaultClient, nil); err == nil {
		t.Errorf("空 urls 应返回错误")
	}
}

func TestQueryPublicIP_NilClientUsesDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.7"))
	}))
	defer srv.Close()
	// nil client 应回退默认；此处仍用 httptest URL，确保不 panic。
	ip, err := QueryPublicIP(context.Background(), nil, []string{srv.URL})
	if err != nil {
		t.Fatalf("nil client 应回退默认, err=%v", err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("ip = %q", ip)
	}
}

func TestDefaultPublicIPURLs(t *testing.T) {
	if len(DefaultPublicIPURLs) == 0 {
		t.Errorf("DefaultPublicIPURLs 不应为空")
	}
}

func TestQueryPublicIP_ReservedRejected(t *testing.T) {
	for _, ip := range []string{"100.64.1.1", "198.18.0.1", "240.0.0.1"} {
		ip := ip
		t.Run(ip, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(ip))
			}))
			defer srv.Close()

			if _, err := QueryPublicIP(context.Background(), srv.Client(), []string{srv.URL}); err == nil {
				t.Errorf("出口返回保留段 %s 应被拒绝", ip)
			}
		})
	}
}
