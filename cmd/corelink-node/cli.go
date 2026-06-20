package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/x6nux/corelink/internal/rpc"
	"github.com/x6nux/corelink/internal/rpc/nodemethods"
)

const nodeSockPath = "/var/run/corelink-node.sock"

// ─── mtr 子命令 ─────────────────────────────────────────────────────────────

func runCLIMTR(args []string) error {
	// 子命令分发：mtr enum <target>
	if len(args) > 0 && args[0] == "enum" {
		return runCLIMTREnum(args[1:])
	}

	fs := flag.NewFlagSet("mtr", flag.ContinueOnError)
	count := fs.Int("c", 10, "每跳探测次数")
	viaStr := fs.String("via", "", "指定中继路径（逗号分隔 nodeID/VIP/前缀），如 --via 100,101")
	replyMode := fs.String("reply", "", "回包模式: auto(走自然路由) 或 trace(原路返回)，默认: via时trace、auto时auto")
	outputFmt := fs.String("o", "table", "输出格式: table|json")
	sock := fs.String("sock", nodeSockPath, "节点 RPC socket 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "用法: corelink-node mtr <target> [-c 10] [--via node1,node2] [-o table|json]")
		return fmt.Errorf("缺少目标参数")
	}
	target := fs.Arg(0)

	// 解析 --via 为节点列表
	var via []string
	if *viaStr != "" {
		for _, v := range strings.Split(*viaStr, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				via = append(via, v)
			}
		}
	}

	c, err := rpc.Dial(*sock)
	if err != nil {
		return fmt.Errorf("连接节点 RPC 失败: %w", err)
	}
	defer c.Close()

	params := map[string]any{"target": target, "count": *count}
	if len(via) > 0 {
		params["via"] = via
	}
	if *replyMode != "" {
		params["reply_mode"] = *replyMode
	}
	var result nodemethods.MTRResult
	if err := c.Call("debug.mtr", params, &result); err != nil {
		return fmt.Errorf("MTR 追踪失败: %w", err)
	}

	if *outputFmt == "json" {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	printMTRResult(os.Stdout, &result)
	return nil
}

func printMTRResult(w io.Writer, r *nodemethods.MTRResult) {
	fmt.Fprintf(w, "CoreLink MTR — %s → %s (via %s)\n", r.Source, r.Target, r.Via)
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
	fmt.Fprintf(w, "%-4s %-28s  %6s  %4s  %7s  %7s  %7s  %7s  %7s\n",
		"HOP", "HOST", "Loss%", "Snt", "Last", "Avg", "Best", "Wrst", "StDev")
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))

	for _, h := range r.Hops {
		host := h.VIP
		if h.Hostname != "" {
			host = fmt.Sprintf("%s (%s)", h.Hostname, h.VIP)
		} else if h.NodeID != "" && len(h.NodeID) > 8 {
			host = fmt.Sprintf("%s (%s)", h.NodeID[:8], h.VIP)
		}

		if h.Hop == 1 {
			fmt.Fprintf(w, "%-4d %-28s  %5.1f%%  %4d  %7.1f  %7.1f  %7.1f  %7.1f  %7.1f\n",
				h.Hop, host, 0.0, h.Sent, 0.0, 0.0, 0.0, 0.0, 0.0)
			continue
		}
		if h.Recv == 0 {
			fmt.Fprintf(w, "%-4d %-28s  %5.1f%%  %4d  %7s  %7s  %7s  %7s  %7s\n",
				h.Hop, host, 100.0, h.Sent, "???", "???", "???", "???", "???")
			continue
		}
		stdev := h.StdevMs
		if math.IsNaN(stdev) || math.IsInf(stdev, 0) {
			stdev = 0
		}
		fmt.Fprintf(w, "%-4d %-28s  %5.1f%%  %4d  %7.1f  %7.1f  %7.1f  %7.1f  %7.1f\n",
			h.Hop, host, h.LossPct, h.Sent, h.LastMs, h.AvgMs, h.BestMs, h.WorstMs, stdev)
	}
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", 90))
}

// ─── mtr enum 子命令 ─────────────────────────────────────────────────────────

