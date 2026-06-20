// cmd/corelink-controller 是 CoreLink 控制器主程序（spec §1/§3.7 合并二进制）。
//
// corelink-controller = 原 controller（enroll/CA/IPAM/ACL/配置下发/管理面）
//   - 拓扑优化器（TopoService，拓扑大脑）
//   - 入口/质量上报接收（ingress.Receiver）+ STUN 反射 + /v1/myip
//   - 拓扑状态持久化（topostore：质量矩阵 / 拓扑结果 / 入口集）
//   - CLI 管理子命令（并入）。
//
// 用法：
//
//	corelink-controller [serve] [-config path]   # 起控制器（serve 为默认子命令）
//	corelink-controller node ls | acl ... | ...  # 管理子命令（复用 cmd/corelink CLI）
//
// 装配（runServe，复用 cmd/controller run() 模式 + 接入拓扑大脑）：
//
//  1. store.Open + Migrate（含 topostore 的 QualityEdge/TopoResult/IngressRow 表）。
//  2. CA/IPAM/Enroll（同既有 controller）。
//  3. ingress.Receiver（P1）+ IngressSourceAdapter；STUN 反射；/v1/myip。
//  4. topostore.New + ResultStoreAdapter。
//  5. TopoService：Load() 重启加载即服务 + 周期 Tick（后台 goroutine）+ Receiver
//     收 EdgeEvent → TopoService.OnEvent（经 sink 接线）。
//  6. configsvc 注入 assignmentFn = TopoService.AssignmentForNode。
//  7. 起 server：enroll gRPC + mTLS gRPC（ConfigService/RelayControlService/
//     IngressService/EnrollService）+ HTTP（config+watch+myip）+ admin + STUN UDP。
//  8. 优雅退出。
//
// 注：完整多节点端到端自动并网 / 角色翻转 / 重启恢复的集成验证留 Task5.1（M4）。
// 本程序聚焦装配 + 拓扑大脑接线；各组件正确性由其所在包单测覆盖，装配冒烟由
// main_test.go 覆盖（内存库、回环端口、直接驱动 TopoService）。
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x6nux/corelink/internal/controller/admin"
	"github.com/x6nux/corelink/internal/controller/ca"
	"github.com/x6nux/corelink/internal/controller/config"
	"github.com/x6nux/corelink/internal/controller/configsvc"
	"github.com/x6nux/corelink/internal/controller/enroll"
	"github.com/x6nux/corelink/internal/controller/geoipdb"
	"github.com/x6nux/corelink/internal/nodecore/geoip"
	"github.com/x6nux/corelink/internal/controller/ingress"
	"github.com/x6nux/corelink/internal/nodecore/location"
	"github.com/x6nux/corelink/internal/controller/ipam"
	"github.com/x6nux/corelink/internal/controller/relayroster"
	"github.com/x6nux/corelink/internal/controller/server"
	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/controller/topoadapter"
	"github.com/x6nux/corelink/internal/controller/topology"
	"github.com/x6nux/corelink/internal/controller/topostore"
	"github.com/x6nux/corelink/internal/rpc"
	"github.com/x6nux/corelink/internal/rpc/ctrlmethods"
	"github.com/x6nux/corelink/internal/tui"
	tuicontroller "github.com/x6nux/corelink/internal/tui/controller"
	"github.com/x6nux/corelink/internal/tui/install"
	"github.com/x6nux/corelink/internal/tui/wizard"
	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

// 拓扑优化器默认参数（规格 §3.3 典型值）。装配期固定；后续可下沉到 config。
const (
	defaultMaxPeers    = 8
	defaultIngressK    = 2
	defaultRouteK      = 3
	defaultLeafUplinks = 2
	defaultProbeLimit  = 16

	// defaultTopoTick 是周期重算间隔（后台 Tick）。
	defaultTopoTick = 30 * time.Second
	// defaultDampingMinInterval 是两次重算的最小间隔（damping 节流）。
	defaultDampingMinInterval = 2 * time.Second
	// defaultStunReflectAddr 是内置 STUN 反射 UDP 监听地址（端口固定，节点据此探测）。
	defaultStunReflectAddr = ":7445"
)

