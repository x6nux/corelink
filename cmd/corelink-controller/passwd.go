package main

import (
	"flag"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/x6nux/corelink/internal/controller/config"
	"github.com/x6nux/corelink/internal/controller/store"
)

// runControllerPasswd 随机生成新管理员密码并更新数据库。
// 密码由系统随机生成（约 144 bit 熵），禁止用户指定——消除弱密码风险。
func runControllerPasswd(args []string) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	configPath := fs.String("config", ctrlConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 加载配置以获取 DBDSN 和 AdminUser
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}

	st, err := store.Open(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	if err := st.Migrate(); err != nil {
		return fmt.Errorf("迁移数据库: %w", err)
	}

	password, err := generateRandomPassword()
	if err != nil {
		return fmt.Errorf("生成密码: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("哈希密码: %w", err)
	}

	if err := st.UpsertAdminCredential(&store.AdminCredential{
		Username: cfg.AdminUser,
		PassHash: string(hash),
	}); err != nil {
		return fmt.Errorf("更新数据库: %w", err)
	}

	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Printf("│  管理员账号: %-32s │\n", cfg.AdminUser)
	fmt.Printf("│  新密码:     %-32s │\n", password)
	fmt.Println("│                                             │")
	fmt.Println("│  ⚠ 密码仅显示一次，请立即记录！             │")
	fmt.Println("└─────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("请重启服务使新密码生效: systemctl restart corelink-controller")

	return nil
}
