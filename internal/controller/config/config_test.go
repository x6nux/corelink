package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	// 不提供任何配置文件时，默认值生效。
	cfg := defaultConfig()
	if cfg.VirtualCIDR != "100.64.0.0/10" {
		t.Fatalf("VirtualCIDR = %q, want 100.64.0.0/10", cfg.VirtualCIDR)
	}
	if cfg.ListenAddr != ":7443" {
		t.Fatalf("ListenAddr = %q, want :7443", cfg.ListenAddr)
	}
	if cfg.TLSMode != "self-signed" {
		t.Fatalf("TLSMode = %q, want self-signed", cfg.TLSMode)
	}
}

func TestAdminDefaults(t *testing.T) {
	cfg := defaultConfig()
	if cfg.AdminAddr != "127.0.0.1:8090" {
		t.Fatalf("AdminAddr = %q, want 127.0.0.1:8090", cfg.AdminAddr)
	}
	if cfg.AdminUser != "admin" {
		t.Fatalf("AdminUser = %q, want admin", cfg.AdminUser)
	}
}

func TestAdminFieldsLoadAndDefault(t *testing.T) {
	// 只给 DBDSN，AdminAddr/AdminUser 应填默认值（密码已迁移到 DB，不在配置中）。
	data := []byte(`{"DBDSN":"sqlite://:memory:","CAEncKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.AdminAddr != "127.0.0.1:8090" {
		t.Fatalf("AdminAddr default not applied: %q", loaded.AdminAddr)
	}
	if loaded.AdminUser != "admin" {
		t.Fatalf("AdminUser default not applied: %q", loaded.AdminUser)
	}
}

func TestLoadJSON(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	cfg := &Config{
		DBDSN:          "sqlite:///tmp/test.db",
		VirtualCIDR:    "10.0.0.0/8",
		CASubject:      "CoreLink Test CA",
		CAEncKey:       key,
		GRPCEnrollAddr: ":9443",
	}
	data, _ := json.Marshal(cfg)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DBDSN != "sqlite:///tmp/test.db" {
		t.Fatalf("DBDSN = %q", loaded.DBDSN)
	}
	if loaded.VirtualCIDR != "10.0.0.0/8" {
		t.Fatalf("VirtualCIDR = %q, want 10.0.0.0/8", loaded.VirtualCIDR)
	}
}

func TestLoadDefaultsWhenFieldsAbsent(t *testing.T) {
	// 只给 DBDSN，其余字段应用默认值。
	data := []byte(`{"DBDSN":"sqlite://:memory:","CAEncKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.VirtualCIDR != "100.64.0.0/10" {
		t.Fatalf("VirtualCIDR default not applied: %q", loaded.VirtualCIDR)
	}
}

// CAEncKey 已迁移到 DB 存储（json:"-"），不再从配置文件加载/校验。

func TestLoadNonExistentFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("want error for nonexistent file")
	}
}
