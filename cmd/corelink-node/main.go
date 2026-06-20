// cmd/corelink-node 是 CoreLink 统一节点主程序（spec §1 合并二进制）。
//
// corelink-node = node-core（agent 数据面能力）+ relay 能力 + 自动智能并网。
// 每个节点都具备 agent+relay 全部能力；是否承担中转由 controller 下发的
// TopologyAssignment.Role 决定（LEAF 叶子接入 / TRANSIT 中转转发）。
//
// 用法：
//
//	corelink-node [-config path]
//
// 启动顺序（统一编排 runNode，复用 agent/relay 既有模式）：
//
//  1. 加载配置 + keystore + enroll（同 agent/relay）。
//  2. 构造 mTLS + 拉首次 NodeConfig。
//  3. 入口发现（ingress.Discover 5 路）+ gRPC 上报（IngressService.ReportIngress）。
//  4. L1 探测上报（probe.Reporter + 周期 Tick + 对 probe_targets 探测）。
//  5. 按角色装配子系统（roleFromConfig）：TRANSIT / LEAF / 基础 agent。
//  6. TopologyAssignment 消费 + 动态角色切换骨架（sync.Client.OnConfig）。
//  7. SIGINT/SIGTERM 优雅退出。
//
// 注：完整多节点端到端 mesh、真实 UDP 探测、角色翻转数据面不中断的完整验证留
// Task5.1（M4）。本程序聚焦装配 + 智能并网编排骨架；数据面正确性由既有
// internal 包的单测 / M3 集成测试覆盖。
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/protojson"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/featureflag"
	agentconfig "github.com/x6nux/corelink/internal/nodecore/config"
	"github.com/x6nux/corelink/internal/nodecore/connpool"
	"github.com/x6nux/corelink/internal/nodecore/dataplane"
	"github.com/x6nux/corelink/internal/nodecore/dnsproxy"
	"github.com/x6nux/corelink/internal/nodecore/dpi"
	"github.com/x6nux/corelink/internal/nodecore/enroll"
	"github.com/x6nux/corelink/internal/nodecore/firewall"
	"github.com/x6nux/corelink/internal/nodecore/flowtrack"
	"github.com/x6nux/corelink/internal/nodecore/geoip"
	"github.com/x6nux/corelink/internal/nodecore/ingress"
	"github.com/x6nux/corelink/internal/nodecore/keystore"
	"github.com/x6nux/corelink/internal/nodecore/location"
	"github.com/x6nux/corelink/internal/nodecore/multirelay"
	"github.com/x6nux/corelink/internal/nodecore/nodestore"
	"github.com/x6nux/corelink/internal/nodecore/portmap"
	"github.com/x6nux/corelink/internal/nodecore/probe"
	"github.com/x6nux/corelink/internal/nodecore/proberouter"
	"github.com/x6nux/corelink/internal/nodecore/route"
	"github.com/x6nux/corelink/internal/nodecore/splittunnel"
	nodesync "github.com/x6nux/corelink/internal/nodecore/sync"
	"github.com/x6nux/corelink/internal/nodecore/tun"
	"github.com/x6nux/corelink/internal/rpc"
	"github.com/x6nux/corelink/internal/rpc/nodemethods"
	"github.com/x6nux/corelink/internal/transport"
	"github.com/x6nux/corelink/internal/tui"
	"github.com/x6nux/corelink/internal/tui/install"
	tuinode "github.com/x6nux/corelink/internal/tui/node"
	"github.com/x6nux/corelink/internal/tui/wizard"
	"github.com/x6nux/corelink/internal/version"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

func main() {
	if err := run(os.Args[1:], tun.CreateReal); err != nil {
		slog.Error("corelink-node 退出", "err", err)
		os.Exit(1)
	}
}

// TRANSIT mesh 互联固定端口。relay server（节点接入）无需固定——其端口通过入口上报。
const defaultMeshPort = 7446

// TUNFactory 创建 TUN 设备的工厂函数，便于测试注入 fakeTUN。
type TUNFactory func(name string, mtu int) (tun.Device, error)

// run 是可测试入口，解析参数 + 信号 + 调用 runNode。
func run(args []string, tunFactory TUNFactory) error {
	// 在 flag 解析之前检查子命令
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			// serve 子命令：剥离 "serve" 后继续常规 flag 解析流程。
			args = args[1:]
			goto parseFlags
		case "tui":
			return runNodeTUI(args[1:])
		case "install":
			return runNodeInstall(args[1:])
		case "uninstall":
			return runNodeUninstall(args[1:])
		case "update":
			return runNodeUpdate(args[1:])
		case "reinstall":
			return runNodeReinstall(args[1:])
		case "config":
			return runNodeConfig(args[1:])
		case "start":
			return install.ServiceCmd("corelink-node", "start")
		case "stop":
			return install.ServiceCmd("corelink-node", "stop")
		case "restart":
			return install.ServiceCmd("corelink-node", "restart")
		case "log":
			return install.ServiceLog("corelink-node")
		case "enable":
			return install.ServiceEnable("corelink-node")
		case "disable":
			return install.ServiceDisable("corelink-node")
		case "status":
			return install.PrintStatus("corelink-node", "/var/run/corelink-node.sock")
		case "info":
			install.PrintInfo("corelink-node", "/var/lib/corelink")
			return nil
		case "doctor":
			install.RunDoctor("corelink-node", install.CommonDoctorChecks(
				"/etc/corelink-node.json", "/var/run/corelink-node.sock", ""))
			return nil
		case "version":
			install.PrintVersion("corelink-node")
			return nil
		case "mtr":
			return runCLIMTR(args[1:])
		case "debug":
			return runCLIDebug(args[1:])
		case "help", "-h", "--help":
			install.PrintHelp("corelink-node", "")
			return nil
		}
	}

parseFlags:
	fs := flag.NewFlagSet("corelink-node", flag.ContinueOnError)
	configPath := fs.String("config", "", "引导配置文件路径（JSON）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		if v := os.Getenv("CORELINK_NODE_CONFIG"); v != "" {
			*configPath = v
		}
	}
	if *configPath == "" {
		return fmt.Errorf("corelink-node: 必须通过 -config 指定引导配置文件")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		slog.Info("corelink-node: 收到退出信号，优雅退出…")
		cancel()
	}()

	return runNode(ctx, *configPath, tunFactory)
}

