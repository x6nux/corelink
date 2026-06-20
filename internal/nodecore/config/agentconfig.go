// Package config 提供 agent/relay 引导配置（S3-P1）。
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Role 节点角色（统一为 "node"）。
type Role string

const RoleNode Role = "node"

// Config agent/relay 启动时从文件加载的引导配置。
// 不包含已注册后的证书/IP（那些由 keystore 持久化）。
type Config struct {
	// Controller 简写：只写主机名/IP，自动展开为 enroll(:7443)/mtls(:7444)/http(:8080)。
	// 优先于下面三个具体地址字段。
	Controller string `json:"controller,omitempty"`
	// ControllerEnrollAddr controller 外层 TLS 注册端口（gRPC），例如 "controller.example.com:7443"
	ControllerEnrollAddr string `json:"controller_enroll_addr"`
	// ControllerMTLSAddr controller 内层 mTLS 端口（gRPC），例如 "controller.example.com:7444"
	ControllerMTLSAddr string `json:"controller_mtls_addr"`
	// ControllerHTTPAddr controller HTTP 端口，用于配置拉取，例如 "controller.example.com:7080"
	ControllerHTTPAddr string `json:"controller_http_addr"`
	// EnrollmentKey 一次性/可复用注册密钥（controller 侧创建）
	EnrollmentKey string `json:"enrollment_key"`
	// ControllerCAHash controller CA 公钥(SPKI)的 SHA-256 哈希，格式 "sha256:<hex>"。
	// 信任锚：node 用它验证 controller 服务端证书链到该 CA（公开值，可明文分发）。
	ControllerCAHash string `json:"controller_ca_hash"`
	// DataDir 本地数据目录，存放 WG 密钥、节点证书等持久化文件。
	DataDir string `json:"data_dir"`
	// Role 已废弃，保留字段避免 JSON 解析报错，值固定 "node"。
	Role Role `json:"role,omitempty"`
	// Hostname 注册时上报给 controller 的主机名。
	// 若为空，将在注册时使用 os.Hostname()。
	Hostname string `json:"hostname,omitempty"`
	// TUNName TUN 设备名称。默认 "corelink%d"（内核自动编号）。
	TUNName string `json:"tun_name,omitempty"`
	// Ingresses 静态配置的接入点（IP 直连 / CDN 边缘）。可选，omitempty 保持向后兼容。
	// 由 ConfigIngresses 转换为 genv1.Ingress 参与候选入口集合并。
	Ingresses []IngressConfig `json:"ingresses,omitempty"`
	// TUNMtu TUN 设备 MTU。仅接受预设档位：0(默认1400)/1400/1500/9000/65535。
	TUNMtu uint32 `json:"tun_mtu,omitempty"`
}

// defaultDataDir 返回当前平台的默认数据目录。
func defaultDataDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/CoreLink"
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "CoreLink")
	default:
		return "/var/lib/corelink"
	}
}

// defaultTUNName 返回当前平台的默认 TUN 设备名。
func defaultTUNName() string {
	switch runtime.GOOS {
	case "darwin":
		return "utun" // 系统自动分配编号
	case "windows":
		return "CoreLink"
	default:
		return "corelink%d"
	}
}

// applyDefaults 为未设置的字段填充默认值。
func (c *Config) applyDefaults() {
	// Controller 简写展开：所有服务共享同一端口。
	// Controller 字段可以是 "host" 或 "host:port"（含端口时直接用，否则默认 :7443）
	if c.Controller != "" {
		addr := c.Controller
		if !strings.Contains(addr, ":") {
			addr += ":7443"
		}
		if c.ControllerEnrollAddr == "" {
			c.ControllerEnrollAddr = addr
		}
		if c.ControllerMTLSAddr == "" {
			c.ControllerMTLSAddr = addr
		}
		if c.ControllerHTTPAddr == "" {
			c.ControllerHTTPAddr = "https://" + addr
		}
	}
	if c.DataDir == "" {
		c.DataDir = defaultDataDir()
	}
	c.Role = RoleNode
	if c.TUNName == "" {
		c.TUNName = defaultTUNName()
	}
}

// Validate 校验必填字段与枚举值。
func (c *Config) Validate() error {
	if c.ControllerEnrollAddr == "" {
		return errors.New("agentconfig: controller_enroll_addr 不能为空")
	}
	if c.ControllerMTLSAddr == "" {
		return errors.New("agentconfig: controller_mtls_addr 不能为空")
	}
	if c.ControllerHTTPAddr == "" {
		return errors.New("agentconfig: controller_http_addr 不能为空")
	}
	if c.EnrollmentKey == "" {
		return errors.New("agentconfig: enrollment_key 不能为空")
	}
	if c.ControllerCAHash == "" {
		return errors.New("agentconfig: controller_ca_hash 不能为空")
	}
	// DataDir 校验：非空且必须为绝对路径，防止相对路径导致证书/密钥存储位置不可预期
	if c.DataDir == "" {
		return errors.New("agentconfig: data_dir 不能为空")
	}
	if !filepath.IsAbs(c.DataDir) {
		return errors.New("agentconfig: data_dir 必须为绝对路径")
	}
	// role 已统一为 "node"，无需校验
	if err := ValidateMTU(c.TUNMtu); err != nil {
		return err
	}
	return nil
}

// Load 从 JSON 文件加载配置，应用默认值并校验必填字段后返回。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}
