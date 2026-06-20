package configsvc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 测试 ConfigWS 构造函数与无 TLS 场景。

func TestNewConfigWSNonNil(t *testing.T) {
	// 验证 NewConfigWS 返回非 nil。
	ws := NewConfigWS(nil, nil)
	if ws == nil {
		t.Fatal("NewConfigWS 不应返回 nil")
	}
}

func TestNewConfigWSEpochNil(t *testing.T) {
	// NewConfigWS 构造的实例 epoch 为 nil。
	ws := NewConfigWS(nil, nil)
	if ws.epoch != nil {
		t.Error("NewConfigWS 的 epoch 应为 nil")
	}
}

func TestConfigWSServeHTTPWithoutTLS(t *testing.T) {
	// 无 TLS 时应返回 401。
	bumper := newStubBumper()
	n := NewNotify(bumper)
	defer n.Close()
	getter := newStubNodeInfoGetter()
	ws := NewConfigWS(n, getter)

	req := httptest.NewRequest(http.MethodGet, "/v1/watch", nil)
	// 不设置 req.TLS
	w := httptest.NewRecorder()
	ws.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("无 TLS 期望 401, 实际 %d", w.Code)
	}
}

func TestConfigWSServeHTTPNodeNotFound(t *testing.T) {
	// TLS 有效但节点不存在时应返回 404。
	bumper := newStubBumper()
	n := NewNotify(bumper)
	defer n.Close()
	getter := newStubNodeInfoGetter()
	ws := NewConfigWS(n, getter)

	req := httptest.NewRequest(http.MethodGet, "/v1/watch", nil)
	req.TLS = fakeTLSState("nonexistent-node")
	w := httptest.NewRecorder()
	ws.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在节点期望 404, 实际 %d", w.Code)
	}
}