// runNode 执行统一节点核心编排（复用 agent/relay 既有模式）。
func runNode(ctx context.Context, cfgPath string, tunFactory TUNFactory) error {
	// ── 1. 加载配置 + keystore + enroll ──
	cfg, err := agentconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("corelink-node: 加载配置: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("corelink-node: 配置校验: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("corelink-node: 创建数据目录: %w", err)
	}
	ks := keystore.New(cfg.DataDir)

	slog.Info("corelink-node: 准备注册（或跳过）…")
	enrollCtx, enrollCancel := context.WithTimeout(ctx, 30*time.Second)
	defer enrollCancel()
	if err := enroll.Enroll(enrollCtx, cfg, ks); err != nil {
		return fmt.Errorf("corelink-node: 注册失败: %w", err)
	}
	slog.Info("corelink-node: 注册就绪")

	// ── 2. 构造 mTLS + 拉首次 NodeConfig ──
	id, err := ks.LoadIdentity()
	if err != nil {
		return fmt.Errorf("corelink-node: 加载身份: %w", err)
	}
	tlsCfg, err := buildMTLSFromIdentity(id, cfg)
	if err != nil {
		return fmt.Errorf("corelink-node: 构造 mTLS: %w", err)
	}

	fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
	defer fetchCancel()
	firstCfg, err := fetchNodeConfig(fetchCtx, cfg.ControllerHTTPAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("corelink-node: 拉取首次 NodeConfig: %w", err)
	}
	if firstCfg == nil {
		return fmt.Errorf("corelink-node: 首次 NodeConfig 为空")
	}
	slog.Info("corelink-node: 收到首次 NodeConfig",
		"gen", firstCfg.GetGeneration(),
		"role", roleFromConfig(firstCfg),
		"peers", len(firstCfg.GetPeers()))

	// gRPC 连接 1：IngressService 上报（入口发现/质量/事件，可后台异步使用）。
	ingressGRPC, err := grpc.NewClient(
		cfg.ControllerMTLSAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		return fmt.Errorf("corelink-node: dial controller gRPC (ingress): %w", err)
	}
	defer ingressGRPC.Close()
	ingressCli := genv1.NewIngressServiceClient(ingressGRPC)

	// gRPC 连接 2：RelayControlService + 其他主流程调用（与 ingress 隔离避免并发竞态）。
	grpcConn, err := grpc.NewClient(
		cfg.ControllerMTLSAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		return fmt.Errorf("corelink-node: dial controller gRPC: %w", err)
	}
	defer grpcConn.Close()

	// 数据面端口（用于入口发现时补全 NETIF 入口，其他节点通过此端口建数据面连接）。
	const dataPlanePort uint16 = dataplane.DefaultDataPlanePort

	// ── 3. 入口发现 + 上报 ──
	mapper := portmap.New(portmap.Config{})
	portmapFn := func(pctx context.Context) ([]*genv1.Ingress, error) {
		return ingress.PortmapDiscover(pctx, mapper, 0, uint16(dataPlanePort))
	}

	// ingressCache 线程安全缓存入口发现结果，供 TUI RPC 读取。
	var ingressMu sync.Mutex
	var cachedIngresses []*genv1.Ingress

	// 入口发现 + MachineSpec 上报后台执行（使用独立 gRPC 连接，不阻塞角色装配）。
	go func() {
		if err := discoverAndReportIngress(ctx, cfg, id.NodeID, ingressCli, dataPlanePort, portmapFn, func(set *genv1.IngressSet) {
			ingressMu.Lock()
			cachedIngresses = set.GetIngresses()
			ingressMu.Unlock()
		}); err != nil {
			slog.Warn("corelink-node: 入口发现/上报失败（继续）", "err", err)
		}
		if err := reportMachineSpec(ctx, id.NodeID, ingressCli); err != nil {
			slog.Warn("corelink-node: MachineSpec 上报失败（继续）", "err", err)
		}
	}()

	// Lifecycle 管理已建立的 portmap 映射续期 + OnMappingLost 触发重报。
	reReportGate := newReportGate(30*time.Second, time.Now)
	lifecycle := portmap.NewLifecycle(mapper, portmap.LifecycleConfig{
		OnMappingLost: func(m *portmap.Mapping) {
			slog.Warn("corelink-node: portmap 映射丢失，触发重报", "port", m.InternalPort, "ext", m.ExternalPort)
			if !reReportGate.Allow() {
				slog.Debug("corelink-node: 重报被节流（30s 内重复）")
				return
			}
			if err := discoverAndReportIngress(ctx, cfg, id.NodeID, ingressCli, dataPlanePort, portmapFn, func(set *genv1.IngressSet) {
				ingressMu.Lock()
				cachedIngresses = set.GetIngresses()
				ingressMu.Unlock()
			}); err != nil {
				slog.Warn("corelink-node: 重报入口失败", "err", err)
			}
		},
	})
	defer lifecycle.Close()

	// 初始 portmap Map 后台执行（数据面端口映射）。
	go func() {
		if dataPlanePort == 0 {
			return
		}
		mapCtx, mapCancel := context.WithTimeout(ctx, 5*time.Second)
		m, err := mapper.Map(mapCtx, uint16(dataPlanePort), false, 7200*time.Second)
		mapCancel()
		if err != nil {
			slog.Debug("corelink-node: portmap 初始映射失败（继续）", "port", dataPlanePort, "err", err)
			return
		}
		lifecycle.Manage(m)
	}()

	// Lifecycle Tick 驱动：定期推进续期/重建。
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				lifecycle.Tick(t)
			}
		}
	}()

	// ── 4. L1 探测上报（Reporter + 周期 Tick + 对 probe_targets 探测）──
	reporter := probe.NewReporter(probe.ReporterConfig{
		SelfNode: id.NodeID,
		Clock:    time.Now,
		Damping:  probe.DefaultQualityDamping(),
		EmitEvent: func(e *genv1.EdgeEvent) {
			ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if _, err := ingressCli.ReportEdgeEvent(ectx, e); err != nil {
				slog.Debug("corelink-node: ReportEdgeEvent 失败", "err", err)
			}
		},
		EmitQuality: func(q *genv1.QualityReport) {
			qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if _, err := ingressCli.ReportQuality(qctx, q); err != nil {
				slog.Debug("corelink-node: ReportQuality 失败", "err", err)
			}
		},
	})
	// probe_targets 持有当前下发的探测目标（OnConfig 动态更新）。
	var probeMu sync.RWMutex
	probeTargets := probeTargetsFromConfig(firstCfg.GetTopology())
	// 构造 TCP 探测器替代占位 prober——通过 TCP 三次握手测量真实 RTT。
	tcpProber := probe.NewTCPProber(probe.TCPProbeConfig{})
	go driveProbeLoop(ctx, reporter, tcpProber.Probe, func() []probeTarget {
		probeMu.RLock()
		defer probeMu.RUnlock()
		return probeTargets
	})

	// ── 5. 按角色装配子系统 ──
	relayCli := genv1.NewRelayControlServiceClient(grpcConn)
	assembler := &realAssembler{
		ks:         ks,
		id:         id,
		cfg:        cfg,
		tlsCfg:     tlsCfg,
		tunFactory: tunFactory,
		relayCli:   relayCli,
		ingressCli: ingressCli,
		geoFlush:   make(chan struct{}, 1),
		fwMgr:      firewall.New(),
		ctx:        ctx,
	}
	// 提前注册 defer：无论角色装配成功或失败，runNode 退出时都回收 assembler 已建
	// 子系统（如装配中途已起 node-core / interconnect 后失败）。Close 已对各字段
	// nil 守卫且可重复调用，对成功路径与部分装配路径都安全（#29）。
	defer assembler.Close()
	// 启用 VIP 路由模式：跳过信封编解码，Bind 直投 interconnect，relay 直通 LoopbackConn。
	globalFlags := featureflag.FromMap(map[string]bool{
		featureflag.VIPRouting: true,
		featureflag.TLS0RTT:    true,
	})
	if err := assembleByRole(ctx, assembler, id.NodeID, firstCfg, globalFlags); err != nil {
		return fmt.Errorf("corelink-node: 角色装配: %w", err)
	}

	// ── 5c. TRANSIT 多 Relay 三维质量探测 ──
	if roleFromConfig(firstCfg) == genv1.NodeTopoRole_NODE_TOPO_ROLE_TRANSIT {
		// 多 Relay 三维质量探测（E2）：经每个 Relay 对每个 peer 探测，上报完整质量矩阵。
		// 本地 relay 用 keepalive RTT（路径保温 echo），其他 relay 用 TCP 探测。
		relayIDs := make([]string, 0, len(firstCfg.GetRelays()))
		relayAddrs := make(map[string]string) // relayID → 可探测地址
		for _, r := range firstCfg.GetRelays() {
			udp := r.GetUdp()
			if udp == nil || udp.GetHost() == "" {
				continue
			}
			rid := r.GetRelayId()
			relayIDs = append(relayIDs, rid)
			if tAddr := tunnelAddrOf(r); tAddr != "" {
				relayAddrs[rid] = tAddr
			} else {
				relayAddrs[rid] = fmt.Sprintf("%s:%d", udp.GetHost(), udp.GetPort())
			}
		}
		selfRelayID := id.NodeID
		relayTCPProber := probe.NewTCPProber(probe.TCPProbeConfig{})
		mrp := probe.NewMultiRelayProber(
			func(relayID, targetNodeID string) (uint32, uint32, bool) {
				// 本地 relay：回环 ≈ 1ms 兜底
				if relayID == selfRelayID {
					return 1, 0, true
				}
				// 其他 relay：TCP 3-way handshake RTT
				if addr, ok := relayAddrs[relayID]; ok {
					rttMs, _, ok := relayTCPProber.Probe(addr)
					return rttMs, 0, ok
				}
				return 0, 0, false
			},
			reporter,
		)
		mrp.SetRelays(relayIDs)
		mrp.SetPeers(func() []probe.ProbeTarget {
			var pts []probe.ProbeTarget
			for _, t := range probeTargetsFromConfig(firstCfg.GetTopology()) {
				pts = append(pts, probe.ProbeTarget{NodeID: t.NodeID, IngressID: t.IngressID})
			}
			return pts
		}())
		go mrp.Run(ctx, 30*time.Second)
		slog.Info("corelink-node: TRANSIT 多 Relay 三维探测器已启动", "relays", len(relayIDs))
	}

	// ── 5b. Unix socket RPC for TUI ──
	startTime := time.Now()
	// state 守护 (角色, 拓扑版本) 这对值：OnConfig 回调（sync 循环 goroutine）写、
	// RPC handler 闭包读，跨 goroutine 须经 RWMutex 同步消除数据竞争（bug #3）。
	logBuf := rpc.NewLogBuffer(1000)
	logLevel := new(slog.LevelVar) // 默认 INFO
	if strings.EqualFold(os.Getenv("CORELINK_LOG_LEVEL"), "debug") {
		logLevel.Set(slog.LevelDebug)
	}
	slog.SetDefault(slog.New(logBuf.Handler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))))

	state := newNodeState(roleFromConfig(firstCfg), firstCfg.GetTopology().GetVersion())
	configSnap := newNodeConfigSnapshot(firstCfg)
	nodeRPCSrv := rpc.NewServer()
	nodemethods.RegisterAll(nodeRPCSrv, nodemethods.Deps{
		LogBuffer:     logBuf,
		NodeID:        id.NodeID,
		VIP:           firstCfg.GetVirtualIp(),
		Role:          func() string { return state.role().String() },
		TopoVer:       func() uint64 { return state.topoVer() },
		TopoUpdatedAt: func() time.Time { return state.topoUpdatedAt() },
		Uptime:        func() time.Duration { return time.Since(startTime) },
		Connected:     func() bool { return ctx.Err() == nil },
		Config:        func() any { return cfg },
		Ingresses: func() []nodemethods.IngressInfo {
			ingressMu.Lock()
			defer ingressMu.Unlock()
			out := make([]nodemethods.IngressInfo, 0, len(cachedIngresses))
			for _, ing := range cachedIngresses {
				out = append(out, nodemethods.IngressInfo{
					Host:       ing.GetHost(),
					Port:       ing.GetPort(),
					Source:     friendlyIngressSource(ing.GetSource()),
					Confidence: ing.GetConfidence(),
					NATType:    friendlyNATType(ing.GetNatType()),
				})
			}
			return out
		},
		PortmapMappings: func() []nodemethods.MappingInfo {
			managed := lifecycle.Managed()
			out := make([]nodemethods.MappingInfo, 0, len(managed))
			for _, m := range managed {
				transport := "TCP"
				if m.TransportUDP {
					transport = "UDP"
				}
				out = append(out, nodemethods.MappingInfo{
					Protocol:     m.Protocol.String(),
					ExternalIP:   m.ExternalIP,
					ExternalPort: m.ExternalPort,
					InternalPort: m.InternalPort,
					Transport:    transport,
					TTL:          m.TTL.String(),
				})
			}
			return out
		},
		PortmapStatus: func() nodemethods.PortmapStatusInfo {
			managed := lifecycle.Managed()
			return nodemethods.PortmapStatusInfo{Active: len(managed) > 0, ManagedCount: len(managed)}
		},
		Connections: func() []nodemethods.ConnectionInfo {
			var out []nodemethods.ConnectionInfo

			// 控制面连接
			ctrlState := "已连接"
			if ctx.Err() != nil {
				ctrlState = "已断开"
			}
			out = append(out, nodemethods.ConnectionInfo{
				PeerID:   "controller",
				PeerIP:   cfg.ControllerMTLSAddr,
				LinkType: "gRPC/mTLS",
				State:    ctrlState,
			})

			// DataPlane peer 连接信息
			for _, peer := range configSnap.get().GetPeers() {
				if peer == nil || peer.GetNodeId() == "" {
					continue
				}
				vip := ""
				if ips := peer.GetAllowedIps(); len(ips) > 0 {
					vip = ips[0]
				}
				out = append(out, nodemethods.ConnectionInfo{
					PeerID:   peer.GetNodeId(),
					VIP:      vip,
					LinkType: "DataPlane",
					State:    "已配置",
				})
			}
			return out
		},
		Routes: func(dst string) []nodemethods.RouteInfo {
			return nil
		},
		Peers: func() []nodemethods.PeerInfo {
			peers := configSnap.get().GetPeers()
			out := make([]nodemethods.PeerInfo, 0, len(peers))
			for _, p := range peers {
				vip := ""
				if ips := p.GetAllowedIps(); len(ips) > 0 {
					vip = ips[0]
				}
				out = append(out, nodemethods.PeerInfo{
					NodeID: p.GetNodeId(),
					VIP:    vip,
				})
			}
			return out
		},
		DebugBlockPeer: func(peerID string) {
			assembler.mu.Lock()
			dp := assembler.dp
			assembler.mu.Unlock()
			if dp != nil {
				dp.BlockPeer(peerID)
			}
			slog.Info("debug: 已屏蔽 peer 直连调度", "peer", peerID)
		},
		DebugUnblockPeer: func(peerID string) {
			assembler.mu.Lock()
			dp := assembler.dp
			assembler.mu.Unlock()
			if dp != nil {
				dp.UnblockPeer(peerID)
			}
			slog.Info("debug: 已恢复 peer 直连调度", "peer", peerID)
		},
		DebugListBlocked: func() []string {
			assembler.mu.Lock()
			dp := assembler.dp
			assembler.mu.Unlock()
			if dp != nil {
				return dp.ListBlocked()
			}
			return nil
		},
		DebugMTR: func(target string, count int, via []string, replyMode string) (*nodemethods.MTRResult, error) {
			return runMTR(target, count, via, replyMode, id.NodeID, configSnap, assembler)
		},
		DebugMTREnum: func(target string) (*nodemethods.MTREnumResult, error) {
			return runMTREnum(target, id.NodeID, configSnap, assembler)
		},
		DebugMTREnumAll: func() (*nodemethods.MTREnumAllResult, error) {
			snap := configSnap.get()
			var results []nodemethods.MTREnumResult
			for _, p := range snap.GetPeers() {
				vip := ""
				for _, cidr := range p.GetAllowedIps() {
					if pfx, err := netip.ParsePrefix(cidr); err == nil {
						vip = pfx.Addr().String()
					}
					break
				}
				if vip == "" || vip == snap.GetVirtualIp() {
					continue
				}
				r, err := runMTREnum(vip, id.NodeID, configSnap, assembler)
				if err != nil {
					continue
				}
				results = append(results, *r)
			}
			return &nodemethods.MTREnumAllResult{Results: results}, nil
		},
	})
	go func() {
		if err := nodeRPCSrv.Serve("/var/run/corelink-node.sock"); err != nil {
			slog.Error("node RPC socket 失败", "err", err)
		}
	}()
	defer nodeRPCSrv.Close()

	// ── 6. TopologyAssignment 消费 + 动态角色切换骨架 ──
	syncCli := nodesync.NewClient(nodesync.Config{
		GRPCAddr:  cfg.ControllerMTLSAddr,
		WSAddr:    cfg.ControllerMTLSAddr,
		HTTPAddr:  "https://" + cfg.ControllerHTTPAddr,
		TLSConfig: tlsCfg,
	})
	syncCli.SetLocalVersion(version.ConfigVersion{Epoch: firstCfg.GetEpoch(), Generation: firstCfg.GetGeneration()})
	syncCli.OnConfig = func(nc *genv1.NodeConfig) {
		slog.Debug("corelink-node: 收到配置更新", "gen", nc.GetGeneration(), "peers", len(nc.GetPeers()))
		configSnap.set(nc)
		// peers/routes 始终应用到数据面（无论角色）。
		assembler.ApplyConfig(nc)

		// 刷新 probe targets。
		probeMu.Lock()
		probeTargets = probeTargetsFromConfig(nc.GetTopology())
		probeMu.Unlock()

		newRole := roleFromConfig(nc)
		newVer := nc.GetTopology().GetVersion()
		curRole, curTopoVer := state.snapshot()
		if newVer <= curTopoVer && newRole == curRole {
			return // 拓扑版本未更新且角色未变，仅数据面 peers/routes 已应用。
		}
		slog.Info("corelink-node: 拓扑更新",
			"old_ver", curTopoVer, "new_ver", newVer,
			"old_role", curRole, "new_role", newRole)

		// 更新子系统下发参数（SessionRouter baseline / interconnect / multirelay resolver）。
		assembler.UpdateTopology(nc)

		// 角色翻转 → 起/停对应子系统（切换骨架；数据面不中断完整验证留 M4）。
		if newRole != curRole {
			slog.Warn("corelink-node: 角色翻转（切换骨架，完整数据面切换留 M4）",
				"from", curRole, "to", newRole)
			if err := assembler.SwitchRole(ctx, curRole, newRole, nc); err != nil {
				slog.Error("corelink-node: 角色切换失败", "err", err)
			}
		}
		state.set(newRole, newVer)
	}

	// 重连 controller 后立即触发定位重新上报
	syncCli.OnReconnect = func() {
		slog.Info("corelink-node: 控制面重连，触发定位重新上报")
		select {
		case assembler.geoFlush <- struct{}{}:
		default:
		}
	}

	// 首次配置也需要应用 DNS proxy / firewall 规则（OnConfig 只处理后续更新）。
	assembler.ApplyConfig(firstCfg)

	slog.Info("corelink-node: 启动配置同步循环")
	syncCli.Run(ctx)
	slog.Info("corelink-node: 已退出")
	return nil
}

