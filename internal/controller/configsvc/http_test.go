package configsvc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/x6nux/corelink/internal/controller/store"
	"google.golang.org/protobuf/encoding/protojson"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// 测试 ConfigHTTP 构造与 ETag 响应头。

func TestNewConfigHTTPNonNil(t *testing.T) {
	// 验证 NewConfigHTTP 返回非 nil。
	h := NewConfigHTTP(&stubConfigStore{}, &stubCRL{}, nil)
	if h == nil {
		t.Fatal("NewConfigHTTP 不应返回 nil")
	}
}

func TestConfigHTTPETagHeader(t *testing.T) {
	// 验证成功响应包含 ETag header，值为 generation 数字。
	const nodeID = "etag-node"
	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.5.1/32", Generation: 123},
		},
	}
	h := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200, 实际 %d", w.Code)
	}
	etag := w.Header().Get("ETag")
	if etag != "123" {
		t.Errorf("ETag = %q, 期望 %q", etag, "123")
	}
}

func TestConfigHTTPContentType(t *testing.T) {
	// 验证成功响应 Content-Type 为 application/json。
	const nodeID = "ct-node"
	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.5.2/32", Generation: 1},
		},
	}
	h := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, 期望 application/json", ct)
	}
}

func TestConfigHTTPNodeNotFound(t *testing.T) {
	// 请求不存在的节点应返回 500。
	st := &stubConfigStore{nodes: nil}
	h := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState("nonexistent-node")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("不存在节点期望 500, 实际 %d", w.Code)
	}
}

func TestConfigHTTPWithNodeRelayFn(t *testing.T) {
	// 验证 nodeRelayFn 被传入后不会导致 panic。
	const nodeID = "relay-fn-node"
	st := &stubConfigStore{
		nodes: []store.Node{
			{ID: nodeID, VirtualIP: "100.64.5.3/32", Generation: 1},
		},
		policy: &store.ACLPolicy{Document: `{"groups":{},"acls":[]}`},
	}
	fn := func() map[string]string { return map[string]string{nodeID: "r1"} }
	h := NewConfigHTTP(st, &stubCRL{data: []byte("crl")}, fn)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.TLS = fakeTLSState(nodeID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200, 实际 %d\n%s", w.Code, w.Body.String())
	}
	var cfg genv1.NodeConfig
	if err := protojson.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("protojson 解码失败: %v", err)
	}
}
