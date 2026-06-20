package geoipdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdater_Download(t *testing.T) {
	fakeDAT := []byte("fake-geoip-data-for-test")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fakeDAT)
	}))
	defer srv.Close()

	dir := t.TempDir()
	u := NewUpdater(dir, []string{srv.URL + "/geoip.dat"})
	path, sha, err := u.TryDownload(context.Background())
	if err != nil {
		t.Fatalf("TryDownload: %v", err)
	}
	if path == "" || sha == "" {
		t.Fatal("期望非空路径和 SHA256")
	}

	// 验证文件存在
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取下载文件: %v", err)
	}
	if string(data) != string(fakeDAT) {
		t.Fatal("文件内容不匹配")
	}

	// 验证 sha256 文件
	shaFile := filepath.Join(dir, "geoip.dat.sha256")
	shaData, err := os.ReadFile(shaFile)
	if err != nil {
		t.Fatalf("读取 sha256 文件: %v", err)
	}
	if string(shaData) != sha {
		t.Fatalf("SHA256 不匹配: file=%s, returned=%s", string(shaData), sha)
	}

	// 验证 CurrentSHA256
	if u.CurrentSHA256() != sha {
		t.Fatal("CurrentSHA256 不匹配")
	}
	if !u.Exists() {
		t.Fatal("Exists 应返回 true")
	}
}

func TestUpdater_DownloadFallback(t *testing.T) {
	fakeDAT := []byte("fallback-data")

	// 第一个源返回 500
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv1.Close()

	// 第二个源正常
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fakeDAT)
	}))
	defer srv2.Close()

	u := NewUpdater(t.TempDir(), []string{srv1.URL + "/bad", srv2.URL + "/good"})
	_, _, err := u.TryDownload(context.Background())
	if err != nil {
		t.Fatalf("期望 fallback 成功, got: %v", err)
	}
}

func TestUpdater_NotExists(t *testing.T) {
	u := NewUpdater(t.TempDir(), nil)
	if u.Exists() {
		t.Fatal("Exists 应返回 false（尚未下载）")
	}
	if u.CurrentSHA256() != "" {
		t.Fatal("CurrentSHA256 应返回空字符串")
	}
}