// discoverAndReportIngress 执行 6 路入口发现并 gRPC 上报到 controller。
//
// listenPort 是 relay/interconnect 实际监听端口；非零时为 NETIF 入口中 Port==0
// 的条目补全端口——确保同内网 LAN IP 入口带有正确端口，对端可直连。
// portmapFn 是端口映射入口发现函数（UPnP/NAT-PMP/PCP），nil 时跳过该路。
func discoverAndReportIngress(
	ctx context.Context,
	cfg *agentconfig.Config,
	nodeID string,
	ingressCli genv1.IngressServiceClient,
	listenPort uint16,
	portmapFn func(ctx context.Context) ([]*genv1.Ingress, error),
	onDiscover ...func(*genv1.IngressSet),
) error {
	// Observed：通过 ObserveSource 取 controller 观察到的源地址（可空）。
	var observed *genv1.Endpoint
	octx, ocancel := context.WithTimeout(ctx, 5*time.Second)
	if src, err := ingressCli.ObserveSource(octx, &genv1.ObserveRequest{}); err == nil && src != nil {
		observed = &genv1.Endpoint{Host: src.GetHost(), Port: src.GetPort()}
	}
	ocancel()

	stunFn := func(c context.Context) (string, uint32, genv1.NatType, error) {
		return ingress.StunProbe(c, ingress.DefaultStunServers)
	}
	netifFn := func() []*genv1.Ingress { return ingress.EnumInterfaces(nil) }
	urlFn := func(c context.Context) (string, error) {
		return ingress.QueryPublicIP(c, nil, ingress.DefaultPublicIPURLs)
	}

	opts := buildIngressOptions(cfg, nodeID, observed, stunFn, netifFn, urlFn, portmapFn)
	dctx, dcancel := context.WithTimeout(ctx, 20*time.Second)
	defer dcancel()
	set := ingress.Discover(dctx, opts)

	// 为 Port==0 的 NETIF 入口补上实际监听端口。
	// NETIF 发现只有 IP 不知端口，补全后同内网节点才能通过 LAN IP 直连。
	if listenPort > 0 {
		for _, ing := range set.GetIngresses() {
			if ing.GetPort() == 0 && ing.GetHost() != "" &&
				ing.GetSource() == genv1.IngressSource_INGRESS_SOURCE_NETIF {
				ing.Port = uint32(listenPort)
			}
		}
	}

	for _, fn := range onDiscover {
		fn(set)
	}

	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	if _, err := ingressCli.ReportIngress(rctx, set); err != nil {
		return fmt.Errorf("ReportIngress: %w", err)
	}
	slog.Info("corelink-node: 入口上报完成", "count", len(set.GetIngresses()))
	return nil
}

// discoverWithOpts 用给定 DiscoverOptions 执行入口发现（封装超时上下文）。
// 抽出供冒烟测试以确定性 fn 复用发现路径，避免真实 STUN/网卡/URL 探测。
func discoverWithOpts(opts ingress.DiscoverOptions) *genv1.IngressSet {
	dctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return ingress.Discover(dctx, opts)
}

// driveProbeLoop 周期驱动 L1 探测：对下发的 probe_targets 逐个探测 → OnProbe → Tick。
//
// 探测函数 prober 本 task 用占位（placeholderProber）；真实 UDP 探测留 M4。
// targetsFn 动态返回当前 probe_targets（OnConfig 更新）。
func driveProbeLoop(
	ctx context.Context,
	reporter *probe.Reporter,
	prober probe.Prober,
	targetsFn func() []probeTarget,
) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			targets := targetsFn()
			// 先同步活跃目标集：剪除已移除目标的 fsms/samples/lastReported，
			// 避免目标缩减后上报陈旧样本、map 无界增长（bug #21）。
			pts := make([]probe.ProbeTarget, 0, len(targets))
			for _, t := range targets {
				pts = append(pts, probe.ProbeTarget{NodeID: t.NodeID, IngressID: t.IngressID})
			}
			reporter.SetTargets(pts)
			for _, pt := range pts {
				rtt, loss, ok := probe.ProbeOnce(prober, pt)
				reporter.OnProbe(pt, rtt, loss, ok)
			}
			reporter.Tick()
		}
	}
}

// placeholderProber 已废弃：生产代码已切换到 probe.TCPProber。
// 保留仅供既有测试使用（TestSmoke_ProbeLoopDrivesReporter 等）。
//
// Deprecated: 使用 probe.NewTCPProber 替代。
func placeholderProber(_ string) (rttMs uint32, lossPermille uint32, ok bool) {
	return 1, 0, true
}

