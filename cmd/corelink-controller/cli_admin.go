package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	ctrlconfig "github.com/x6nux/corelink/internal/controller/config"
	"github.com/x6nux/corelink/internal/controller/store"
)

const defaultControllerConfig = "/etc/corelink-controller.json"

// openStore 加载 controller 配置并打开数据库。
func openStore() (*store.Store, error) {
	cfg, err := ctrlconfig.Load(defaultControllerConfig)
	if err != nil {
		return nil, fmt.Errorf("加载配置 %s 失败: %w", defaultControllerConfig, err)
	}
	s, err := store.Open(cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	return s, nil
}

// runDirectAdmin 解析管理子命令并直接调用 store。
func runDirectAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("缺少管理命令")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "node":
		return adminNode(rest)
	case "key":
		return adminKey(rest)
	case "acl":
		return adminACL(rest)
	case "cert":
		return adminCert(rest)
	case "ca":
		return adminCA(rest)
	case "relay":
		return adminRelay(rest)
	case "route":
		return adminRoute(rest)
	case "dns":
		return adminDNS(rest)
	case "split-tunnel":
		return adminSplitTunnel(rest)
	default:
		return fmt.Errorf("未知管理命令: %s\n可用: node key acl cert ca relay route dns split-tunnel", sub)
	}
}

// ─── node ────────────────────────────────────────────────────────────────────

func adminNode(args []string) error {
	if len(args) == 0 || args[0] == "ls" {
		s, err := openStore()
		if err != nil {
			return err
		}
		nodes, err := s.ListNodes()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tVIRTUAL_IP\tROLE\tREMARK")
		for _, n := range nodes {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				n.ID, n.Name, n.VirtualIP, n.Role, n.Remark)
		}
		return w.Flush()
	}
	switch args[0] {
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("用法: corelink-controller node rm <id|name>")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		n, err := s.ResolveNode(args[1])
		if err != nil {
			return err
		}
		if err := s.DeleteNode(n.ID); err != nil {
			return err
		}
		fmt.Printf("已删除节点 %s (%s)\n", n.ID, n.Name)
		return nil
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("用法: corelink-controller node show <id|name|vip>")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		n, err := s.ResolveNode(args[1])
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(n, "", "  ")
		fmt.Println(string(data))
		return nil
	case "rename":
		if len(args) < 3 {
			return fmt.Errorf("用法: corelink-controller node rename <id|name> <new_name>")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		n, err := s.ResolveNode(args[1])
		if err != nil {
			return err
		}
		if err := s.UpdateNodeMeta(n.ID, args[2], ""); err != nil {
			return err
		}
		fmt.Printf("节点 %s 已改名为 %s\n", n.ID, args[2])
		return nil
	case "remark":
		if len(args) < 3 {
			return fmt.Errorf("用法: corelink-controller node remark <id|name> <text>")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		n, err := s.ResolveNode(args[1])
		if err != nil {
			return err
		}
		if err := s.UpdateNodeMeta(n.ID, "", args[2]); err != nil {
			return err
		}
		fmt.Printf("节点 %s 备注已更新\n", n.ID)
		return nil
	default:
		return fmt.Errorf("未知 node 子命令: %s (可用: ls show rm rename remark)", args[0])
	}
}

// ─── key ─────────────────────────────────────────────────────────────────────

func adminKey(args []string) error {
	if len(args) == 0 || args[0] == "ls" {
		s, err := openStore()
		if err != nil {
			return err
		}
		keys, err := s.ListEnrollKeys()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "KEY\tTAG\tREUSABLE\tREVOKED\tEXPIRES")
		for _, k := range keys {
			exp := "—"
			if k.ExpiresAt != nil {
				exp = k.ExpiresAt.Format(time.DateTime)
			}
			fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%s\n",
				k.Key[:min(16, len(k.Key))], k.Tag, k.Reusable, k.Revoked, exp)
		}
		return w.Flush()
	}
	switch args[0] {
	case "create":
		s, err := openStore()
		if err != nil {
			return err
		}
		b := make([]byte, 16)
		if _, err = rand.Read(b); err != nil {
			return fmt.Errorf("生成密钥失败: %w", err)
		}
		ek := &store.EnrollKey{
			Key:      hex.EncodeToString(b),
			Reusable: true,
		}
		if err := s.CreateEnrollKey(ek); err != nil {
			return err
		}
		fmt.Printf("新注册密钥: %s\n", ek.Key)
		return nil
	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("用法: corelink-controller key revoke <key>")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		if err := s.RevokeEnrollKey(args[1]); err != nil {
			return err
		}
		fmt.Printf("已吊销密钥 %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("未知 key 子命令: %s (可用: ls create revoke)", args[0])
	}
}

