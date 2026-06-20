package install

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/x6nux/corelink/internal/rpc"
)

// 编译时通过 -ldflags 注入。
var (
	Version   = "dev"
	CommitSHA = "unknown"
	BuildTime = "unknown"
)

// PrintVersion 打印版本信息。
func PrintVersion(binary string) {
	fmt.Printf("%s %s\n", binary, Version)
	fmt.Printf("  commit:  %s\n", CommitSHA)
	fmt.Printf("  built:   %s\n", BuildTime)
	fmt.Printf("  go:      %s\n", runtime.Version())
	fmt.Printf("  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

// PrintHelp 打印命令帮助。
func PrintHelp(binary string, extra string) {
	fmt.Printf("用法: %s <命令> [选项]\n\n", binary)
	fmt.Println("服务管理:")
	fmt.Println("  serve          启动服务（默认）")
	fmt.Println("  start          启动守护进程")
	fmt.Println("  stop           停止守护进程")
	fmt.Println("  restart        重启守护进程")
	fmt.Println("  log            查看实时日志")
	fmt.Println("  status         快速查看运行状态")
	fmt.Println()
	fmt.Println("安装管理:")
	fmt.Println("  install        安装为 systemd 服务")
	fmt.Println("  uninstall      卸载服务")
	fmt.Println("  update         更新到最新版本")
	fmt.Println("  reinstall      重新安装")
	fmt.Println("  enable         设为开机自启")
	fmt.Println("  disable        取消开机自启")
	fmt.Println()
	fmt.Println("配置与管理:")
	fmt.Println("  config         交互式配置向导")
	fmt.Println("  tui            TUI 管理面板")
	fmt.Println("  info           显示本机信息")
	fmt.Println("  doctor         诊断检查")
	fmt.Println()
	fmt.Println("其它:")
	fmt.Println("  version        显示版本信息")
	fmt.Println("  help           显示此帮助")
	if extra != "" {
		fmt.Println()
		fmt.Println(extra)
	}
}

// PrintStatus 连接 Unix socket 获取快速状态（一行输出）。
func PrintStatus(binary, sockPath string) error {
	c, err := rpc.Dial(sockPath)
	if err != nil {
		fmt.Printf("%s: 未运行（无法连接 %s）\n", binary, sockPath)
		return nil
	}
	defer c.Close()

	var result map[string]any
	if err := c.Call("system.status", nil, &result); err != nil {
		fmt.Printf("%s: 运行中，但状态查询失败: %v\n", binary, err)
		return nil
	}

	// 格式化输出
	role, _ := result["role"].(string)
	if role == "" {
		role = "-"
	}
	uptime, _ := result["uptime_seconds"].(float64)
	uptimeStr := formatDuration(time.Duration(uptime) * time.Second)

	nodeCnt, _ := result["node_count"].(float64)
	onlineCnt, _ := result["online_count"].(float64)
	topoVer, _ := result["topo_version"].(float64)
	nodeID, _ := result["node_id"].(string)
	vip, _ := result["vip"].(string)
	connected, _ := result["connected"].(bool)

	if nodeID != "" {
		// Node 模式
		connStr := "已连接"
		if !connected {
			connStr = "未连接"
		}
		fmt.Printf("%s: 运行中 | 节点=%s | IP=%s | 角色=%s | 拓扑=%v | 控制器=%s | 运行=%s\n",
			binary, nodeID, vip, role, topoVer, connStr, uptimeStr)
	} else {
		// Controller 模式
		fmt.Printf("%s: 运行中 | 节点=%v/%v在线 | 拓扑=%v | 运行=%s\n",
			binary, onlineCnt, nodeCnt, topoVer, uptimeStr)
	}
	return nil
}

// PrintInfo 从本地 keystore 读取节点信息（不需要服务运行）。
func PrintInfo(binary, dataDir string) {
	fmt.Printf("%s 本机信息\n\n", binary)

	// 读 identity JSON（如果存在）
	idPath := dataDir + "/identity.json"
	data, err := os.ReadFile(idPath)
	if err != nil {
		fmt.Printf("  数据目录: %s\n", dataDir)
		fmt.Printf("  状态: 未注册（%s 不存在）\n", idPath)
		return
	}

	var info map[string]any
	if err := json.Unmarshal(data, &info); err != nil {
		fmt.Printf("  数据目录: %s\n", dataDir)
		fmt.Printf("  身份文件: %s（解析失败: %v）\n", idPath, err)
		return
	}

	fmt.Printf("  数据目录:   %s\n", dataDir)
	if nodeID, ok := info["node_id"].(string); ok {
		fmt.Printf("  节点 ID:    %s\n", nodeID)
	}
	// 检查证书文件
	certPath := dataDir + "/node.crt"
	if _, err := os.Stat(certPath); err == nil {
		fmt.Printf("  节点证书:   %s\n", certPath)
	}
	keyPath := dataDir + "/node.key"
	if _, err := os.Stat(keyPath); err == nil {
		fmt.Printf("  节点密钥:   %s\n", keyPath)
	}
	wgPath := dataDir + "/wg.key"
	if _, err := os.Stat(wgPath); err == nil {
		fmt.Printf("  WG 密钥:    %s\n", wgPath)
	}
}

// RunDoctor 执行诊断检查。
func RunDoctor(binary string, checks []DoctorCheck) {
	fmt.Printf("%s 诊断检查\n\n", binary)
	passed, failed := 0, 0
	for _, ch := range checks {
		ok, detail := ch.Check()
		if ok {
			passed++
			fmt.Printf("  ✅ %s: %s\n", ch.Name, detail)
		} else {
			failed++
			fmt.Printf("  ❌ %s: %s\n", ch.Name, detail)
		}
	}
	fmt.Printf("\n结果: %d 通过, %d 失败\n", passed, failed)
}

// DoctorCheck 诊断检查项。
type DoctorCheck struct {
	Name  string
	Check func() (ok bool, detail string)
}

// CommonDoctorChecks 返回通用检查项。
func CommonDoctorChecks(configPath, sockPath string, controllerAddr string) []DoctorCheck {
	checks := []DoctorCheck{
		{
			Name: "配置文件",
			Check: func() (bool, string) {
				if _, err := os.Stat(configPath); err != nil {
					return false, fmt.Sprintf("%s 不存在", configPath)
				}
				return true, configPath
			},
		},
		{
			Name: "守护进程",
			Check: func() (bool, string) {
				c, err := rpc.Dial(sockPath)
				if err != nil {
					return false, fmt.Sprintf("无法连接 %s", sockPath)
				}
				c.Close()
				return true, "运行中"
			},
		},
	}
	if controllerAddr != "" {
		host, port, _ := strings.Cut(controllerAddr, ":")
		if port == "" {
			port = "7443"
		}
		addr := net.JoinHostPort(host, port)
		checks = append(checks, DoctorCheck{
			Name: "Controller 可达",
			Check: func() (bool, string) {
				conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
				if err != nil {
					return false, fmt.Sprintf("%s 连接失败: %v", addr, err)
				}
				conn.Close()
				return true, addr
			},
		})
	}
	return checks
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd%dh%dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