// runNodeTUI 启动 Node 管理 TUI。
func runNodeTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	socketPath := fs.String("socket", "/var/run/corelink-node.sock", "RPC Unix socket 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 尝试连接 RPC client（连接失败不阻止启动——显示未连接）。
	var client *tui.RPCClient
	if c, err := tui.NewRPCClient(*socketPath); err != nil {
		slog.Warn("corelink-node tui: RPC 连接失败（继续）", "socket", *socketPath, "err", err)
	} else {
		client = c
		defer client.Close()
	}

	tabs := []tui.Tab{
		tuinode.NewStatusTab(client),
		tuinode.NewIngressTab(client),
		tuinode.NewConnectionsTab(client),
		tuinode.NewPortmapTab(client),
		tuinode.NewTracerouteTab(client),
		tuinode.NewLogsTab(client),
		tuinode.NewConfigTab(client),
	}

	app := tui.NewApp(tui.AppConfig{
		Title:  "CoreLink Node",
		Tabs:   tabs,
		Client: client,
	})

	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// runNodeConfig 启动 Node 配置向导。
func runNodeConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	output := fs.String("output", "/etc/corelink-node.json", "配置文件输出路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	steps := wizard.NodeWizardSteps()
	w := wizard.New(steps)
	p := tea.NewProgram(w)
	m, err := p.Run()
	if err != nil {
		return fmt.Errorf("config 向导运行失败: %w", err)
	}
	wiz := m.(*wizard.Wizard)
	if wiz.Cancelled() {
		fmt.Println("已取消。")
		return nil
	}
	if !wiz.Done() {
		return nil
	}

	data, err := wizard.NodeConfigJSON(wiz.Values())
	if err != nil {
		return fmt.Errorf("生成配置 JSON 失败: %w", err)
	}
	if err := os.WriteFile(*output, data, 0600); err != nil {
		return fmt.Errorf("写入配置文件 %s 失败: %w", *output, err)
	}
	fmt.Printf("配置已保存到 %s\n", *output)
	return nil
}

// ─────────────────────── realAssembler：生产子系统装配 ───────────────────────

// realAssembler 是 subsystemAssembler 的生产实现，接通真实子系统。
//
// 注：本 task 聚焦装配编排骨架。数据面正确性由既有 internal 包单测 + M3 集成测试
// 覆盖；完整多节点 mesh 留 M4。各子系统句柄保存以便优雅退出（Close）与动态更新
// （UpdateTopology / SwitchRole）。
type realAssembler struct {
	ks         *keystore.KeyStore
	id         *keystore.Identity
	cfg        *agentconfig.Config
	tlsCfg     *tls.Config
	tunFactory TUNFactory

	// relayCli 供 TRANSIT 注册 relay 端点信息到 controller。
	relayCli genv1.RelayControlServiceClient
	// ingressCli 供 setupNodeCore 内的 OnRouteUpdate / 定位 goroutine 上报 controller。
	ingressCli genv1.IngressServiceClient
	// geoFlush 重连 controller 时发信号触发定位立即重新上报。
	geoFlush chan struct{}

	// DNS proxy、防火墙管理器、分流引擎（配置更新时动态应用）。
	dnsProxy    *dnsproxy.Proxy
	dnsRelay    *splittunnel.DNSRelay
	fwMgr       firewall.FirewallManager
	splitEngine *splittunnel.Engine

	mu sync.Mutex
	// 数据面（DataPlane 是唯一数据面路径）。
	dp         *dataplane.DataPlane
	dpListener *dataplane.Listener // 数据面 TLS 监听器，需显式关闭
	t          tun.Device
	// LEAF 子系统。
	selector *multirelay.Selector
	// ProbeRouter 自治选路引擎
	probeRouter  *proberouter.ProbeRouter
	maxRouteHops int // 当前路由表中最长路由跳数（TTL 环路检测用）
	ctx          context.Context
}

// SetupLeaf 装配 LEAF：node-core（DataPlane+TUN）+ multirelay 按选定入口接入。
func (a *realAssembler) SetupLeaf(ctx context.Context, p leafParams) error {
	if err := a.setupNodeCore(p.FirstConfig); err != nil {
		return fmt.Errorf("LEAF node-core: %w", err)
	}
	a.mu.Lock()
	a.selector = multirelay.New(multirelay.Config{
		Candidates:      p.Candidates,
		Probe:           leafProbe,
		IngressResolver: p.IngressResolver,
	})
	sel := a.selector
	a.mu.Unlock()
	go sel.Run(ctx)
	slog.Info("corelink-node: LEAF 装配就绪", "candidates", len(p.Candidates))
	return nil
}

// SetupTransit 装配 TRANSIT：内嵌 node-core 数据面。
func (a *realAssembler) SetupTransit(ctx context.Context, p transitParams) error {
	// 分流模式预处理：清理上次残留策略路由 + 预设 fwmark，
	// 确保后续数据面连接能绕过 TUN 策略路由。
	if sp := p.FirstConfig.GetSplitTunnel(); sp != nil && sp.GetEnabled() {
		splittunnel.RemovePolicyRoutes()
		tunnel.SetFwMark(splittunnel.FwMarkBypass)
		slog.Info("corelink-node: 预设 fwmark（分流模式）", "mark", fmt.Sprintf("0x%x", splittunnel.FwMarkBypass))
	}

	// 内嵌 node-core（TRANSIT 自身作业务节点）——DataPlane 数据面。
	if err := a.setupNodeCore(p.FirstConfig); err != nil {
		slog.Warn("corelink-node: TRANSIT 内嵌 node-core 装配失败（继续）", "err", err)
	}
	slog.Info("corelink-node: TRANSIT 装配就绪", "neighbors", len(p.NeighborIDs))
	return nil
}

// SetupBasicAgent 装配退化基础 agent（无 topology，S5 单 relay 接入）。
func (a *realAssembler) SetupBasicAgent(_ context.Context, p basicParams) error {
	if err := a.setupNodeCore(p.FirstConfig); err != nil {
		return fmt.Errorf("基础 agent node-core: %w", err)
	}
	slog.Info("corelink-node: 基础 agent 装配就绪（无拓扑下发）")
	return nil
}

// setupNodeCore 装配 node-core 数据面（DataPlane 是唯一数据面路径）。
//
// 创建 TUN（MTU=1400）→ FlowTracker → RouteEngine → ConnPool → DataPlane 编排器。
// 装配完成后存入 a.dp 供 ApplyConfig / Close 使用。
// 旧 DataPlane（如有）在锁外关闭，避免角色翻转时资源泄漏。
func (a *realAssembler) setupNodeCore(firstCfg *genv1.NodeConfig) error {
	// 启动前清理：杀残留进程 + 删遗留 TUN 接口
	cleanupBeforeStart(a.cfg.TUNName)

	// 统一初始化内核参数（IP 转发、ICMP Redirect 禁用、rp_filter 等）
	tunName := a.cfg.TUNName
	if idx := strings.Index(tunName, "%"); idx >= 0 {
		tunName = tunName[:idx] + "0"
	}
	initSysctl(tunName)

	// 从配置解析 MTU，支持预设档位 1400/1500/9000/65535，默认 1400
	mtu := agentconfig.ResolveMTU(a.cfg.TUNMtu)

	tunDev, err := a.tunFactory(a.cfg.TUNName, mtu)
	if err != nil {
		return fmt.Errorf("创建 TUN: %w", err)
	}

	// 分流引擎套在 TUN 和 DataPlane 之间——拦截出站流量做 direct/proxy 分流
	physIfce := splittunnel.DetectPhysicalInterface()
	actualTUNName := a.cfg.TUNName
	if idx := strings.Index(actualTUNName, "%"); idx >= 0 {
		actualTUNName = actualTUNName[:idx] + "0" // corelink%d → corelink0
	}
	a.splitEngine = splittunnel.NewEngine(tunDev, physIfce, actualTUNName)
	// 设置 wrapper VIP（出口节点解封装需要知道本机 VIP）
	if vip := firstCfg.GetVirtualIp(); vip != "" {
		a.splitEngine.SetLocalVIP(vip)
	}
	// 预加载本地缓存的 GeoIP 数据
	geoPath := filepath.Join(a.cfg.DataDir, "geoip.dat")
	if m, err := geoip.LoadFile(geoPath); err == nil {
		a.splitEngine.UpdateMatcher(m)
		slog.Info("corelink-node: GeoIP 缓存已加载", "path", geoPath)
	}
	var tunForDP tun.Device = a.splitEngine.Wrapper()
	slog.Info("corelink-node: 分流引擎已初始化", "physIfce", physIfce, "tunName", actualTUNName)

	tracker := flowtrack.NewTracker(flowtrack.DefaultConfig())
	router := route.NewEngine()

	// 构造 mTLS 拨号器：用节点证书连接 relay 的 AccessListener
	nodeCert, certErr := tls.X509KeyPair(a.id.NodeCertPEM, a.id.NodeKeyPEM)
	if certErr != nil {
		tunDev.Close()
		return fmt.Errorf("加载节点证书: %w", certErr)
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(a.id.CACertPEM)
	mtlsConf := &tls.Config{
		Certificates:       []tls.Certificate{nodeCert},
		RootCAs:            caPool,
		InsecureSkipVerify: true, // relay 用自签 CA，不校验 hostname
		MinVersion:         tls.VersionTLS12,
		// 不设 ALPN — relay 侧按首帧内容区分数据面和 mesh 互联
	}
	dialFunc := func(ctx context.Context, addr string, _ connpool.TransportType) (net.Conn, error) {
		// 设置 fwmark 绕过分流策略路由（避免路由环路）
		d := &tls.Dialer{
			NetDialer: &net.Dialer{
				Control: splittunnel.DialControlFwMark,
			},
			Config: mtlsConf,
		}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return c, nil
	}

	// 收集本机地址（用于 ConnPool LAN 判定 + 内网地址优先）
	poolCfg := connpool.DefaultConfig()
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			if addrs, err := iface.Addrs(); err == nil {
				for _, a := range addrs {
					if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
						poolCfg.SelfAddrs = append(poolCfg.SelfAddrs, ipnet.IP.String())
					}
				}
			}
		}
	}

	pool := connpool.NewPool(poolCfg,
		connpool.WithDialer(dialFunc),
		connpool.WithOnConnEstablished(func(c *connpool.Conn) {
			if c.Framer == nil {
				return
			}
			// DNS 响应帧回调——交给 DNSRelay 处理（通过 a.mu 保护消除与 ApplyConfig 写入的竞态）
			c.Framer.OnDNS = func(_ netip.Addr, dnsPayload []byte) {
				a.mu.Lock()
				relay := a.dnsRelay
				a.mu.Unlock()
				if relay != nil {
					relay.HandleResponse(dnsPayload)
				}
			}
			// Probe 帧回调——出站连接也需处理 Probe 帧（对端通过入站 peerFramer 发来的探测包）
			c.Framer.OnProbe = func(sourceVIP netip.Addr, payload []byte) {
				a.mu.Lock()
				dpRef := a.dp
				a.mu.Unlock()
				if dpRef != nil {
					myVIP, _ := netip.ParseAddr(firstCfg.GetVirtualIp())
					dpRef.HandleProbeFrame(c.NextHop, sourceVIP, myVIP, payload)
				}
			}
			// 对每条出站连接启动 recv 循环
			go func() {
				for {
					dstVIP, _, _, payload, err := c.Framer.ReadPacket()
					if err != nil {
						return
					}
					// 本地投递
					a.mu.Lock()
					dpRef := a.dp
					a.mu.Unlock()
					if dpRef != nil {
						_ = dstVIP // DstVIP 用于中继路由，本地投递时直接注入 TUN
						dpRef.InjectInbound(payload)
					}
				}
			}()
		}),
	)

	// 初始化 Node 侧 SQLite 持久化
	dataDir := a.cfg.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/corelink"
	}
	os.MkdirAll(dataDir, 0o755) //nolint:errcheck
	nstore, err := nodestore.Open(dataDir + "/node.db")
	if err != nil {
		slog.Warn("nodestore: 初始化失败（继续无持久化）", "err", err)
	}

	// probeRouterRef 用于 OnRouteSync 闭包引用（dpInst 先于 probeRouter 创建）
	var probeRouterRef atomic.Pointer[proberouter.ProbeRouter]

	dpInst, err := dataplane.New(dataplane.Config{
		TUN:         tunForDP,
		Pool:        pool,
		Router:      router,
		FlowTracker: tracker,
		DefaultTTL:  64,
		OnRouteSync: func(senderVIP netip.Addr, entry transport.RouteSyncEntry) {
			if pr := probeRouterRef.Load(); pr != nil {
				pr.ReceiveRouteSync(senderVIP, entry)
			}
		},
	})
	if err != nil {
		tunDev.Close() //nolint:errcheck
		return fmt.Errorf("创建数据面: %w", err)
	}

	// 从缓存恢复 rankedHops（重启后立即可用，ProbeRouter 会覆盖）
	if nstore != nil {
		if cached, err := nstore.LoadRoutes(); err == nil && len(cached) > 0 {
			ranked := make(map[string][]string)
			for _, r := range cached {
				if r.NextHopID != "" && r.DstNodeID != "" {
					ranked[r.DstNodeID] = []string{r.NextHopID}
				}
			}
			if len(ranked) > 0 {
				dpInst.SetRankedHops(ranked)
				slog.Info("nodestore: 从缓存恢复路由", "routes", len(ranked))
			}
		}
	}

	// 首次应用 peers/routes。
	if err := dpInst.ApplyConfig(firstCfg); err != nil {
		dpInst.Close() //nolint:errcheck
		return fmt.Errorf("数据面 ApplyConfig: %w", err)
	}

	go dpInst.Run() //nolint:errcheck

	// 启动 ProbeRouter 自治选路引擎
	selfVIPAddr, _ := netip.ParseAddr(firstCfg.GetVirtualIp())
	localNStore := nstore // 闭包引用
	probeRouter := proberouter.New(proberouter.Config{
		DataPlane:      dpInst,
		Router:         router,
		SelfVIP:        selfVIPAddr,
		ProbeTimeout:   3 * time.Second,
		TickInterval:   60 * time.Second,
		Debounce:       1 * time.Second,
		MaxViaDepth:    2,
		MaxConcurrency: 10,
		OnMaxHopsUpdate: func(hops int) {
			a.mu.Lock()
			a.maxRouteHops = hops
			a.mu.Unlock()
		},
		OnRouteUpdate: func(best map[netip.Addr]proberouter.BestRoute, vipMap map[netip.Addr]string) {
			if localNStore == nil {
				return
			}
			var routes []nodestore.RouteCache
			for dstVIP, b := range best {
				routes = append(routes, nodestore.RouteCache{
					DstVIP:     dstVIP.String(),
					DstNodeID:  vipMap[dstVIP],
					NextHopID:  b.NextHopID,
					NextHopVIP: b.NextHopVIP.String(),
					RTTMs:      b.RTTMs,
					Label:      b.Label,
				})
			}
			if err := localNStore.SaveRoutes(routes); err != nil {
				slog.Warn("nodestore: 保存路由失败", "err", err)
			}

			// 上报路由到 controller（供拓扑地图）
			hops := make([]*genv1.RouteHop, 0, len(best))
			for dstVIP, b := range best {
				if b.Stale || b.NextHopID == "" {
					continue
				}
				dstID := vipMap[dstVIP]
				if dstID == "" {
					continue
				}
				hops = append(hops, &genv1.RouteHop{
					DstNodeId: dstID,
					NextHopId: b.NextHopID,
					RttMs:     uint32(b.RTTMs),
					Ranked:    []string{b.NextHopID},
				})
			}
			if len(hops) > 0 && a.ingressCli != nil {
				rep := &genv1.RouteReport{SrcNodeId: a.id.NodeID, Routes: hops}
				if _, err := a.ingressCli.ReportRoutes(a.ctx, rep); err != nil {
					slog.Debug("location: 路由上报失败", "err", err)
				}
			}
		},
	})
	probeRouterRef.Store(probeRouter) // OnRouteSync 闭包可用
	go probeRouter.Run(a.ctx)
	a.mu.Lock()
	a.probeRouter = probeRouter
	a.mu.Unlock()

	// 首次触发探测
	probeRouter.SetPeers(peersFromConfig(firstCfg))

	// 异步综合定位 + 上报 controller（启动一次 + 30min 周期重报）
	go func() {
		if a.ingressCli == nil {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				slog.Error("location: goroutine panic", "recover", r)
			}
		}()
		slog.Info("location: goroutine 启动")
		locCtx := a.ctx
		if locCtx == nil {
			locCtx = context.Background()
		}
		loc := location.New()
		report := func() bool {
			ctx, cancel := context.WithTimeout(locCtx, 15*time.Second)
			defer cancel()
			got, err := loc.Locate(ctx)
			if err != nil {
				slog.Warn("location: 定位失败", "err", err)
				return false
			}
			if _, err := a.ingressCli.ReportNodeGeo(locCtx, &genv1.NodeGeo{
				NodeId:    a.id.NodeID,
				Latitude:  got.Latitude,
				Longitude: got.Longitude,
				City:      got.City,
				Country:   got.Country,
				ColoIata:  got.ColIATA,
				Accuracy:  got.Accuracy,
				ColoLat:   got.ColLat,
				ColoLon:   got.ColLon,
				CfRttMs:   got.CFRttMs,
			}); err != nil {
				slog.Warn("location: 上报失败", "err", err)
				return false
			}
			slog.Info("location: 已上报定位", "city", got.City, "iata", got.ColIATA, "acc", got.Accuracy)
			return true
		}
		// 首次上报：失败时快速重试（30s 间隔，最多 5 次），避免 gRPC 启动竞态导致等 30min
		for range 5 {
			if report() {
				break
			}
			select {
			case <-locCtx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
		// 周期重报（30min）+ 控制面重连触发即时上报
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-locCtx.Done():
				return
			case <-ticker.C:
				report()
			case <-a.geoFlush:
				slog.Info("location: 控制面重连，立即重新上报")
				report()
			}
		}
	}()

	// 启动数据面专用 TLS 监听器（:7447）——接收其他节点 ConnPool 发来的 Transport 帧。
	serverCert, _ := tls.X509KeyPair(a.id.NodeCertPEM, a.id.NodeKeyPEM)
	srvTLSConf := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAnyClientCert, // 要求客户端证书但不做 CA 链验证（CA 证书可能用途不匹配）
		MinVersion:   tls.VersionTLS12,
	}
	selfVIP := firstCfg.GetVirtualIp()
	dpListener, err := dataplane.NewListener(dataplane.ListenerConfig{
		Addr:    ":7447",
		TLSConf: srvTLSConf,
		OnDNS: func(srcVIP netip.Addr, dnsPayload []byte, replyFramer *transport.Framer) {
			// 出口节点处理 DNS 帧：转发到 8.8.8.8 解析后回传
			dpInst.HandleDNSFrame(srcVIP, dnsPayload, replyFramer)
		},
		// Probe 帧接线：路径探测 → 逐跳转发或终点回复
		OnProbe: func(nodeID string, sourceVIP netip.Addr, payload []byte) {
			myVIP, _ := netip.ParseAddr(selfVIP)
			dpInst.HandleProbeFrame(nodeID, sourceVIP, myVIP, payload)
		},
		OnPeerConnect: func(nodeID string, f *transport.Framer) {
			dpInst.RegisterPeerFramer(nodeID, f)
		},
		OnPeerDisconnect: func(nodeID string, f *transport.Framer) {
			dpInst.UnregisterPeerFramer(nodeID, f)
		},
		OnFrame: func(srcNodeID string, dstVIP netip.Addr, dstRelay uint16, ttl uint8, payload []byte) {
			dstStr := dstVIP.String()
			// 本地投递：DstVIP 是本机 VIP → 注入 TUN
			if dstStr == selfVIP {
				dpInst.InjectInbound(payload)
				return
			}
			// 中继转发：DstVIP 不是本机 → 通过 rankedHops 按质量排序转发
			if !dstVIP.IsValid() || dstVIP.IsUnspecified() {
				return
			}
			slog.Debug("dataplane: 中继转发", "src", srcNodeID[:min(8, len(srcNodeID))], "dst", dstStr, "ttl", ttl)
			// TTL 环路检测：低于阈值则标记环路回避 + 触发重探测
			a.mu.Lock()
			maxHops := a.maxRouteHops
			a.mu.Unlock()
			if maxHops == 0 {
				maxHops = 3
			}
			loopThreshold := uint8(64 - (maxHops + 2))
			if ttl <= loopThreshold {
				slog.Warn("dataplane: TTL 环路！标记回避并重选路由", "dst", dstStr, "ttl", ttl, "sender", srcNodeID[:min(8, len(srcNodeID))])
				// 标记发送者为环路回避节点
				key := flowtrack.FlowKey{DstIP: dstVIP}
				decision := router.Route(key, dpi.Result{})
				if decision.NextHop != "" {
					dpInst.MarkLoopAvoid(decision.NextHop, srcNodeID)
				}
				a.mu.Lock()
				pr := a.probeRouter
				a.mu.Unlock()
				if pr != nil {
					go pr.TriggerReprobe()
				}
				return // TTL 已极低，不再转发此包
			}
			// 用 RelayForward 按 rankedHops 排序尝试，自动跳过 srcNodeID 和环路回避表
			dpInst.RelayForward(srcNodeID, dstVIP, dstRelay, ttl, payload)
		},
	})
	if err != nil {
		slog.Warn("corelink-node: 数据面监听器启动失败（继续）", "err", err)
	} else {
		// 存入 assembler 以便 Close/SwitchRole 时显式关闭（防止端口+goroutine 泄漏）
		a.mu.Lock()
		oldDPL := a.dpListener
		a.dpListener = dpListener
		a.mu.Unlock()
		if oldDPL != nil {
			oldDPL.Close()
		}
	}

	// 给 TUN 接口分配 VIP 地址并 UP 链路。
	if vip := firstCfg.GetVirtualIp(); vip != "" {
		tunName, _ := tunDev.Name()
		if err := tun.ConfigureAddress(tunName, vip+"/32"); err != nil {
			slog.Warn("corelink-node: TUN 分配 VIP 失败（继续）", "vip", vip, "tun", tunName, "err", err)
		} else {
			// 跨平台 TUN 链路和路由配置（Linux 使用 iproute2，其他平台为空操作）
			configureLinkUpAndRoute(tunName, "100.64.0.0/10")
			slog.Info("corelink-node: TUN 已就绪", "tun", tunName, "vip", vip, "mtu", mtu)
		}
	}

	// 写入新句柄、取出旧句柄；锁外关闭旧实例。
	a.mu.Lock()
	oldDP, oldT := a.dp, a.t
	a.dp = dpInst
	a.t = tunDev
	a.mu.Unlock()

	if oldDP != nil {
		oldDP.Close() //nolint:errcheck
	} else if oldT != nil {
		oldT.Close() //nolint:errcheck
	}

	slog.Info("corelink-node: 数据面装配就绪", "mtu", mtu)
	return nil
}

