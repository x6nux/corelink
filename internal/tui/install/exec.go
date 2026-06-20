package install

import (
	"os"
	"os/exec"
)

// defaultExecCmd 是生产环境的命令执行实现。
func defaultExecCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
