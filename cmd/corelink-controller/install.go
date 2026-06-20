package main

import (
	"flag"

	"github.com/x6nux/corelink/internal/tui/install"
)

const (
	ctrlBinaryDest    = "/usr/local/bin/corelink-controller"
	ctrlDataDir       = "/var/lib/corelink-controller"
	ctrlServiceName   = "corelink-controller"
	ctrlConfigDefault = "/etc/corelink-controller.json"
)

func controllerInstallConfig(configPath string) install.InstallConfig {
	return install.InstallConfig{
		BinaryName:   "corelink-controller",
		BinaryDest:   ctrlBinaryDest,
		DataDir:      ctrlDataDir,
		ConfigPath:   configPath,
		ServiceName:  ctrlServiceName,
		UnitContent:  install.UnitContent(ctrlBinaryDest, configPath, ctrlDataDir, "Controller"),
		ConfigFn:     runControllerConfig,
		PostConfigFn: ensureControllerSecrets,
	}
}

func runControllerInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	configPath := fs.String("config", ctrlConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Run(controllerInstallConfig(*configPath))
}

func runControllerUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	configPath := fs.String("config", ctrlConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Uninstall(controllerInstallConfig(*configPath))
}

func runControllerUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	configPath := fs.String("config", ctrlConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Update(controllerInstallConfig(*configPath))
}

func runControllerReinstall(args []string) error {
	fs := flag.NewFlagSet("reinstall", flag.ContinueOnError)
	configPath := fs.String("config", ctrlConfigDefault, "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return install.Reinstall(controllerInstallConfig(*configPath))
}
