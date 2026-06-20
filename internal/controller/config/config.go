// Package config 提供 CoreLink controller 配置加载与默认值。
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config controller 全局配置。
type Config struct {
	// 数据库
	DBDSN string `json:"DBDSN"`

	// 统一监听地址（gRPC + HTTP 共享同一端口，VerifyClientCertIfGiven）
	ListenAddr string `json:"ListenAddr"`

	// 兼容旧配置（已废弃，优先使用 ListenAddr）
	GRPCEnrollAddr string `json:"GRPCEnrollAddr,omitempty"`
	GRPCAddr       string `json:"GRPCAddr,omitempty"`
	HTTPAddr       string `json:"HTTPAddr,omitempty"`

	// 虚拟网段（CIDR）
	VirtualCIDR string `json:"VirtualCIDR"`

	// CA 主体名
	CASubject string `json:"CASubject"`

	// 外层 TLS 配置
	TLSMode        string   `json:"TLSMode"`        // "self-signed" | "acme"
	ACMEDomains    []string `json:"ACMEDomains"`    // ACME 模式下的域名列表
	ACMECacheDir   string   `json:"ACMECacheDir"`   // ACME 证书缓存目录
	SelfSignedHost string   `json:"SelfSignedHost"` // 自签证书 Host/IP

	// CAEncKey 运行时注入的 CA 私钥加密密钥（AES-256-GCM，32 字节）。
	// 不从 JSON 配置文件加载——由上层从 DB 读取后赋值。
	CAEncKey []byte `json:"-"`

	// ─── 管理面（S6 console/CLI，spec §9）─────────────────────────────────────
	// 管理 HTTP 监听地址（与节点 mTLS 端口分离）。
	AdminAddr string `json:"AdminAddr"`
	// 初始管理员用户名。
	AdminUser string `json:"AdminUser"`
	// 会话 token 的 HMAC-SHA256 签名密钥（建议 >=32 字节）。
	// 配置文件中为 base64 编码的 JSON 字节数组；为空时由上层随机生成。
	AdminTokenKey []byte `json:"AdminTokenKey"`
}

// defaultConfig 返回带默认值的配置。
func defaultConfig() *Config {
	return &Config{
		DBDSN:          "sqlite://corelink.db",
		ListenAddr:     ":7443",
		VirtualCIDR:    "100.64.0.0/10",
		CASubject:      "CoreLink Root CA",
		TLSMode:        "self-signed",
		SelfSignedHost: "localhost",
		// 管理面默认仅绑定本机回环：管理 token / 密码以明文 HTTP 传输，
		// 绑定 0.0.0.0 会让任意网络对端嗅探凭据。生产如需远程访问，
		// 应显式配置地址并在前置反代终止 TLS。
		AdminAddr: "127.0.0.1:8090",
		AdminUser: "admin",
	}
}

// Load 从 JSON 文件加载配置，未指定字段保留默认值，并校验关键字段。
func Load(path string) (*Config, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: 读取文件失败: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: JSON 解析失败: %w", err)
	}

	// 应用默认值（JSON 里空字段保留默认）
	if cfg.VirtualCIDR == "" {
		cfg.VirtualCIDR = "100.64.0.0/10"
	}
	// 兼容旧配置：如果有旧字段但没新字段，用旧字段
	if cfg.ListenAddr == "" {
		if cfg.GRPCEnrollAddr != "" {
			cfg.ListenAddr = cfg.GRPCEnrollAddr
		} else {
			cfg.ListenAddr = ":7443"
		}
	}
	// 旧字段统一指向新地址
	cfg.GRPCEnrollAddr = cfg.ListenAddr
	cfg.GRPCAddr = cfg.ListenAddr
	cfg.HTTPAddr = cfg.ListenAddr
	if cfg.TLSMode == "" {
		cfg.TLSMode = "self-signed"
	}
	if cfg.AdminAddr == "" {
		cfg.AdminAddr = "127.0.0.1:8090"
	}
	if cfg.AdminUser == "" {
		cfg.AdminUser = "admin"
	}

	return cfg, nil
}