func main() {
	args := os.Args[1:]

	// 子命令分发：serve（默认）起控制器；其余复用 cmd/corelink 管理 CLI。
	//
	// 最小并入策略（task 允许 DONE_WITH_CONCERNS）：root=corelink-controller，
	// "serve" 子命令跑装配，其余子命令（node/acl/key/relay/cert/ca/login/status/up）
	// 委托给 cmd/corelink 的既有 Cobra 命令树（cmds.NewRootCmd），避免重复实现。
	if len(args) > 0 {
		var err error
		sub := args[0]
		rest := args[1:]
		switch sub {
		case "serve":
			if err := runServe(rest); err != nil {
				slog.Error("corelink-controller serve 失败", "err", err)
				os.Exit(1)
			}
			return
		case "tui":
			err = runControllerTUI(rest)
		case "config":
			err = runControllerConfig(rest)
		case "install":
			err = runControllerInstall(rest)
		case "uninstall":
			err = runControllerUninstall(rest)
		case "update":
			err = runControllerUpdate(rest)
		case "reinstall":
			err = runControllerReinstall(rest)
		case "passwd":
			err = runControllerPasswd(rest)
		case "start":
			err = install.ServiceCmd("corelink-controller", "start")
		case "stop":
			err = install.ServiceCmd("corelink-controller", "stop")
		case "restart":
			err = install.ServiceCmd("corelink-controller", "restart")
		case "log":
			err = install.ServiceLog("corelink-controller")
		case "enable":
			err = install.ServiceEnable("corelink-controller")
		case "disable":
			err = install.ServiceDisable("corelink-controller")
		case "status":
			err = install.PrintStatus("corelink-controller", "/var/run/corelink-controller.sock")
		case "info":
			install.PrintInfo("corelink-controller", "/var/lib/corelink-controller")
		case "doctor":
			install.RunDoctor("corelink-controller", install.CommonDoctorChecks(
				"/etc/corelink-controller.json", "/var/run/corelink-controller.sock", ""))
		case "version":
			install.PrintVersion("corelink-controller")
		case "help", "-h", "--help":
			install.PrintHelp("corelink-controller", "管理 CLI:\n  node/key/acl/relay/cert/ca/login  详见各子命令 --help")
		default:
			// 管理命令（node/key/acl/cert/ca/relay/route/dns）直接调 store
			err = runDirectAdmin(args)
		}
		if err != nil {
			slog.Error("corelink-controller "+sub+" 失败", "err", err)
			os.Exit(1)
		}
		return
	}

	// 无子命令 → 启动 controller 服务
	if err := runServe(nil); err != nil {
		slog.Error("corelink-controller serve 失败", "err", err)
		os.Exit(1)
	}
}

// runServe 解析 serve 子命令的参数并起控制器。
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "配置文件路径（JSON）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		if v := os.Getenv("CORELINK_CONFIG"); v != "" {
			*configPath = v
		}
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}

	comps, err := buildController(cfg)
	if err != nil {
		return err
	}
	defer comps.Close()

	return comps.serve()
}

// loadConfig 加载配置文件（缺省回落默认值），并解析 CA 加密密钥。
func loadConfig(configPath string) (*config.Config, error) {
	var cfg *config.Config
	if configPath != "" {
		var err error
		cfg, err = config.Load(configPath)
		if err != nil {
			return nil, fmt.Errorf("加载配置失败: %w", err)
		}
	} else {
		cfg = &config.Config{
			DBDSN:          "sqlite://corelink.db",
			ListenAddr: ":7443",
			VirtualCIDR:    "100.64.0.0/10",
			CASubject:      "CoreLink Root CA",
			TLSMode:        "self-signed",
			SelfSignedHost: "localhost",
		}
	}

	// CAEncKey 从 DB 解析（buildController 中 store 打开后处理），此处不再处理。
	return cfg, nil
}