// ─── acl ─────────────────────────────────────────────────────────────────────

func adminACL(args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "get" {
		policies, err := s.ListACLPolicies()
		if err != nil {
			return err
		}
		if len(policies) == 0 {
			fmt.Println("无 ACL 策略")
			return nil
		}
		p := policies[len(policies)-1]
		fmt.Printf("版本: %d  作者: %s\n---\n%s\n", p.Version, p.Author, p.Document)
		return nil
	}
	return fmt.Errorf("未知 acl 子命令: %s (可用: get)", args[0])
}

// ─── cert ────────────────────────────────────────────────────────────────────

func adminCert(args []string) error {
	if len(args) == 0 || args[0] == "ls" {
		s, err := openStore()
		if err != nil {
			return err
		}
		certs, err := s.ListCerts()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SERIAL\tNODE_ID\tNOT_AFTER\tREVOKED")
		for _, c := range certs {
			nid := c.NodeID
			if len(nid) > 12 {
				nid = nid[:12]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\n",
				c.Serial, nid, c.NotAfter.Format(time.DateOnly), c.Revoked)
		}
		return w.Flush()
	}
	switch args[0] {
	case "revoke":
		if len(args) < 2 {
			return fmt.Errorf("用法: corelink-controller cert revoke <serial>")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		if err := s.RevokeCert(args[1]); err != nil {
			return err
		}
		fmt.Printf("已吊销证书 %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("未知 cert 子命令: %s (可用: ls revoke)", args[0])
	}
}

// ─── ca ──────────────────────────────────────────────────────────────────────

func adminCA(_ []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	root, err := s.GetCARoot()
	if err != nil {
		return fmt.Errorf("读取 CA 失败: %w", err)
	}
	fmt.Printf("CA 证书:\n%s\n", string(root.CertPEM))
	return nil
}

// ─── relay ───────────────────────────────────────────────────────────────────

func adminRelay(args []string) error {
	if len(args) == 0 || args[0] == "ls" {
		s, err := openStore()
		if err != nil {
			return err
		}
		relays, err := s.ListRelayInfo()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NODE_ID\tENDPOINT\tPRIORITY")
		for _, r := range relays {
			nid := r.NodeID
			if len(nid) > 12 {
				nid = nid[:12]
			}
			fmt.Fprintf(w, "%s\t%s\t%d\n", nid, r.TunnelEndpoint, r.Priority)
		}
		return w.Flush()
	}
	return fmt.Errorf("未知 relay 子命令: %s (可用: ls)", args[0])
}

// ─── route ───────────────────────────────────────────────────────────────────

func adminRoute(args []string) error {
	if len(args) == 0 || args[0] == "ls" {
		s, err := openStore()
		if err != nil {
			return err
		}
		routes, err := s.ListAllPublishedRoutes()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNODE_ID\tKIND\tCIDR\tENABLED")
		for _, r := range routes {
			nid := r.NodeID
			if len(nid) > 12 {
				nid = nid[:12]
			}
			cidr := r.RouteCIDR
			if cidr == "" {
				cidr = r.VIPCIDR
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%v\n", r.ID, nid, r.Kind, cidr, r.Enabled)
		}
		return w.Flush()
	}
	return fmt.Errorf("未知 route 子命令: %s (可用: ls)", args[0])
}

// ─── split-tunnel ────────────────────────────────────────────────────────────

func adminSplitTunnel(args []string) error {
	if len(args) == 0 || args[0] == "ls" {
		s, err := openStore()
		if err != nil {
			return err
		}
		rules, err := s.ListSplitRules()
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			fmt.Println("无分流规则")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNODE_ID\tMATCH\tACTION\tEXIT_NODE\tORDER\tENABLED")
		for _, r := range rules {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%d\t%v\n",
				r.ID, r.NodeID, r.Match, r.Action, r.ExitNodeID, r.SortOrder, r.Enabled)
		}
		return w.Flush()
	}
	return fmt.Errorf("未知 split-tunnel 子命令: %s (可用: ls)", args[0])
}

// ─── dns ─────────────────────────────────────────────────────────────────────

func adminDNS(args []string) error {
	if len(args) == 0 || args[0] == "get" {
		s, err := openStore()
		if err != nil {
			return err
		}
		dns, err := s.GetDNSSettings()
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(dns, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	return fmt.Errorf("未知 dns 子命令: %s (可用: get)", args[0])
}
