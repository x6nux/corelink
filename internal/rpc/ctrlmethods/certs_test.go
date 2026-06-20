package ctrlmethods

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// TestToCertDTO 验证 toCertDTO 转换逻辑
func TestToCertDTO(t *testing.T) {
	now := time.Now()
	revAt := now.Add(-time.Hour)
	cases := []struct {
		name    string
		input   store.Cert
		wantRev bool
	}{
		{
			name:    "未吊销证书",
			input:   store.Cert{Serial: "s1", NodeID: "n1", NotAfter: now, Revoked: false},
			wantRev: false,
		},
		{
			name:    "已吊销证书",
			input:   store.Cert{Serial: "s2", NodeID: "n2", NotAfter: now, Revoked: true, RevokedAt: &revAt},
			wantRev: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dto := toCertDTO(&tc.input)
			if dto.Serial != tc.input.Serial {
				t.Errorf("serial = %q, want %q", dto.Serial, tc.input.Serial)
			}
			if dto.NodeID != tc.input.NodeID {
				t.Errorf("node_id = %q, want %q", dto.NodeID, tc.input.NodeID)
			}
			if dto.Revoked != tc.wantRev {
				t.Errorf("revoked = %v, want %v", dto.Revoked, tc.wantRev)
			}
			if tc.wantRev && dto.RevokedAt == nil {
				t.Error("已吊销证书的 revoked_at 不应为 nil")
			}
			if !tc.wantRev && dto.RevokedAt != nil {
				t.Errorf("未吊销证书的 revoked_at 应为 nil，got %v", dto.RevokedAt)
			}
		})
	}
}

// TestHandleCertsList_Empty 验证无证书时返回空数组
func TestHandleCertsList_Empty(t *testing.T) {
	ms := &mockStore{certs: []store.Cert{}}
	deps := buildTestDeps(ms, nil, nil, nil, nil, nil)
	h := handleCertsList(deps)
	result, err := h(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, _ := json.Marshal(result)
	var got []certDTO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("无证书应返回空数组，got %d", len(got))
	}
}

// TestHandleCAInfo_NilCA 验证 CA 为 nil 时返回错误
func TestHandleCAInfo_NilCA(t *testing.T) {
	// 直接构造 Deps 确保 CA 接口值为 nil（避免 nil *mockCA 包装成非 nil 接口）
	deps := Deps{
		Store:     &mockStore{},
		StartTime: time.Now(),
	}
	h := handleCAInfo(deps)
	_, err := h(nil)
	if err == nil {
		t.Fatal("CA 为 nil 时应返回错误")
	}
}