// controllerComponents 持有装配好的全部组件 + 监听器，供 serve / 测试复用。
type controllerComponents struct {
	cfg *config.Config
	st  *store.Store

	caM       *ca.Manager
	ipamA     *ipam.Allocator
	enrollSvc *enroll.Service
	cfgSvcs   *configsvc.Services
	roster    *relayroster.Roster

	receiver   *ingress.Receiver
	topoSvc    *topology.TopoService
	topoStore  *topostore.TopoStore
	ingressSrc *topoadapter.IngressSourceAdapter

	stun *ingress.StunReflector

	// startedAt 记录 controller 启动时间，用于冷启动宽限期判断。
	startedAt time.Time
	// lastTopoTick 记录最近一次拓扑 ticker 触发的 Unix 纳秒时间戳（0 表示尚未触发）。
	lastTopoTick atomic.Int64

	serverCert tls.Certificate
	caPool     *x509.CertPool
}

// edgeEventSink 是注入 Receiver 的 Sink：把收到的 EdgeEvent 转 EdgeDelta 回调进
// TopoService.OnEvent（驱动增量重算）。IngressSet / Quality 仅入 Receiver 内存
// （IngressSourceAdapter.Snapshot 直接读取），无需在 sink 里额外处理。
type edgeEventSink struct {
	topo *topology.TopoService
}

func (s *edgeEventSink) PutIngressSet(*genv1.IngressSet)   {}
func (s *edgeEventSink) PutQuality(*genv1.QualityReport)   {}
func (s *edgeEventSink) PutMachineSpec(*genv1.MachineSpec) {} // 阶段1：仅入 Receiver 内存
func (s *edgeEventSink) PutEdgeEvent(ev *genv1.EdgeEvent) {
	if ev != nil && s.topo != nil {
		s.topo.OnEvent(topoadapter.EdgeEventToDelta(ev))
	}
}

