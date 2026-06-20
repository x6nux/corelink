// Package geoipdb 管理 GeoIP 数据库的下载、校验与存储。
package geoipdb

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultSources GeoIP 下载源（按优先级）。
var DefaultSources = []string{
	"https://raw.githubusercontent.com/Loyalsoldier/geoip/release/geoip.dat",
	"https://cdn.jsdelivr.net/gh/Loyalsoldier/geoip@release/geoip.dat",
	"https://fastly.jsdelivr.net/gh/Loyalsoldier/geoip@release/geoip.dat",
}

// Updater 管理 GeoIP 数据库的下载和本地存储。
type Updater struct {
	dataDir string
	sources []string
	client  *http.Client
}

// NewUpdater 创建 GeoIP 更新器。
func NewUpdater(dataDir string, sources []string) *Updater {
	if len(sources) == 0 {
		sources = DefaultSources
	}
	return &Updater{
		dataDir: dataDir,
		sources: sources,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// TryDownload 逐个尝试下载源，返回本地文件路径和 SHA256。
func (u *Updater) TryDownload(ctx context.Context) (path string, sha string, err error) {
	for _, src := range u.sources {
		slog.Info("geoipdb: 尝试下载", "url", src)
		path, sha, err = u.downloadOne(ctx, src)
		if err == nil {
			slog.Info("geoipdb: 下载成功", "sha256", sha)
			return
		}
		slog.Warn("geoipdb: 下载失败", "url", src, "err", err)
	}
	return "", "", fmt.Errorf("geoipdb: 所有下载源均失败: %w", err)
}

func (u *Updater) downloadOne(ctx context.Context, url string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(u.dataDir, 0755); err != nil {
		return "", "", err
	}
	dst := filepath.Join(u.dataDir, "geoip.dat.tmp")
	f, err := os.Create(dst)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(dst)
		return "", "", err
	}
	f.Close()

	sum := fmt.Sprintf("%x", h.Sum(nil))
	final := filepath.Join(u.dataDir, "geoip.dat")
	if err := os.Rename(dst, final); err != nil {
		return "", "", err
	}
	_ = os.WriteFile(filepath.Join(u.dataDir, "geoip.dat.sha256"), []byte(sum), 0644)
	return final, sum, nil
}

// DataPath 返回 geoip.dat 文件路径。
func (u *Updater) DataPath() string {
	return filepath.Join(u.dataDir, "geoip.dat")
}

// CurrentSHA256 返回当前 geoip.dat 的 SHA256（如果存在）。
func (u *Updater) CurrentSHA256() string {
	data, err := os.ReadFile(filepath.Join(u.dataDir, "geoip.dat.sha256"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Exists 检查 geoip.dat 是否已存在。
func (u *Updater) Exists() bool {
	_, err := os.Stat(u.DataPath())
	return err == nil
}

// ComputeSHA256 计算本地 geoip.dat 的 SHA256 并写入 .sha256 文件。
func (u *Updater) ComputeSHA256() (string, int64, error) {
	f, err := os.Open(u.DataPath())
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}
	sum := fmt.Sprintf("%x", h.Sum(nil))
	_ = os.WriteFile(filepath.Join(u.dataDir, "geoip.dat.sha256"), []byte(sum), 0644)
	return sum, info.Size(), nil
}