// ApplyConfig 应用新 NodeConfig 的 peers/routes 到数据面（动态配置同步）。
func (a *realAssembler) ApplyConfig(nc *genv1.NodeConfig) {
	// 数据面配置同步：dp 非 nil 时应用到 DataPlane。
	a.mu.Lock()
	dpInst := a.dp
	a.mu.Unlock()
	if dpInst != nil {
		if err := dpInst.ApplyConfig(nc); err != nil {
			slog.Warn("corelink-node: 数据面 ApplyConfig 失败", "err", err)
		}
	}

	// ProbeRouter：peer 列表变更触发重新探测
	a.mu.Lock()
	pr := a.probeRouter
	a.mu.Unlock()
	if pr != nil {
		pr.SetPeers(peersFromConfig(nc))
	}

	// 诊断日志：确认 NodeConfig 是否携带新字段
	slog.Info("corelink-node: ApplyConfig 新字段检查",
		"dns", nc.GetDns() != nil,
		"dns_enabled", nc.GetDns().GetEnabled(),
		"published_prefixes", len(nc.GetPublishedPrefixes()),
		"egress_rules", len(nc.GetEgressRules()),
		"discovery_configs", len(nc.GetDiscoveryConfigs()),
	)

	// DNS proxy 动态更新
	if dnsCfg := nc.GetDns(); dnsCfg != nil && dnsCfg.GetEnabled() {
		if a.dnsProxy == nil {
			p := dnsproxy.New(dnsCfg)
			if err := p.Start(context.Background()); err != nil {
				slog.Warn("corelink-node: DNS proxy 启动失败", "err", err)
			} else {
				a.dnsProxy = p
				slog.Info("corelink-node: DNS proxy 已启动", "addr", p.Addr())
			}
		} else {
			a.dnsProxy.UpdateRecords(dnsCfg.GetRecords())
		}
	}

	// Published prefix TUN 路由安装（访问方节点需要把流量导入 TUN）。
	// 关键安全检查：跳过与本机已有非 TUN 路由冲突的 prefix，防止覆盖真实 LAN 网关。
	a.mu.Lock()
	tunDev := a.t
	a.mu.Unlock()
	if tunDev != nil {
		tunName, _ := tunDev.Name()
		for _, pp := range nc.GetPublishedPrefixes() {
			prefix := pp.GetPrefix()
			if prefix == "" || pp.GetOwnerNodeId() == "" {
				continue
			}
			// 检查该 prefix 是否已有非 TUN 路由（即本机 LAN 路由），有则跳过
			checkOut, _ := exec.Command("ip", "route", "show", "match", prefix).CombinedOutput()
			if len(checkOut) > 0 && !strings.Contains(string(checkOut), tunName) {
				slog.Warn("corelink-node: 跳过 published prefix 路由（与本机路由冲突）",
					"prefix", prefix, "existing", strings.TrimSpace(string(checkOut)))
				continue
			}
			out, err := exec.Command("ip", "route", "replace", prefix, "dev", tunName).CombinedOutput()
			if err != nil {
				slog.Debug("corelink-node: published prefix 路由安装失败", "prefix", prefix, "err", err, "out", string(out))
			}
		}
	}

	// Firewall 规则应用（仅出口 node 有 egress rules）
	if a.fwMgr != nil {
		ctx := context.Background()
		if dnsCfg := nc.GetDns(); dnsCfg != nil {
			if err := a.fwMgr.ApplyDNS(ctx, dnsCfg); err != nil {
				slog.Warn("corelink-node: DNS 防火墙规则应用失败", "err", err)
			}
		}
		if rules := nc.GetEgressRules(); len(rules) > 0 {
			if err := a.fwMgr.ApplyEgress(ctx, rules); err != nil {
				slog.Warn("corelink-node: 出口防火墙规则应用失败", "err", err)
			}
		}
	}

	// 分流策略应用（v2：TUN wrapper 模式）
	if a.splitEngine != nil {
		sp := nc.GetSplitTunnel()
		slog.Info("corelink-node: 分流策略检查", "engine", a.splitEngine != nil, "policy", sp != nil, "enabled", sp.GetEnabled(), "rules", len(sp.GetRules()))
		if sp != nil {
			// GeoIP 版本变化时从 controller 拉取更新（5s 超时，不阻塞 Apply）
			geoPath := filepath.Join(a.cfg.DataDir, "geoip.dat")
			if v := sp.GetGeoipVersion(); v != "" {
				geoCtx, geoCancel := context.WithTimeout(context.Background(), 5*time.Second)
				geoURL := "https://" + a.cfg.ControllerHTTPAddr + "/v1/geoip"
				if data, err := fetchGeoIPData(geoCtx, geoURL, a.tlsCfg); err == nil {
					if wErr := os.WriteFile(geoPath, data, 0o644); wErr == nil {
						if m, lErr := geoip.LoadFile(geoPath); lErr == nil {
							a.splitEngine.UpdateMatcher(m)
							slog.Info("corelink-node: GeoIP 已更新", "version", v)
						}
					} else {
						slog.Warn("corelink-node: GeoIP 落盘失败", "err", wErr)
					}
				} else {
					slog.Debug("corelink-node: GeoIP 拉取跳过", "err", err)
				}
				geoCancel() // 显式调用而非 defer，避免 context 生命周期延迟到 ApplyConfig 返回
			}

			// 收集 controller / relay 地址用于保护列表（分流时绕行 VPN 隧道）
			var controllerAddrs, relayAddrs []string
			controllerAddrs = append(controllerAddrs, a.cfg.ControllerHTTPAddr)
			for _, relay := range nc.GetRelays() {
				if addr := tunnelAddrOf(relay); addr != "" {
					relayAddrs = append(relayAddrs, addr)
				}
			}

			// matcher 参数传 nil：Engine 内部已持有通过 UpdateMatcher 设置的 matcher
			opts := &splittunnel.ApplyOptions{
				LocalVIP: nc.GetVirtualIp(),
				Peers:    nc.GetPeers(),
			}
			if err := a.splitEngine.Apply(context.Background(), sp, nil, controllerAddrs, relayAddrs, opts); err != nil {
				slog.Warn("corelink-node: 分流引擎 Apply 失败", "err", err)
			}

			// 启动 DNS 中继（仅首次，防污染）
			if a.dnsRelay == nil {
				selfAddr, _ := netip.ParseAddr(nc.GetVirtualIp())
				exitVIP := splittunnel.ResolveExitVIPFromPolicy(sp, nc.GetPeers())
				if exitVIP.IsValid() {
					relay := splittunnel.NewDNSRelay(selfAddr, exitVIP)
					exitNodeID := sp.GetDefaultExitNodeId()
					slog.Info("dnsrelay: 出口节点 ID", "defaultExitID", exitNodeID, "exitVIP", exitVIP)
					if exitNodeID == "" {
						for _, r := range sp.GetRules() {
							if r.GetAction() == "proxy" && r.GetExitNodeId() != "" {
								exitNodeID = r.GetExitNodeId()
								break
							}
						}
					}
					relay.SetSendFn(func(dstVIP netip.Addr, dnsPayload []byte) error {
						a.mu.Lock()
						dp := a.dp
						a.mu.Unlock()
						if dp == nil {
							return fmt.Errorf("dataplane 未就绪")
						}
						if err := dp.SendDNSTo(exitNodeID, dstVIP, dnsPayload); err != nil {
							slog.Warn("dnsrelay: 发送 DNS 帧失败", "exit", exitNodeID, "err", err)
							return err
						}
						return nil
					})
					// TUN 层透明拦截：注入到 splitEngine wrapper
					relay.SetInjectFn(func(pkt []byte) {
						a.mu.Lock()
						dp := a.dp
						a.mu.Unlock()
						if dp != nil {
							dp.InjectInbound(pkt)
						}
					})
					a.splitEngine.SetDNSRelay(relay)
					a.dnsRelay = relay
					slog.Info("dnsrelay: TUN 层 DNS 拦截已启用", "exitVIP", exitVIP, "exitNode", exitNodeID)
				}
			}
		}
	}
}

