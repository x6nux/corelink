# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概览

CoreLink 是一个用 Go 编写的 overlay VPN / mesh networking 系统,类似 Tailscale/ZeroTier。它实现了完整的节点注册(enroll)、CA 证书管理、虚拟 IP 分配(IPAM)、ACL 策略、自研高性能数据面(98%线速)、relay 中转、智能拓扑优化、Probe 自治选路、GeoIP 智能分流等能力。

**模块名**: `github.com/x6nux/corelink`
**Go 版本**: 1.26.0

## 架构总览

系统由三种角色组成:
- **Controller** (`corelink-controller`): 控制面中枢 -- CA/PKI、IPAM、节点注册、ACL、配置下发、拓扑优化、管理面 API
- **Node** (`corelink-node`): 统一节点程序 -- agent 数据面 + relay 中转能力;角色(LEAF/TRANSIT)由 controller 拓扑下发决定
- **CLI** (`corelink`): 管理命令行工具,通过 Admin HTTP API 管理节点/ACL/密钥/证书等

### 辅助二进制

| 二进制 | 路径 | 说明 |
|--------|------|------|
| `corelink-controller` | `cmd/corelink-controller/` | 主控制器(生产用,含 TUI/install/wizard 子命令) |
| `corelink-node` | `cmd/corelink-node/` | 统一节点(生产用,含 TUI/install/wizard 子命令) |
| `corelink` | `cmd/corelink/` | 管理 CLI (Cobra 命令树) |
| `corelink-deploy` | `cmd/corelink-deploy/` | SSH 远程部署编排工具 |

> 生产使用 `corelink-controller` + `corelink-node`。

## 构建、测试与代码生成

```bash
# 构建
go build ./cmd/corelink-controller
go build ./cmd/corelink-node
go build ./cmd/corelink

# 测试（涉及 Go 代码变更时必须在提交前跑全量测试）
make test                    # go test ./...
make test-integration        # go test -tags=integration ./...

# 代码检查
make lint                    # go vet ./...
make tidy                    # go mod tidy

# Protobuf 代码生成
make proto                   # protoc --go_out --go-grpc_out
```

### Protobuf

- Proto 文件: `pkg/proto/corelink/v1/*.proto`
- 生成输出: `pkg/proto/gen/*.pb.go`
- 需要 `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc`
- 生成后的 Go 包别名: `genv1`(代码中统一使用 `genv1 "github.com/x6nux/corelink/pkg/proto/gen"`)

### 前端 (Admin SPA)

```bash
cd web && npm install && npm run build  # Vite + React + TypeScript
```

构建产物 `web/dist/` 通过 `go:embed` 嵌入到 Go 二进制(`web/embed.go`),由管理面 HTTP server 提供 SPA 服务。

## 目录结构

