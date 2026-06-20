//go:build linux

package firewall

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ExecRunner 使用 os/exec 执行系统命令。
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("firewall: %s %s 失败: %s (%w)", name, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}