// UpdateTopology 更新拓扑相关子系统参数。
//
// 角色不翻转时的拓扑版本更新走此路径。数据面路由通过 DataPlane.ApplyConfig 更新，
// 此处仅处理不需要全量 config 更新的拓扑特定逻辑。
func (a *realAssembler) UpdateTopology(nc *genv1.NodeConfig) {
	asg := nc.GetTopology()
	if asg == nil {
		return
	}

	// 数据面路由在 ApplyConfig 中已更新，此处仅处理拓扑特定逻辑。
	// multirelay IngressResolver 更新（LEAF）：Selector 不暴露 SetResolver，留 M4
	// 由 SwitchRole 重建（本 task 切换骨架）。
	_ = asg // 预留扩展点
}

// SwitchRole 角色翻转骨架：停旧角色专属子系统 + 起新角色子系统。
//
// 数据面保留策略（TRANSIT↔LEAF）：
// TRANSIT 和 LEAF 都需要 node-core 数据面（TUN + DataPlane），角色切换时
// setupNodeCore 会重建 DataPlane（有短暂中断）。
//
// TODO(M4): 优化为复用 TUN、仅重建 DataPlane 连接池，避免角色切换中断。
func (a *realAssembler) SwitchRole(ctx context.Context, from, to genv1.NodeTopoRole, nc *genv1.NodeConfig) error {
	slog.Info("corelink-node: 执行角色切换", "from", from, "to", to)

	// ── 停旧角色专属子系统 ──
	a.mu.Lock()
	if from == genv1.NodeTopoRole_NODE_TOPO_ROLE_LEAF {
		a.selector = nil // Selector 由其 Run goroutine 随 ctx 退出
	}
	a.mu.Unlock()

	// ── 起新角色子系统（复用装配分支）──
	switchFlags := featureflag.FromMap(map[string]bool{
		featureflag.VIPRouting: true,
		featureflag.TLS0RTT:    true,
	})
	return assembleByRole(ctx, a, a.id.NodeID, nc, switchFlags)
}

