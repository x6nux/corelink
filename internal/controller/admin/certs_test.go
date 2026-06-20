package admin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
)

// 测试 certDTO / caDTO 结构体字段与 JSON 序列化。

func TestCertDTOJSONFields(t *testing.T) {
	// 验证 certDTO 的 JSON tag 产出预期键名。
	now := time.Now().Truncate(time.Second)
	dto := certDTO{
		Serial:    "abc123",
		NodeID:    "n1",
		NotAfter:  now,
		Revoked:   false,
		CreatedAt: now,
	}
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	// 验证预期键存在。
	for _, key := range []string{"serial", "node_id", "not_after", "revoked", "created_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON 中缺少键 %q", key)
		}
	}
	// revoked_at 使用 omitempty，未设置时不应出现。
	if _, ok := m["revoked_at"]; ok {
		t.Error("revoked_at 为 nil 时不应出现在 JSON 中")
	}
}

func TestCADTOJSONFields(t *testing.T) {
	// 验证 caDTO 的 JSON tag 产出预期键名。
	dto := caDTO{CACertPEM: "---PEM---", CAHash: "sha256:abcdef"}
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["ca_cert_pem"] != "---PEM---" {
		t.Errorf("ca_cert_pem = %v", m["ca_cert_pem"])
	}
	if m["ca_hash"] != "sha256:abcdef" {
		t.Errorf("ca_hash = %v", m["ca_hash"])
	}
}

func TestToCertDTOFieldMapping(t *testing.T) {
	// 验证 toCertDTO 对所有字段的一一映射（含 RevokedAt 有值场景）。
	now := time.Now()
	revokedAt := now.Add(-5 * time.Minute)
	cert := &store.Cert{
		Serial:    "serial-999",
		NodeID:    "node-x",
		NotAfter:  now.Add(24 * time.Hour),
		Revoked:   true,
		RevokedAt: &revokedAt,
		CreatedAt: now,
	}
	dto := toCertDTO(cert)
	if dto.Serial != cert.Serial {
		t.Errorf("Serial = %q, 期望 %q", dto.Serial, cert.Serial)
	}
	if dto.NodeID != cert.NodeID {
		t.Errorf("NodeID = %q, 期望 %q", dto.NodeID, cert.NodeID)
	}
	if !dto.NotAfter.Equal(cert.NotAfter) {
		t.Errorf("NotAfter 不匹配")
	}
	if !dto.Revoked {
		t.Error("Revoked 应为 true")
	}
	if dto.RevokedAt == nil || !dto.RevokedAt.Equal(revokedAt) {
		t.Error("RevokedAt 不匹配")
	}
	if !dto.CreatedAt.Equal(cert.CreatedAt) {
		t.Errorf("CreatedAt 不匹配")
	}
}
