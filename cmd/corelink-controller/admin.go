package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/x6nux/corelink/internal/controller/admin"
	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/config"
	"github.com/x6nux/corelink/internal/controller/configsvc"
	"github.com/x6nux/corelink/internal/controller/ingress"
	"github.com/x6nux/corelink/internal/controller/ipam"
	"github.com/x6nux/corelink/internal/controller/store"
)

// resolveAdminTokenKey 解析管理面 HMAC token 签名密钥。
func resolveAdminTokenKey(cfgKey []byte) ([]byte, error) {
	if len(cfgKey) > 0 {
		return cfgKey, nil
	}
	slog.Warn("未设置 AdminTokenKey，使用随机临时密钥（重启后管理会话失效，生产请设置稳定密钥）")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成随机管理 token 密钥失败: %w", err)
	}
	return key, nil
}

// generateRandomPassword 生成一个 24 字符 URL-safe 随机密码（约 144 bit 熵）。
func generateRandomPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ipamAdapterForAdmin 将 *ipam.Allocator 适配到 admin.IPAMIface。
type ipamAdapterForAdmin struct{ m *ipam.Allocator }

func (a *ipamAdapterForAdmin) Release(ip string) error { return a.m.Release(ip) }

// buildAdminServer 构造管理面 HTTP server 及其监听器。
//
// 管理员密码哈希从 DB（AdminCredential）读取，不从配置文件读取。
// DB 无记录时自动生成随机强密码、bcrypt 哈希后写入 DB，密码仅输出一次。
func buildAdminHandler(
	cfg *config.Config,
	st *store.Store,
	caM *ca.Manager,
	ipamA *ipam.Allocator,
	notify *configsvc.Notify,
	receiver *ingress.Receiver,
) (http.Handler, error) {
	tokenKey, err := resolveAdminTokenKey(cfg.AdminTokenKey)
	if err != nil {
		return nil, err
	}

	passHash, err := resolveAdminPassHash(st, cfg.AdminUser)
	if err != nil {
		return nil, err
	}

	auth, err := admin.NewAuthenticator(cfg.AdminUser, passHash, tokenKey, 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("构建管理员认证器失败: %w", err)
	}

	adminHash := auth.PassHashPrefix(8)
	slog.Info("管理员认证器初始化完成", "user", cfg.AdminUser, "pass_hash_prefix", adminHash)

	caAdapter := admin.NewCAAdapter(caM)

	deps := admin.Deps{
		Auth:     auth,
		Store:    st,
		CA:       caAdapter,
		IPAM:     &ipamAdapterForAdmin{m: ipamA},
		Online:   notify,
		Notify:   notify,
		Topology: receiver,
	}
	return admin.NewAdminServer(deps), nil
}

// resolveAdminPassHash 从 DB 读取管理员密码哈希。
// DB 无记录时自动生成随机强密码 → bcrypt → 存 DB → 终端输出一次。
func resolveAdminPassHash(st *store.Store, username string) ([]byte, error) {
	cred, err := st.GetAdminCredential(username)
	if err != nil {
		return nil, fmt.Errorf("读取管理员凭据: %w", err)
	}
	if cred != nil && cred.PassHash != "" {
		return []byte(cred.PassHash), nil
	}

	// DB 无记录——首次启动，生成随机密码
	password, err := generateRandomPassword()
	if err != nil {
		return nil, fmt.Errorf("生成随机管理员密码: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt 哈希: %w", err)
	}

	if err := st.UpsertAdminCredential(&store.AdminCredential{
		Username: username,
		PassHash: string(hash),
	}); err != nil {
		return nil, fmt.Errorf("存储管理员凭据: %w", err)
	}

	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────┐")
	fmt.Printf("  │  管理员账号: %-32s │\n", username)
	fmt.Printf("  │  管理员密码: %-32s │\n", password)
	fmt.Println("  │                                             │")
	fmt.Println("  │  ⚠ 密码仅显示一次，请立即记录！             │")
	fmt.Println("  │  重置密码: corelink-controller passwd       │")
	fmt.Println("  └─────────────────────────────────────────────┘")
	fmt.Println()

	return hash, nil
}