```
cmd/
  corelink-controller/   # 主控制器入口(含拓扑大脑、steward 还政、TUI)
  corelink-node/          # 统一节点入口(LEAF/TRANSIT 角色自动切换)
  corelink/               # 管理 CLI (Cobra: node/acl/key/relay/cert/ca/login/status/route/dns)
  corelink-deploy/        # SSH 远程部署工具

internal/
  controller/             # Controller 侧逻辑
    admin/                #   管理面 HTTP API + SPA 内嵌 + 认证
    ca/                   #   CA 证书管理器
    config/               #   Controller 配置加载
    configsvc/            #   配置下发服务(gRPC stream + HTTP pull + WebSocket watch)
    enroll/               #   节点注册服务(gRPC)
    ingress/              #   入口上报接收 + STUN 反射 + 公网 IP 探测
    ipam/                 #   虚拟 IP 分配(CIDR 池)
    relayroster/          #   Relay 花名册(节点-relay 映射)
    server/               #   gRPC/HTTP server 构造 + CRL 缓存/拦截
    snapshot/             #   全网快照(steward failover 用)
    store/                #   持久化层(GORM: SQLite/PostgreSQL/MySQL)
    topology/             #   拓扑优化器(图/K路径/DAG/FIB/增量优化/服务编排)
    topoadapter/          #   拓扑适配器(解耦 topology <-> topostore/ingress)
    topostore/            #   拓扑结果持久化
    acl/                  #   ACL 策略解析 + NodeConfig 生成(纯函数)
    routepolicy/          #   路由策略(alias/route/DNS/子网发布)

  nodecore/               # 节点侧逻辑
    dataplane/            #   自研数据面(TLS帧传输/TUN读写/路由/中继转发)
    connpool/             #   弹性连接池(多连接/质量排序/自动扩缩容)
    splittunnel/          #   智能分流引擎(gVisor/IPIP封装/GeoIP/DNS拦截)
    config/               #   节点引导配置
    dnsproxy/             #   内置 DNS 代理
    discovery/            #   ARP/邻居发现
    enroll/               #   注册客户端(gRPC)
    firewall/             #   iptables/nftables 防火墙管理
    flowtrack/            #   分段锁流追踪器(五元组/DPI/超时GC)
    geoip/                #   GeoIP 匹配器(国家CIDR查表)
    ingress/              #   入口发现(STUN/UPnP/NAT-PMP/PCP/网卡枚举/公网查询)
    keystore/             #   节点密钥/证书本地存储
    multirelay/           #   多 relay 选择器(LEAF 用)
    portmap/              #   端口映射(UPnP-IGD/NAT-PMP/PCP)
    probe/                #   L1 质量探测(TCP RTT/LinkState FSM/多 relay 三维探测)
    relayclient/          #   Relay 接入客户端
    snapstore/            #   节点侧快照存储
    steward/              #   Steward 决策层(选举/加冕/探活/A档服务)
    sync/                 #   配置同步客户端(gRPC+WS+HTTP 三通道 failover)
    tun/                  #   TUN 设备(真实/fake)

  relay/                  # Relay 中转逻辑
    server/               #   接入监听(TLS/WS/gRPC 多协议合并/CRL 拦截)
    mesh/                 #   Relay 间 mesh 互联(Interconnect/SessionRouter/FIBRoute/Gossip/Snapshot)
    forward/              #   转发逻辑
    session/              #   会话表
    ratelimit/            #   速率限制
    health/               #   健康检查
    handoff/              #   会话迁移
    keepalive/            #   保活
    location/             #   位置上报器
    locationcache/        #   位置缓存
    wgrouter/             #   WG 路由

  transport/              # 帧传输层(Framer/bufio批量写/可复用读缓冲区)
  rpc/                    # Unix socket RPC(TUI <-> daemon 通信)
  tui/                    # Terminal UI(bubbletea, controller/node 两种视图)
  pki/                    # PKI 工具(CSR/CRL/CA/轮换)
  featureflag/            # Feature flag(VIPRouting/TLS0RTT)
  version/                # 版本号 + 配置版本(Epoch/Generation)
  integration/            # 集成测试(steward 选举/服务)

pkg/
  proto/                  # Protobuf 定义与生成代码
    corelink/v1/          #   .proto 源文件(8 个)
    gen/                  #   生成的 .pb.go + _grpc.pb.go
  tunnel/                 # 隧道传输层(TLS/WS/gRPC/TCP + mTLS 指纹校验)

web/                      # 管理面 React SPA(Vite + TypeScript)
```

## 关键架构概念

### 配置下发流

1. Controller 侧 `configsvc` 为每个节点维护 `generation`(单调递增)
2. 节点通过三通道 failover 同步配置: gRPC 服务端流(`WatchConfig`) > WebSocket(`/v1/watch`) > HTTP 轮询(`/v1/config`)
3. Controller 仅推送轻量 `ChangeSignal`(changed + generation + epoch),节点收到后通过 HTTP 拉取完整 `NodeConfig`
4. `NodeConfig` 包含: peers/routes/relays/CRL/拓扑分配(TopologyAssignment)/DNS/发布前缀/出口规则

### 拓扑优化(智能并网)

- `topology.TopoService` 是拓扑大脑:周期 Tick + 事件驱动(EdgeEvent) + damping 节流
- 输入: 入口上报(IngressSet) + 质量矩阵(QualityReport) + 边事件(EdgeEvent)
- 输出: per-node `TopologyAssignment`(角色/邻居/基线路由/探测目标/FIB)
- 角色分配: TRANSIT(中转) / LEAF(叶子)
- 结果持久化到 `topostore`,重启后 `Load()` 立即可服务

### 数据面

自研数据面（已替代 WireGuard）:
- 自定义 TLS 帧传输协议(4B length-prefix + VIP 路由头 + payload)
- 出站: TUN Read → FlowTracker → RouteEngine → ConnPool/PeerFramer → Framer.WritePacket → TLS Write
- 入站: TLS Read → Framer.ReadPacket → channel → 单消费者批量 TUN Write
- 中继转发: InjectInbound → TUN → kernel ip_forward → TUN Read → processOutbound → 转发到目标节点
- 性能: bufio 批量 Flush + channel 消费者模型，实测 98% 物理线速(983 Mbps / 1 Gbps)，CPU 30%
- 数据面监听端口: `:7447`(DataPlane Listener)

### VIP 路由模式

FIB 表(`FIBTable`)由 controller 按拓扑计算并下发，节点侧 RouteEngine 做多层匹配(L5/L4/L3)
ProbeRouter 自治选路: 周期探测 → 加权评分(throughput×0.6 + latency×0.4) → 动态调整最优路径

### Steward (故障转移)

TRANSIT 节点内置 steward 决策层:
- 周期探活 controller(`/v1/health`)
- Controller 失联时通过 mesh aliveness/coronation 帧选举新 steward
- 当选后自动起 A 档服务(降级的 config 下发)
- Controller 恢复后通过 `/v1/steward-handoff` 还政

### 数据面（旧 WireGuard 已移除）