// buildController 装配全部组件（不起监听）。可被 main_test.go 复用做冒烟测试。
func buildController(cfg *config.Config) (*controllerComponents, error) {
	// ── store ──
	st, err := store.Open(cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	if err := st.Migrate(); err != nil {
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}

	// ── CAEncKey（从 DB 读取，首次自动生成）──
	encKey, err := resolveEncKey(st)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 加密密钥失败: %w", err)
	}
	cfg.CAEncKey = encKey

	// ── CA / IPAM / Enroll ──
	caM, err := ca.EnsureCA(st, cfg.CASubject, cfg.CAEncKey)
	if err != nil {
		return nil, fmt.Errorf("CA 初始化失败: %w", err)
	}
	ipamA, err := ipam.New(cfg.VirtualCIDR, st)
	if err != nil {
		return nil, fmt.Errorf("IPAM 初始化失败: %w", err)
	}
	enrollSvc := enroll.NewService(st, caM, ipamA)

	// ── topostore + ResultStore 适配 ──
	topoStore := topostore.New(st.DB())
	resultStore := topoadapter.NewResultStoreAdapter(topoStore)

	// ── ingress.Receiver + IngressSource 适配（sink 先空，建 TopoService 后接线）──
	sink := &edgeEventSink{}
	receiver := ingress.New(sink)
	receiver.GeoPersist = st // 定位持久化到 DB
	// 启动时从 DB 恢复定位数据
	if nodes, err := st.ListNodes(); err == nil {
		var geos []*genv1.NodeGeo
		for _, n := range nodes {
			if n.GeoLat != 0 || n.GeoLon != 0 {
				geos = append(geos, &genv1.NodeGeo{
					NodeId: n.ID, Latitude: n.GeoLat, Longitude: n.GeoLon,
					City: n.GeoCity, Country: n.GeoCountry, Accuracy: n.GeoAccuracy,
					ColoIata: n.GeoColIATA, ColoLat: n.GeoColLat, ColoLon: n.GeoColLon,
					CfRttMs: n.GeoCfRttMs,
				})
			}
		}
		receiver.LoadGeo(geos)
	}
	ingressSrc := topoadapter.NewIngressSourceAdapter(receiver)
	// A4：注入证书指纹查询，拓扑下发时填充 NeighborRef.Fingerprint。
	ingressSrc.SetFingerprintFn(st.GetCertFingerprint)
	// P5 FIB：注入节点 VIP 查询，拓扑下发时填充 TopologyAssignment.Fib。
	ingressSrc.SetNodeVIPsFn(func() (map[string]string, error) {
		nodes, err := st.ListNodes()
		if err != nil {
			return nil, err
		}
		vips := make(map[string]string, len(nodes))
		for _, n := range nodes {
			ip := n.VirtualIP
			if i := strings.Index(ip, "/"); i >= 0 {
				ip = ip[:i]
			}
			if ip != "" {
				vips[n.ID] = ip
			}
		}
		return vips, nil
	})

	// ── ConfigSvc（rosterRef 函数指针注入，打破循环依赖）──
	var rosterRef *relayroster.Roster
	cfgSvcs := configsvc.New(st, caM, func() map[string]string {
		if rosterRef != nil {
			return rosterRef.NodeRelay()
		}
		return nil
	})
	enrollSvc.SetNotify(cfgSvcs.Notify)
	receiver.Notify = cfgSvcs.Notify // 入口上报后触发该节点配置重算
	caM.Notify = cfgSvcs.Notify      // 证书签发/吊销后触发全网指纹刷新
	rosterRef = relayroster.New(st, cfgSvcs.Notify)

	// ── TopoService（拓扑大脑）──
	topoSvc := topology.NewTopoService(topology.TopoServiceDeps{
		Recv:   ingressSrc,
		Store:  resultStore,
		Notify: cfgSvcs.Notify,
		Clock:  time.Now,
		Params: topology.OptimizerParams{
			MaxPeers:    defaultMaxPeers,
			IngressK:    defaultIngressK,
			RouteK:      defaultRouteK,
			LeafUplinks: defaultLeafUplinks,
			ProbeFull:   false,
			ProbeLimit:  defaultProbeLimit,
		},
		Damping: topology.DampingParams{MinInterval: defaultDampingMinInterval},
		OnError: func(err error) { slog.Error("拓扑重算/持久化失败", "err", err) },
	})
	// 接线：Receiver 收 EdgeEvent → TopoService.OnEvent。
	sink.topo = topoSvc
	// 接线：入口变更 → 立即触发拓扑重算（含 damping 节流，避免风暴）。
	receiver.OnTopoTick = topoSvc.Tick

	// 重启加载即服务（有持久化则立即可服务 AssignmentForNode）。
	if loaded, err := topoSvc.Load(); err != nil {
		slog.Warn("拓扑持久化加载失败（将等首次重算）", "err", err)
	} else if loaded {
		slog.Info("拓扑持久化加载成功，重启即服务")
	}

	// configsvc 注入 assignmentFn = TopoService.AssignmentForNode。
	cfgSvcs.SetAssignmentFn(func(nodeID string) *genv1.TopologyAssignment {
		asg, ok := topoSvc.AssignmentForNode(nodeID)
		if !ok {
			return nil
		}
		return asg
	})
	// 注入入口查询——全连接 fallback 用
	cfgSvcs.SetIngressFn(receiver.GetIngressSet)

	// ── server cert & CA pool（缓存到 data 目录，重启复用避免重复签发）──
	serverCertPath := filepath.Join(filepath.Dir(strings.TrimPrefix(cfg.DBDSN, "sqlite://")), "server-cert.pem")
	if serverCertPath == "" || serverCertPath == "server-cert.pem" {
		serverCertPath = "/var/lib/corelink-controller/server-cert.pem"
	}
	serverCert, err := server.BuildServerCert(caM, serverCertPath)
	if err != nil {
		return nil, fmt.Errorf("构建 server 证书失败: %w", err)
	}
	caPool := server.BuildCAPool(caM.Cert())

	return &controllerComponents{
		cfg:        cfg,
		st:         st,
		caM:        caM,
		ipamA:      ipamA,
		enrollSvc:  enrollSvc,
		cfgSvcs:    cfgSvcs,
		roster:     rosterRef,
		receiver:   receiver,
		topoSvc:    topoSvc,
		topoStore:  topoStore,
		ingressSrc: ingressSrc,
		startedAt:  time.Now(),
		serverCert: serverCert,
		caPool:     caPool,
	}, nil
}