// Close 优雅关闭各子系统（defer 调用）。
//
// 先持锁取出各句柄到局部并置 nil，立即 Unlock，再在锁外按序关闭。
func (a *realAssembler) Close() {
	a.mu.Lock()
	t := a.t
	dpInst := a.dp
	dpLis := a.dpListener
	dns, fw, split := a.dnsProxy, a.fwMgr, a.splitEngine
	a.t = nil
	a.dp = nil
	a.dpListener = nil
	a.dnsProxy = nil
	a.fwMgr = nil
	a.splitEngine = nil
	a.mu.Unlock()

	// DNS proxy 和 firewall 清理（锁外执行避免死锁）
	if dns != nil {
		_ = dns.Close()
	}
	if fw != nil {
		_ = fw.Cleanup(context.Background())
	}
	if split != nil {
		split.Cleanup(context.Background())
	}

	// 带超时关闭各子系统。
	closeDone := make(chan struct{})
	go func() {
		if dpLis != nil {
			dpLis.Close()
		}
		if dpInst != nil {
			dpInst.Close() //nolint:errcheck
		}
		if t != nil {
			t.Close() //nolint:errcheck
		}
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(10 * time.Second):
		fmt.Fprintln(os.Stderr, "corelink-node: Close 超时（10s），强制退出")
		os.Exit(0)
	}
}

// leafProbe 通过 TCP 探测 RelayEndpoint 的隧道地址测量真实 RTT。
//
// 优先探测隧道（Tunnel）地址（TCP 可达），其次 UDP 地址。若无有效地址则返回
// ok=false。底层使用共享的 TCPProber 实例避免重复构造。
var leafTCPProber = probe.NewTCPProber(probe.TCPProbeConfig{})

func leafProbe(ep *genv1.RelayEndpoint) (rttMs int, ok bool) {
	if ep == nil {
		return 0, false
	}
	// 优先探测隧道地址（TCP 端口，三次握手可测 RTT）
	if t := ep.GetTunnel(); t != nil && t.GetHost() != "" {
		addr := fmt.Sprintf("%s:%d", t.GetHost(), t.GetPort())
		return leafTCPProber.ProbeAddr(addr)
	}
	// 回退：探测 UDP 地址（虽然是 UDP 服务，但 TCP 连接尝试仍可测量网络可达性）
	if u := ep.GetUdp(); u != nil && u.GetHost() != "" {
		addr := fmt.Sprintf("%s:%d", u.GetHost(), u.GetPort())
		return leafTCPProber.ProbeAddr(addr)
	}
	return 0, false
}

// collectMemoryMB 读取系统总物理内存（MB）。
// Linux 从 /proc/meminfo 的 MemTotal 行读取；其他平台或读取失败时返回 0。
func collectMemoryMB() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		// 格式：MemTotal:       16384000 kB
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	// 未找到 MemTotal 行（或扫描中途出错）：已知可忽略，调用方按 memory=0 兜底。
	// 显式吞掉 scanner.Err()，避免未来排查 memory=0 时成为盲点。
	_ = scanner.Err()
	return 0
}

// collectLoadPermille 读取 1 分钟负载并转换为 load_permille（×1000/核数）。
// Linux 从 /proc/loadavg 读取；其他平台或读取失败时返回 0。
func collectLoadPermille(cpus int) uint32 {
	if runtime.GOOS != "linux" || cpus <= 0 {
		return 0
	}
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	// 格式：0.12 0.34 0.56 1/234 5678
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return uint32(load1 * 1000 / float64(cpus))
}

// reportMachineSpec 采集本机规格并通过 gRPC 上报给 controller。
func reportMachineSpec(ctx context.Context, nodeID string, cli genv1.IngressServiceClient) error {
	cpus := runtime.NumCPU()
	spec := &genv1.MachineSpec{
		NodeId:       nodeID,
		Cpus:         uint32(cpus),
		MemoryMb:     collectMemoryMB(),
		LoadPermille: collectLoadPermille(cpus),
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := cli.ReportMachineSpec(rctx, spec); err != nil {
		return fmt.Errorf("ReportMachineSpec: %w", err)
	}
	slog.Info("corelink-node: MachineSpec 上报完成",
		"cpus", spec.Cpus, "memory_mb", spec.MemoryMb, "load_permille", spec.LoadPermille)
	return nil
}

// ─────────────────────── 辅助函数（复制自 cmd/agent、cmd/relay，避免改既有 cmd）──

// buildMTLSFromIdentity 从 keystore.Identity 构造 mTLS tls.Config。
// 信任锚用 token 下发的 controller CA 哈希（cfg.ControllerCAHash）：node 不依赖本地
// CA 证书，完全用 controller 握手出示的完整证书链 + ca_hash 验证——controller 服务端
// 证书可任意轮换，只要由该 CA 签发即信任（关闭默认 hostname 校验，改挂 CAPinnedVerifier）。
func buildMTLSFromIdentity(id *keystore.Identity, cfg *agentconfig.Config) (*tls.Config, error) {
	sn := serverNameFromAddr(cfg.ControllerMTLSAddr)
	tlsCfg, err := nodesync.BuildMTLSConfig(id.NodeCertPEM, id.NodeKeyPEM, id.CACertPEM, sn)
	if err != nil {
		return nil, err
	}
	tlsCfg.InsecureSkipVerify = true //nolint:gosec // 关默认 hostname 校验，改用 CAPinnedVerifier
	tlsCfg.VerifyPeerCertificate = tunnel.CAPinnedVerifier(cfg.ControllerCAHash)
	return tlsCfg, nil
}

// fetchNodeConfig 通过 mTLS HTTP 一次性拉取 NodeConfig。
func fetchNodeConfig(ctx context.Context, httpAddr string, tlsCfg *tls.Config) (*genv1.NodeConfig, error) {
	httpCli := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   15 * time.Second,
	}
	url := "https://" + httpAddr + "/v1/config"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var nc genv1.NodeConfig
	if err := protojson.Unmarshal(body, &nc); err != nil {
		return nil, err
	}
	return &nc, nil
}

