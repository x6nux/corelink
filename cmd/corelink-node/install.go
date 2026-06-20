package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/x6nux/corelink/internal/nodecore/jointoken"
	"github.com/x6nux/corelink/internal/tui/install"
	"github.com/x6nux/corelink/internal/tui/wizard"
)

const (
	nodeBinaryDest    = "/usr/local/bin/corelink-node"
	nodeDataDir       = "/var/lib/corelink"
	nodeServiceName   = "corelink-node"
	nodeConfigDefault = "/etc/corelink-node.json"
)

// tokenConfigFn 返回一个 ConfigFn：解析入网 token 并写出 node 配置。
// 复用 install.Run 的 ConfigFn 契约（args 形如 {"--output", path}）。
func tokenConfigFn(token string) func(args []string) error {
	return func(args []string) error {
		fs := flag.NewFlagSet("config", flag.ContinueOnError)
		output := fs.String("output", nodeConfigDefault, "配置文件输出路径")
		if err := fs.Parse(args); err != nil {
			return err
		}
		jt, err := jointoken.Decode(token)
		if err != nil {
			return fmt.Errorf("解析入网 token 失败: %w", err)
		}
		data, err := wizard.TokenToConfigJSON(jt, "", "")
		if err != nil {
			return fmt.Errorf("生成配置失败: %w", err)
		}
		if err := os.WriteFile(*output, data, 0600); err != nil {
			return fmt.Errorf("写入配置文件 %s 失败: %w", *output, err)
		}
		fmt.Printf("配置已从 token 写入 %s\n", *output)
		return nil
	}
}

func nodeInstallConfig(configPath, token string) install.InstallConfig {
	configFn := runNodeConfig
	if token != "" {
		configFn = tokenConfigFn(token)
	}
	return install.InstallConfig{
		BinaryName:  "corelink-node",
		BinaryDest:  nodeBinaryDest,
		DataDir:     nodeDataDir,
		ConfigPath:  configPath,
		ServiceName: nodeServiceName,
		UnitContent: install.UnitContent(nodeBinaryDest, configPath, nodeDataDir, "Node",
			"AmbientCapabilities=CAP_NET_ADMIN"),
		ConfigFn: configFn,
	}
}

func runNodeInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	configPath := fs.String("config", nodeConfigDefault, "配置文件路径")
	token := fs.String("token", "", "入网 token（由 corelink key create 生成，非交互安装）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Run(nodeInstallConfig(*configPath, *token))
}

func runNodeUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	configPath := fs.String("config", nodeConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Uninstall(nodeInstallConfig(*configPath, ""))
}

func runNodeUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	configPath := fs.String("config", nodeConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Update(nodeInstallConfig(*configPath, ""))
}

func runNodeReinstall(args []string) error {
	fs := flag.NewFlagSet("reinstall", flag.ContinueOnError)
	configPath := fs.String("config", nodeConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Reinstall(nodeInstallConfig(*configPath, ""))
}