// httpMux 构造节点面 HTTP mux（config + watch + myip + health）。
func (c *controllerComponents) httpMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/v1/config", c.cfgSvcs.HTTPHandler())
	mux.Handle("/v1/watch", c.cfgSvcs.WSHandler())
	mux.HandleFunc("/v1/geoip", c.cfgSvcs.GeoIPHandler())
	mux.Handle("/v1/myip", ingress.NewMyIPHandler())
	// /v1/health：readiness 探活（DB 可达 + 拓扑 ticker 心跳新鲜）。
	// 冷启动宽限：首次 tick 前，若启动不足 90s 也视为新鲜。
	sqlDB, _ := c.st.DB().DB()
	mux.HandleFunc("/v1/health", configsvc.HealthHandler(sqlDB, func() bool {
		last := c.lastTopoTick.Load()
		if last == 0 {
			// 尚未触发过拓扑 tick，冷启动宽限 90s
			return time.Since(c.startedAt) < 90*time.Second
		}
		return time.Since(time.Unix(0, last)) < 90*time.Second
	}))
	return mux
}

// topoRPCAdapter 适配 *topology.TopoService → ctrlmethods.TopoIface。
//
// TopoService.AssignmentForNode 签名匹配；Status() 暂返回占位数据（TopoService
// 未公开 version/transit_count/leaf_count，后续迭代补全，不为 TUI 大改 topology 包）。
type topoRPCAdapter struct {
	svc *topology.TopoService
}

func (a *topoRPCAdapter) AssignmentForNode(nodeID string) (*genv1.TopologyAssignment, bool) {
	return a.svc.AssignmentForNode(nodeID)
}

func (a *topoRPCAdapter) Status() ctrlmethods.TopoStatus {
	st := a.svc.Status()
	return ctrlmethods.TopoStatus{
		Version:       st.Version,
		TransitCount:  st.TransitCount,
		LeafCount:     st.LeafCount,
		LastRecompute: st.LastRecompute,
	}
}