// fetchGeoIPData 通过 mTLS HTTP 拉取 geoip.dat 二进制数据。
func fetchGeoIPData(ctx context.Context, url string, tlsCfg *tls.Config) ([]byte, error) {
	httpCli := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   120 * time.Second, // geoip.dat ~20MB，需要较长超时
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpCli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// fileExists 简单判断文件是否存在。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// tunnelAddrOf 从 relay endpoint 提取隧道备路地址；无则空串。
func tunnelAddrOf(ep *genv1.RelayEndpoint) string {
	if ep == nil {
		return ""
	}
	if t := ep.GetTunnel(); t != nil && t.GetHost() != "" {
		return fmt.Sprintf("%s:%d", t.GetHost(), t.GetPort())
	}
	return ""
}

// pickListenAddrs 返回 TRANSIT relay server 的本地监听地址。
//
// 单端口合并后，relay server（节点接入）与 mesh 互联共享同一固定端口
// （defaultMeshPort=7446）：AccessListener 按 peer cert OU=relay 识别互联连接后
// 移交 Interconnect。固定端口避免每次重启端口变化导致 controller 拓扑分配的
// 邻居入口地址过期（重启后旧端口失效、互联建链失败）。
func pickListenAddrs(_ *genv1.NodeConfig) (streamAddr, udpAddr string) {
	return "0.0.0.0:" + uitoa(uint32(defaultMeshPort)), ""
}

// buildCACertPool 从 PEM 构造 x509 证书池。
func buildCACertPool(caPEM []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for len(caPEM) > 0 {
		var block *pem.Block
		block, caPEM = pem.Decode(caPEM)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		pool.AddCert(cert)
	}
	return pool, nil
}

// serverNameFromAddr 从 "host:port" 提取 host（SNI 用）。
func serverNameFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func friendlyIngressSource(s genv1.IngressSource) string {
	switch s {
	case genv1.IngressSource_INGRESS_SOURCE_CONFIG:
		return "静态配置"
	case genv1.IngressSource_INGRESS_SOURCE_OBSERVED:
		return "服务端观测"
	case genv1.IngressSource_INGRESS_SOURCE_STUN:
		return "STUN探测"
	case genv1.IngressSource_INGRESS_SOURCE_NETIF:
		return "本地网卡"
	case genv1.IngressSource_INGRESS_SOURCE_URL:
		return "公网查询"
	case genv1.IngressSource_INGRESS_SOURCE_UPNP:
		return "自助打洞"
	default:
		return "未知"
	}
}

func friendlyNATType(n genv1.NatType) string {
	switch n {
	case genv1.NatType_NAT_TYPE_OPEN:
		return "开放"
	case genv1.NatType_NAT_TYPE_FULL_CONE:
		return "完全锥形"
	case genv1.NatType_NAT_TYPE_RESTRICTED:
		return "受限锥形"
	case genv1.NatType_NAT_TYPE_PORT_RESTRICTED:
		return "端口受限"
	case genv1.NatType_NAT_TYPE_SYMMETRIC:
		return "对称型"
	default:
		return "未知"
	}
}

// ─── MTR 路由追踪 ───────────────────────────────────────────────────────────

// runMTR 执行 overlay MTR 路由追踪：统一使用 Probe 帧测量真实路径延迟。
//
// 两种模式：
//   - 自然路由（无 --via）：查 FIB 构建路径，Probe 帧 AutoReply=true（reply 走自然路由回源端）
//   - 指定路由（--via）：手动构建路径，Probe 帧 AutoReply=false（reply 原路逐跳返回）
//
// 一个 Probe 包 → 路径上每个节点各回一个 reply → 一次探测获得全路径 per-hop 累计延迟。
func runMTR(target string, count int, via []string, replyMode string, selfNodeID string, cfgSnap *nodeConfigSnapshot, a *realAssembler) (*nodemethods.MTRResult, error) {
	type peerMeta struct {
		nodeID, vip, hostname string
	}
	peers := make(map[string]peerMeta)
	vipMap := make(map[string]string) // VIP → nodeID
	selfVIP := ""

	snap := cfgSnap.get()
	for _, p := range snap.GetPeers() {
		nid := p.GetNodeId()
		vip := ""
		for _, cidr := range p.GetAllowedIps() {
			if pfx, err := netip.ParsePrefix(cidr); err == nil {
				vip = pfx.Addr().String()
			}
			break
		}
		peers[nid] = peerMeta{nodeID: nid, vip: vip}
		if vip != "" {
			vipMap[vip] = nid
		}
	}
	if v := snap.GetVirtualIp(); v != "" {
		selfVIP = v
	}

	// 解析目标为 VIP
	targetVIP, targetNodeID := "", ""
	if nid, ok := vipMap[target]; ok {
		targetVIP, targetNodeID = target, nid
	}
	if targetVIP == "" {
		for nid, m := range peers {
			if strings.HasPrefix(nid, target) {
				targetNodeID, targetVIP = nid, m.vip
				break
			}
		}
	}
	if targetVIP == "" {
		if ip := net.ParseIP(target); ip != nil {
			targetVIP = ip.String()
		} else {
			return nil, fmt.Errorf("无法解析目标: %s", target)
		}
	}

	resolveNode := func(hint string) (nodeID, vip string) {
		if nid, ok := vipMap[hint]; ok {
			return nid, hint
		}
		for nid, m := range peers {
			if strings.HasPrefix(nid, hint) {
				return nid, m.vip
			}
		}
		if ip := net.ParseIP(hint); ip != nil {
			return "", ip.String()
		}
		return hint, ""
	}

	type hopEntry struct {
		nodeID, vip, hostname string
	}

	a.mu.Lock()
	dp := a.dp
	a.mu.Unlock()
	if dp == nil {
		return nil, fmt.Errorf("数据面未就绪")
	}

	var hops []hopEntry
	var routeVia string
	hops = append(hops, hopEntry{nodeID: selfNodeID, vip: selfVIP})

	// 默认回包模式：via→trace（原路回包），auto→auto（自然路由回包）
	// 允许通过 --reply 显式覆盖
	isViaMode := len(via) > 0
	autoReply := !isViaMode // 默认值
	switch replyMode {
	case "auto":
		autoReply = true
	case "trace":
		autoReply = false
	}

	if isViaMode {
		// 指定路由模式：手动构建多跳路径
		viaNames := make([]string, 0, len(via))
		for _, v := range via {
			nid, vip := resolveNode(v)
			m := peers[nid]
			if m.vip != "" {
				vip = m.vip
			}
			hops = append(hops, hopEntry{nodeID: nid, vip: vip, hostname: m.hostname})
			if nid != "" {
				viaNames = append(viaNames, nid[:min(8, len(nid))])
			} else {
				viaNames = append(viaNames, v)
			}
		}
		routeVia = "forced(" + strings.Join(viaNames, "→") + ")"
	} else {
		// 自然路由模式：查 FIB 路径
		routeVia = "auto"
		if addr, err := netip.ParseAddr(targetVIP); err == nil {
			pi := dp.QueryRoute(addr)
			routeVia = "auto(" + pi.Via + ")"
			for i, nid := range pi.Hops {
				if i == len(pi.Hops)-1 && nid == targetNodeID {
					continue // target 单独追加
				}
				m := peers[nid]
				hops = append(hops, hopEntry{nodeID: nid, vip: m.vip, hostname: m.hostname})
			}
		}
	}

	targetHostname := ""
	if m, ok := peers[targetNodeID]; ok {
		targetHostname = m.hostname
	}
	hops = append(hops, hopEntry{nodeID: targetNodeID, vip: targetVIP, hostname: targetHostname})

	// 构建 Probe 路由 VIP 列表（hops[1:] 的 VIP，不含自身）
	var routeVIPs []netip.Addr
	for _, h := range hops[1:] {
		addr, err := netip.ParseAddr(h.vip)
		if err != nil {
			return nil, fmt.Errorf("跳 %s VIP 无效: %s", h.nodeID, h.vip)
		}
		routeVIPs = append(routeVIPs, addr)
	}
	selfVIPAddr, err := netip.ParseAddr(selfVIP)
	if err != nil {
		return nil, fmt.Errorf("本机 VIP 无效: %s", selfVIP)
	}

	// 发 count 轮 Probe，每轮一个包测全路径
	// hopRTTs[hopIndex] = 该跳收到的 RTT 列表（hopIndex 从 1 开始）
	hopRTTs := make(map[uint8][]float64)
	for range count {
		results, _ := dp.SendProbeAll(selfVIPAddr, routeVIPs, autoReply, 3*time.Second)
		for _, r := range results {
			ms := float64(r.RTT.Microseconds()) / 1000.0
			hopRTTs[r.HopIndex] = append(hopRTTs[r.HopIndex], ms)
		}
	}

	// 汇总每跳统计
	result := &nodemethods.MTRResult{Source: selfVIP, Target: targetVIP, Via: routeVia}
	for i, h := range hops {
		mh := nodemethods.MTRHop{
			Hop: i + 1, NodeID: h.nodeID, VIP: h.vip, Hostname: h.hostname, Sent: count,
		}
		if i == 0 {
			mh.Recv = count
			result.Hops = append(result.Hops, mh)
			continue
		}

		// hopIndex = i（Advance 后的值，对应 hops[i]）
		rtts := hopRTTs[uint8(i)]
		mh.Recv = len(rtts)
		if count > 0 {
			mh.LossPct = float64(count-len(rtts)) / float64(count) * 100
		}
		if len(rtts) > 0 {
			mh.LastMs = rtts[len(rtts)-1]
			mh.BestMs, mh.WorstMs = rtts[0], rtts[0]
			sum := 0.0
			for _, r := range rtts {
				sum += r
				if r < mh.BestMs {
					mh.BestMs = r
				}
				if r > mh.WorstMs {
					mh.WorstMs = r
				}
			}
			mh.AvgMs = sum / float64(len(rtts))
			if len(rtts) > 1 {
				varSum := 0.0
				for _, r := range rtts {
					d := r - mh.AvgMs
					varSum += d * d
				}
				mh.StdevMs = math.Sqrt(varSum / float64(len(rtts)))
			}
		}
		result.Hops = append(result.Hops, mh)
	}
	return result, nil
}

// ─── MTR 路由穷举 ─────────────────────────────────────────────────────────────

// runMTREnum 穷举到目标的所有可能路由（direct + 单跳 via + 双跳 via），每条发 1 个 Probe。
func runMTREnum(target string, selfNodeID string, cfgSnap *nodeConfigSnapshot, a *realAssembler) (*nodemethods.MTREnumResult, error) {
	type peerInfo struct {
		nodeID, vip string
	}

	snap := cfgSnap.get()
	selfVIP := snap.GetVirtualIp()
	if selfVIP == "" {
		return nil, fmt.Errorf("本机 VIP 未分配")
	}
	selfVIPAddr, _ := netip.ParseAddr(selfVIP)

	// 收集所有 peer
	var allPeers []peerInfo
	vipMap := make(map[string]string) // VIP → nodeID
	for _, p := range snap.GetPeers() {
		nid := p.GetNodeId()
		vip := ""
		for _, cidr := range p.GetAllowedIps() {
			if pfx, err := netip.ParsePrefix(cidr); err == nil {
				vip = pfx.Addr().String()
			}
			break
		}
		allPeers = append(allPeers, peerInfo{nodeID: nid, vip: vip})
		if vip != "" {
			vipMap[vip] = nid
		}
	}

	// 解析目标
	targetVIP := ""
	if _, ok := vipMap[target]; ok {
		targetVIP = target
	}
	if targetVIP == "" {
		for _, p := range allPeers {
			if strings.HasPrefix(p.nodeID, target) {
				targetVIP = p.vip
				break
			}
		}
	}
	if targetVIP == "" {
		if ip := net.ParseIP(target); ip != nil {
			targetVIP = ip.String()
		} else {
			return nil, fmt.Errorf("无法解析目标: %s", target)
		}
	}
	targetVIPAddr, _ := netip.ParseAddr(targetVIP)

	// 收集可作为 via 的 peer（排除自身和目标）
	var others []peerInfo
	for _, p := range allPeers {
		if p.vip != selfVIP && p.vip != targetVIP && p.vip != "" {
			others = append(others, p)
		}
	}

	a.mu.Lock()
	dp := a.dp
	a.mu.Unlock()
	if dp == nil {
		return nil, fmt.Errorf("数据面未就绪")
	}

	// 构建标签：nodeID 前缀 + VIP
	peerLabel := func(p peerInfo) string {
		id := p.nodeID
		if len(id) > 6 {
			id = id[:6]
		}
		return fmt.Sprintf("%s(.%s)", id, p.vip[strings.LastIndex(p.vip, ".")+1:])
	}

	// 探测一条路由：返回终点 RTT（ms），失败返回 -1
	probeRoute := func(routeVIPs []netip.Addr) float64 {
		results, _ := dp.SendProbeAll(selfVIPAddr, routeVIPs, true, 3*time.Second)
		// 取跳索引最大的 reply 的 RTT（即终点延迟）
		var maxHop uint8
		var rtt time.Duration
		for _, r := range results {
			if r.HopIndex >= maxHop {
				maxHop = r.HopIndex
				rtt = r.RTT
			}
		}
		if len(results) == 0 || rtt == 0 {
			return -1
		}
		return float64(rtt.Microseconds()) / 1000.0
	}

	var routes []nodemethods.MTREnumRoute

	// 生成所有无环全排列（所有深度）：direct + 1跳via + 2跳via + ... + N跳via
	// N = min(len(others), MaxProbeHops-1)，不允许循环（每个节点最多出现一次）
	type enumRoute struct {
		indices []int // others 的索引序列
	}
	var allRoutes []enumRoute
	allRoutes = append(allRoutes, enumRoute{}) // direct（无 via）

	var genPerms func(chosen []int, used []bool)
	genPerms = func(chosen []int, used []bool) {
		for i := range others {
			if used[i] {
				continue
			}
			next := append(append([]int{}, chosen...), i)
			allRoutes = append(allRoutes, enumRoute{indices: next})
			if len(next) < len(others) && len(next) < transport.MaxProbeHops-1 {
				used[i] = true
				genPerms(next, used)
				used[i] = false
			}
		}
	}
	genPerms(nil, make([]bool, len(others)))

	slog.Info("mtr-enum: 开始穷举", "target", targetVIP, "others", len(others), "routes", len(allRoutes))

	for _, er := range allRoutes {
		// 构建路由 VIP 列表：via[0] → via[1] → ... → target
		routeVIPs := make([]netip.Addr, 0, len(er.indices)+1)
		var labelParts []string
		for _, idx := range er.indices {
			addr, _ := netip.ParseAddr(others[idx].vip)
			routeVIPs = append(routeVIPs, addr)
			labelParts = append(labelParts, peerLabel(others[idx]))
		}
		routeVIPs = append(routeVIPs, targetVIPAddr)

		label := "direct"
		if len(labelParts) > 0 {
			label = "via " + strings.Join(labelParts, "→")
		}

		ms := probeRoute(routeVIPs)
		routes = append(routes, nodemethods.MTREnumRoute{Label: label, RTTMs: ms, Loss: ms < 0})
	}

	return &nodemethods.MTREnumResult{
		Source: selfVIP, Target: targetVIP, Routes: routes,
	}, nil
}

// peersFromConfig 从 NodeConfig 提取 PeerInfo 列表供 ProbeRouter 使用。
func peersFromConfig(cfg *genv1.NodeConfig) []proberouter.PeerInfo {
	var peers []proberouter.PeerInfo
	for _, p := range cfg.GetPeers() {
		nid := p.GetNodeId()
		for _, cidr := range p.GetAllowedIps() {
			if pfx, err := netip.ParsePrefix(cidr); err == nil {
				peers = append(peers, proberouter.PeerInfo{NodeID: nid, VIP: pfx.Addr()})
			}
			break
		}
	}
	return peers
}