- 自研数据面: `internal/nodecore/dataplane/`（DataPlane 编排器 + DataPlane Listener）
- 连接池: `internal/nodecore/connpool/`（弹性多连接 + 质量排序 + 自动扩缩容）
- 帧传输: `internal/transport/`（Framer + bufio 批量写 + 可复用读缓冲区）
- TUN 设备: 真实(`tun.CreateReal`)或 fake(`tun.CreateFake`,测试用)
- 智能分流: `internal/nodecore/splittunnel/`（gVisor + IPIP 封装 + GeoIP 路由）

## 数据库

- 通过 GORM 支持三种后端: SQLite(纯 Go,无 CGO) / PostgreSQL / MySQL
- DSN 格式: `sqlite://<path>` | `postgres://...` | `mysql://...`
- 默认: `sqlite://corelink.db`
- 迁移: `store.Migrate()` 使用 GORM AutoMigrate
- 主要模型: Node, Lease, EnrollKey, Cert, ACLPolicy, CARoot, RelayInfo, QualityEdge, TopoResult, IngressRow, SnapshotRow, AdminCredential, SystemSecret, NodeAlias, PublishedRoute, DiscoveredMapping, DNSSettings

## 网络端口(默认)

| 端口 | 用途 |
|------|------|
| `:7443` | Controller 统一端口(gRPC + HTTP + Admin 共享,VerifyClientCertIfGiven) |
| `:7445` | STUN 反射 UDP |
| `:7447` | 数据面 TLS 监听(DataPlane Listener,节点间帧传输) |
| Unix socket | `/var/run/corelink-controller.sock` 和 `/var/run/corelink-node.sock`(TUI RPC) |

## 配置文件

### Controller (`/etc/corelink-controller.json`)
```json
{
  "DBDSN": "sqlite://corelink.db",
  "ListenAddr": ":7443",
  "VirtualCIDR": "100.64.0.0/10",
  "CASubject": "CoreLink Root CA",
  "TLSMode": "self-signed",
  "SelfSignedHost": "localhost",
  "AdminAddr": "127.0.0.1:8090",
  "AdminUser": "admin"
}
```

### Node (`/etc/corelink-node.json`)
```json
{
  "controller_enroll_addr": "controller:7443",
  "controller_mtls_addr": "controller:7444",
  "controller_http_addr": "controller:8080",
  "enrollment_key": "<key>",
  "controller_ca_hash": "sha256:<hex>",
  "data_dir": "/var/lib/corelink",
  "role": "agent",
  "tun_name": "corelink%d"
}
```

## 测试策略

- **单元测试**: `go test ./...` -- 大量使用表驱动测试,测试文件与源码同包
- **集成测试**: `go test -tags=integration ./...` -- 需要 `//go:build integration` 构建标签
  - `internal/controller/store/integration_test.go` -- 数据库集成
  - `internal/integration/` -- steward 选举/服务集成
  - `pkg/tunnel/proxy_integration_test.go` -- 隧道代理集成
- **冒烟测试**: 多个 `*_test.go` 中的 `TestSmoke_*` 函数,验证装配流程
- **TUN 测试**: 通过 `tun.CreateFake` 注入 fake TUN,避免需要 root 权限
- 测试中 DB 使用 `sqlite://:memory:` 内存库

## 编码规范

- 所有注释和日志使用中文
- 日志使用 `log/slog`(结构化日志)
- 错误处理: `fmt.Errorf("模块: 操作: %w", err)` 格式
- Proto 生成的 Go 包统一用别名 `genv1`
- Feature flag 通过 `internal/featureflag` 管理(当前: `VIPRouting`, `TLS0RTT`)
- CLI 使用 Cobra (`github.com/spf13/cobra`)
- TUI 使用 Bubbletea (`github.com/charmbracelet/bubbletea`)
- 依赖注入优先使用函数指针/接口,避免循环 import
- 并发安全: 共享状态使用 `sync.Mutex` / `sync.RWMutex`,关键路径有详细的锁序注释
- 优雅退出: context 取消 + signal 捕获 + 超时 shutdown

## 提交规范

- **所有涉及 Go 代码的变更,必须在提交前通过全量测试 `go test ./...`,未通过不允许提交**
- 建议同时跑 `go vet ./...` 确认无静态分析问题

## 运维规范

- **测试网操作禁止使用 SSH**,统一通过 `cmd/corelink-deploy` 工具进行部署和管理
- **判断测试网服务器是否可达必须用 `corelink-deploy <name> status` 实测**,不能仅凭 IP 地址段（如 10.x 内网地址）推断不可达

## AI 使用指引

- 修改 proto 后需运行 `make proto` 重新生成
- 修改前端后需在 `web/` 目录运行 `npm run build` 重新生成嵌入资源
- 新增持久化模型后需在 `internal/controller/store/migrate.go` 的 `Migrate()` 中注册
- 拓扑相关代码避免直接 import `topostore`/`configsvc`,通过接口解耦
- 测试 TUN 相关代码时注入 `tun.CreateFake`,不需要 root
- `cmd/corelink-controller` 和 `cmd/corelink-node` 是主入口,关注装配(wiring)逻辑