// serve 起全部监听 + 后台 goroutine（拓扑周期 Tick + STUN 反射）+ 优雅退出。
func (c *controllerComponents) serve() error {
	cfg := c.cfg

	// ── CRL 缓存（mTLS/HTTP 热路径共用，TTL 刷新避免每请求重算）──
	crlCache := server.NewCRLCache(c.caM.CurrentCRL, 30*time.Second)

	// ── 统一 gRPC server（证书可选：Enroll 不要求证书，其余要求 mTLS）──
	grpcSrv, unifiedTLS := server.NewUnifiedServer(c.serverCert, c.caPool, crlCache,
		func(s *grpc.Server) { genv1.RegisterConfigServiceServer(s, c.cfgSvcs.ConfigGRPC) },
		func(s *grpc.Server) { genv1.RegisterRelayControlServiceServer(s, c.roster) },
		func(s *grpc.Server) { genv1.RegisterIngressServiceServer(s, c.receiver) },
		func(s *grpc.Server) { genv1.RegisterEnrollServiceServer(s, c.enrollSvc) },
	)

	// ── 管理面 handler（合并到统一端口，路径 /admin/*，无需客户端证书）──
	adminHandler, err := buildAdminHandler(cfg, c.st, c.caM, c.ipamA, c.cfgSvcs.Notify, c.receiver)
	if err != nil {
		return fmt.Errorf("构建管理 handler 失败: %w", err)
	}

	// ── 统一 HTTP handler（gRPC + admin + mTLS HTTP 共享同一端口）──
	mTLSMux := server.RequireCertHTTPMiddleware(crlCache, "/admin/", "/login")(c.httpMux())
	combinedMux := http.NewServeMux()
	combinedMux.Handle("/admin/", adminHandler)
	combinedMux.Handle("/", mTLSMux)

	unifiedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcSrv.ServeHTTP(w, r)
		} else {
			combinedMux.ServeHTTP(w, r)
		}
	})
	httpSrv := &http.Server{
		Handler:   unifiedHandler,
		TLSConfig: unifiedTLS,
	}

	// ── GeoIP 数据库初始化（检测并注册本地 geoip.dat）──
	{
		// 从 DBDSN 提取数据目录（如 sqlite:///var/lib/xxx/corelink.db → /var/lib/xxx/）
		geoDataDir := filepath.Dir(strings.TrimPrefix(cfg.DBDSN, "sqlite://"))
		if geoDataDir == "" || geoDataDir == "." {
			geoDataDir = "/var/lib/corelink-controller"
		}
		geoUpdater := geoipdb.NewUpdater(geoDataDir, nil)
		if geoUpdater.Exists() {
			sha, size, err := geoUpdater.ComputeSHA256()
			if err == nil {
				_ = c.st.UpsertGeoIPMeta(&store.GeoIPMeta{
					SHA256:   sha,
					FilePath: geoUpdater.DataPath(),
					FileSize: size,
				})
				slog.Info("GeoIP 数据库已注册", "sha256", sha[:16]+"…", "size", size)
			}
		}
	}

	// 注入 GeoIP 查询到 configsvc——分流规则中 geoip:xx 在 controller 侧展开为 CIDR
	{
		geoDataDir := filepath.Dir(strings.TrimPrefix(cfg.DBDSN, "sqlite://"))
		if geoDataDir == "" || geoDataDir == "." {
			geoDataDir = "/var/lib/corelink-controller"
		}
		geoPath := filepath.Join(geoDataDir, "geoip.dat")
		if gm, err := geoip.LoadFile(geoPath); err == nil {
			c.cfgSvcs.SetGeoLookupFn(func(code string) []string {
				prefixes := gm.LookupCIDRs(code)
				out := make([]string, len(prefixes))
				for i, p := range prefixes {
					out[i] = p.String()
				}
				return out
			})
			slog.Info("configsvc: GeoIP 查询已注入", "codes", len(gm.Codes()))
		}
	}

	// ── STUN 反射 UDP ──
	stunAddr := defaultStunReflectAddr
	stun, err := ingress.NewStunReflector(stunAddr)
	if err != nil {
		slog.Warn("STUN 反射启动失败（继续，节点回退外部 STUN）", "addr", stunAddr, "err", err)
	} else {
		c.stun = stun
	}

	// ── 日志缓冲区（供 TUI 日志 Tab）──
	logBuf := rpc.NewLogBuffer(1000)
	baseHandler := slog.NewTextHandler(os.Stderr, nil)
	slog.SetDefault(slog.New(logBuf.Handler(baseHandler)))

	// ── Unix socket RPC for TUI ──
	rpcSrv := rpc.NewServer()
	caAdapter := admin.NewCAAdapter(c.caM)
	caHash := ""
	if h, err := caAdapter.CAPublicKeyHash(); err == nil {
		caHash = h
	}
	ctrlmethods.RegisterAll(rpcSrv, ctrlmethods.Deps{
		Store:     c.st,
		CA:        caAdapter,
		Online:    c.cfgSvcs.Notify,
		Notify:    c.cfgSvcs.Notify,
		Topo:      &topoRPCAdapter{svc: c.topoSvc},
		Ingress:   c.receiver,
		StartTime: time.Now(),
		Version:   "dev",
		Config: &ctrlmethods.ConfigSummary{
			DBDSN:          cfg.DBDSN,
			ListenAddr: cfg.ListenAddr,
			AdminAddr:  cfg.ListenAddr,
			VirtualCIDR:    cfg.VirtualCIDR,
			TLSMode:        cfg.TLSMode,
			CASubject:      cfg.CASubject,
			CAHash:         caHash,
		},
		LogBuffer: logBuf,
	})
	go func() {
		if err := rpcSrv.Serve("/var/run/corelink-controller.sock"); err != nil {
			slog.Error("controller RPC socket 失败", "err", err)
		}
	}()
	defer rpcSrv.Close()

	// ── 监听（统一端口：gRPC + HTTP 共享）──
	unifiedLis, err := tls.Listen("tcp", cfg.ListenAddr, unifiedTLS)
	if err != nil {
		return fmt.Errorf("监听统一地址 %s 失败: %w", cfg.ListenAddr, err)
	}

	slog.Info("corelink-controller 启动",
		"listen_addr", cfg.ListenAddr,
		"stun_addr", stunAddr,
	)

	// ── 后台 goroutine ──
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := httpSrv.Serve(unifiedLis); err != nil && err != http.ErrServerClosed {
			slog.Error("统一 server 退出", "err", err)
		}
	}()
	// 拓扑周期 Tick（damping 节流；首次 Tick 用当前 Snapshot 建立基线并下发）。
	go c.runTopoTicker(ctx, defaultTopoTick)

	// ── controller 自身定位（同 node 侧逻辑，结果直接写入 receiver 内存）──
	go c.runSelfLocation(ctx)

	// ── 优雅退出 ──
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("收到退出信号，正在关闭…")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()

	c.cfgSvcs.Notify.Close()
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutCtx)
	if c.stun != nil {
		_ = c.stun.Close()
	}

	slog.Info("corelink-controller 已退出")
	return nil
}