func runCLIMTREnum(args []string) error {
	fs := flag.NewFlagSet("mtr enum", flag.ContinueOnError)
	outputFmt := fs.String("o", "table", "输出格式: table|json")
	sock := fs.String("sock", nodeSockPath, "节点 RPC socket 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	c, err := rpc.Dial(*sock)
	if err != nil {
		return fmt.Errorf("连接节点 RPC 失败: %w", err)
	}
	defer c.Close()

	// 无参数：穷举到所有 peer 的路由
	if fs.NArg() < 1 {
		var allResult nodemethods.MTREnumAllResult
		if err := c.Call("debug.mtr_enum_all", nil, &allResult); err != nil {
			return fmt.Errorf("MTR 全量穷举失败: %w", err)
		}
		if *outputFmt == "json" {
			return json.NewEncoder(os.Stdout).Encode(allResult)
		}
		for i, r := range allResult.Results {
			if i > 0 {
				fmt.Println()
			}
			printMTREnumResult(os.Stdout, &r)
		}
		return nil
	}

	// 有参数：穷举到指定目标
	target := fs.Arg(0)
	var result nodemethods.MTREnumResult
	if err := c.Call("debug.mtr_enum", map[string]any{"target": target}, &result); err != nil {
		return fmt.Errorf("MTR 穷举失败: %w", err)
	}
	if *outputFmt == "json" {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	printMTREnumResult(os.Stdout, &result)
	return nil
}

func printMTREnumResult(w io.Writer, r *nodemethods.MTREnumResult) {
	fmt.Fprintf(w, "CoreLink Route Enum — %s → %s\n", r.Source, r.Target)
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", 50))
	fmt.Fprintf(w, "  %-30s %8s\n", "ROUTE", "RTT(ms)")
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", 50))

	bestIdx := -1
	bestRTT := math.MaxFloat64
	for i, rt := range r.Routes {
		if !rt.Loss && rt.RTTMs < bestRTT {
			bestRTT = rt.RTTMs
			bestIdx = i
		}
	}

	for i, rt := range r.Routes {
		mark := " "
		if i == bestIdx {
			mark = "*"
		}
		if rt.Loss {
			fmt.Fprintf(w, "%s %-30s %8s\n", mark, rt.Label, "FAIL")
		} else {
			fmt.Fprintf(w, "%s %-30s %8.1f\n", mark, rt.Label, rt.RTTMs)
		}
	}
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", 50))
	if bestIdx >= 0 {
		fmt.Fprintf(w, "Best: %s — %.1fms\n", r.Routes[bestIdx].Label, bestRTT)
	}
}

// ─── debug 子命令 ────────────────────────────────────────────────────────────

func runCLIDebug(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `用法: corelink-node debug <command>

命令:
  block <peer>       屏蔽 peer 直连调度（强制走中继）
  unblock <peer>     恢复 peer 直连调度
  list-blocked       列出所有被屏蔽的 peer

<peer> 支持: 完整 nodeID、nodeID 前缀、VIP 地址`)
		return nil
	}

	sock := nodeSockPath
	switch args[0] {
	case "block":
		if len(args) < 2 {
			return fmt.Errorf("用法: corelink-node debug block <peer_id|vip>")
		}
		return debugBlockPeer(sock, args[1])
	case "unblock":
		if len(args) < 2 {
			return fmt.Errorf("用法: corelink-node debug unblock <peer_id|vip>")
		}
		return debugUnblockPeer(sock, args[1])
	case "list-blocked":
		return debugListBlocked(sock)
	default:
		return fmt.Errorf("未知 debug 子命令: %s", args[0])
	}
}

func debugBlockPeer(sock, peer string) error {
	c, err := rpc.Dial(sock)
	if err != nil {
		return fmt.Errorf("连接节点 RPC 失败: %w", err)
	}
	defer c.Close()

	var result map[string]string
	if err := c.Call("debug.block_peer", map[string]string{"peer_id": peer}, &result); err != nil {
		return fmt.Errorf("屏蔽失败: %w", err)
	}
	fmt.Printf("已屏蔽 peer %s 的直连调度\n", result["peer_id"])
	return nil
}

func debugUnblockPeer(sock, peer string) error {
	c, err := rpc.Dial(sock)
	if err != nil {
		return fmt.Errorf("连接节点 RPC 失败: %w", err)
	}
	defer c.Close()

	var result map[string]string
	if err := c.Call("debug.unblock_peer", map[string]string{"peer_id": peer}, &result); err != nil {
		return fmt.Errorf("恢复失败: %w", err)
	}
	fmt.Printf("已恢复 peer %s 的直连调度\n", result["peer_id"])
	return nil
}

func debugListBlocked(sock string) error {
	c, err := rpc.Dial(sock)
	if err != nil {
		return fmt.Errorf("连接节点 RPC 失败: %w", err)
	}
	defer c.Close()

	var result struct {
		Blocked []string `json:"blocked"`
	}
	if err := c.Call("debug.list_blocked", nil, &result); err != nil {
		return fmt.Errorf("查询失败: %w", err)
	}
	if len(result.Blocked) == 0 {
		fmt.Println("无屏蔽的 peer")
		return nil
	}
	fmt.Println("当前屏蔽的 peer:")
	for _, id := range result.Blocked {
		fmt.Printf("  - %s\n", id)
	}
	return nil
}