// runTopoTicker 周期驱动 TopoService.Tick（受 damping 节流）。ctx 取消即退出。
func (c *controllerComponents) runTopoTicker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.topoSvc.Tick()
			// 记录本次拓扑 tick 时间，供 /v1/health 判断心跳新鲜度。
			c.lastTopoTick.Store(time.Now().UnixNano())
		}
	}
}

// runSelfLocation 定位 controller 自身并直接写入 receiver 内存。
// Controller 通过 os.Hostname() 匹配 store 中的 Node.Hostname 找到自己的 nodeID。
func (c *controllerComponents) runSelfLocation(ctx context.Context) {
	hostname, _ := os.Hostname()
	nodes, err := c.st.ListNodes()
	if err != nil {
		slog.Warn("location: controller 查询节点失败", "err", err)
		return
	}
	var selfID string
	for _, n := range nodes {
		if n.Hostname == hostname {
			selfID = n.ID
			break
		}
	}
	if selfID == "" {
		slog.Info("location: controller 未在节点表中找到自身", "hostname", hostname)
		return
	}

	loc := location.New()
	report := func() bool {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		got, err := loc.Locate(lctx)
		if err != nil {
			slog.Warn("location: controller 定位失败", "err", err)
			return false
		}
		c.receiver.SetNodeGeo(&genv1.NodeGeo{
			NodeId:    selfID,
			Latitude:  got.Latitude,
			Longitude: got.Longitude,
			City:      got.City,
			Country:   got.Country,
			ColoIata:  got.ColIATA,
			Accuracy:  got.Accuracy,
			ColoLat:   got.ColLat,
			ColoLon:   got.ColLon,
			CfRttMs:   got.CFRttMs,
		})
		slog.Info("location: controller 已定位", "nodeID", selfID, "city", got.City, "acc", got.Accuracy)
		return true
	}

	// 首次定位 + 快速重试
	for range 5 {
		if report() {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
	// 周期重定位：30min
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report()
		}
	}
}

// Close 释放装配资源（store 等）。serve() 内的监听/server 在 serve 退出时已各自关闭。
//
// 顺序：
//  1. 停止 Notify worker goroutines（含 wg.Wait）——这些 worker 会调 BumpGeneration
//     写 DB，必须先于 DB 关闭停止，否则测试 TempDir cleanup 时发生"directory not empty"。
//     Notify.Close() 是幂等的：serve() 也调了它，重复调用直接返回。
//  2. 关闭 store 底层 *sql.DB 连接——释放 SQLite WAL/journal 文件句柄，
//     确保 t.TempDir() RemoveAll 时目录已无写入者。
//  3. 关闭 STUN 反射（如果已启动）。
func (c *controllerComponents) Close() {
	// 停止所有 per-node notify worker（BumpGeneration 写 DB 的 goroutine）。
	if c.cfgSvcs != nil {
		c.cfgSvcs.Notify.Close()
	}
	// 关闭底层 *sql.DB 连接（释放 SQLite 文件锁 + WAL 句柄）。
	if c.st != nil {
		if sqlDB, err := c.st.DB().DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	if c.stun != nil {
		_ = c.stun.Close()
	}
}

// runControllerTUI 启动 Controller 管理 TUI。
func runControllerTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	socketPath := fs.String("socket", "/var/run/corelink-controller.sock", "RPC Unix socket 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 尝试连接 RPC client（连接失败不阻止启动——显示未连接）。
	var client *tui.RPCClient
	if c, err := tui.NewRPCClient(*socketPath); err != nil {
		slog.Warn("corelink-controller tui: RPC 连接失败（继续）", "socket", *socketPath, "err", err)
	} else {
		client = c
		defer client.Close()
	}

	tabs := []tui.Tab{
		tuicontroller.NewDashboardTab(client),
		tuicontroller.NewNodesTab(client),
		tuicontroller.NewKeysTab(client),
		tuicontroller.NewCertsTab(client),
		tuicontroller.NewACLTab(client),
		tuicontroller.NewTopoTab(client),
		tuicontroller.NewTracerouteTab(client),
		tuicontroller.NewLogsTab(client),
		tuicontroller.NewConfigTab(client),
	}

	app := tui.NewApp(tui.AppConfig{
		Title:  "CoreLink Controller",
		Tabs:   tabs,
		Client: client,
	})

	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// runControllerConfig 启动 Controller 配置向导。
func runControllerConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	output := fs.String("output", "/etc/corelink-controller.json", "配置文件输出路径")
	if err := fs.Parse(args); err != nil {
		return err
	}

	steps := wizard.ControllerWizardSteps()
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

	data, err := wizard.ControllerConfigJSON(wiz.Values())
	if err != nil {
		return fmt.Errorf("生成配置 JSON 失败: %w", err)
	}
	if err := os.WriteFile(*output, data, 0600); err != nil {
		return fmt.Errorf("写入配置文件 %s 失败: %w", *output, err)
	}
	fmt.Printf("配置已保存到 %s\n", *output)
	return nil
}

// resolveEncKey 从 DB 读取 CA 加密密钥。首次启动时自动生成并持久化。
func resolveEncKey(st *store.Store) ([]byte, error) {
	const secretKey = "ca_enc_key"
	val, err := st.GetSystemSecret(secretKey)
	if err != nil {
		return nil, fmt.Errorf("读取 CA 加密密钥: %w", err)
	}
	if len(val) == 32 {
		return val, nil
	}

	// 首次启动——生成 32 字节 AES-256 密钥并持久化
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成 CA 加密密钥: %w", err)
	}
	if err := st.SetSystemSecret(secretKey, key); err != nil {
		return nil, fmt.Errorf("存储 CA 加密密钥: %w", err)
	}
	slog.Info("已自动生成 CA 加密密钥（持久化到数据库）")
	return key, nil
}
