# CoreLink

[English](README.en.md) | **简体中文**

> 一个用 Go 编写的 overlay VPN / mesh networking 系统，自带智能拓扑编排与控制面故障自愈。

CoreLink 把任意分布的节点组成一张加密的虚拟私有网络（mesh），类似 [Tailscale](https://tailscale.com/) / [ZeroTier](https://www.zerotier.com/)：节点自动注册、自动分配虚拟 IP、按链路质量智能组网，自研数据面基于 TLS 帧传输（98% 物理线速），支持 NAT 穿透、relay 中转、多跳路由与 GeoIP 智能分流，并在控制面失联时由 TRANSIT 节点自动接管（Steward 故障转移）。

```text
                 ┌────────────────────────────────────────────┐
                 │              Controller (控制面)            │
                 │  CA/PKI · IPAM · Enroll · ACL · 拓扑大脑    │
                 │  配置下发 · 管理面 API · Steward 还政        │
                 └───────────────┬────────────────────────────┘
            gRPC(mTLS) / WS / HTTP│ 三通道 failover 配置同步
        ┌─────────────────────────┼─────────────────────────┐
        ▼                         ▼                         ▼
  ┌──────────┐              ┌──────────┐              ┌──────────┐
  │  Node A  │◄── mesh ────►│  Node B  │◄── mesh ────►│  Node C  │
  │ (TRANSIT)│   互联/中转   │ (TRANSIT)│              │  (LEAF)  │
  └────┬─────┘              └────┬─────┘              └────┬─────┘
       │ 自研数据面 (TLS 帧传输)    │                         │
       └────────── 100.64.0.0/10 虚拟网段 (VIP 直通) ─────────┘
```

---

## 目录

- [✨ 特性](#-特性)
- [🏗️ 架构](#️-架构)
- [🚀 快速开始](#-快速开始)
- [📦 二进制与子命令](#-二进制与子命令)
- [⚙️ 配置](#️-配置)
- [🔌 默认端口](#-默认端口)
- [🛠️ 开发](#️-开发)
- [📁 目录结构](#-目录结构)
- [🗺️ 项目状态](#️-项目状态)
- [📄 License](#-license)

---

## ✨ 特性

- **一键组网** — 节点注册（enroll）后自动获得虚拟 IP，无需手动配置 IP/路由。
- **智能拓扑编排** — 控制面 `TopoService`（拓扑大脑）周期 + 事件驱动重算，按链路质量（RTT/丢包）分配 LEAF / TRANSIT 角色、计算 K 路径与 FIB 转发表。
- **自研高性能数据面** — 自定义 TLS 帧传输协议（4B length-prefix + VIP 路由头 + payload），bufio 批量写 + channel 单消费者批量 TUN Write，实测吞吐达物理线速 **98%**（983 Mbps / 1 Gbps），CPU 占用仅 30%。
- **Probe 自治选路** — 节点间周期 L1 质量探测（TCP RTT / LinkState FSM / 多 relay 三维探测），ProbeRouter 按 throughput×0.6 + latency×0.4 加权评分自动选择最优路径。
- **VIP 直通路由** — FIB 表（ECMP / DAG）支持多跳路由，中继转发通过内核 ip_forward 透明转发。
- **Steward 故障自愈** — TRANSIT 节点内置决策层：周期探活 Controller，失联时通过 mesh 选举新 Steward 并自动起降级控制面（A 档服务），Controller 恢复后自动还政。
- **三通道配置同步** — gRPC 服务端流 → WebSocket → HTTP 轮询，自动 failover；以 `(epoch, generation)` 版本号保证单调，节点只接受更新版本。
- **NAT 穿透** — 内置 STUN 反射 + 公网 IP 探测 + UPnP-IGD / NAT-PMP / PCP 端口映射。
- **安全** — mTLS 双向认证 + CRL 证书吊销热拦截，内置 CA/PKI 与证书轮换。
- **GeoIP 智能分流** — 用户态 split tunnel + gVisor 协议栈 + IPIP 封装代理，按 GeoIP/CIDR 规则分流到指定出口节点，overlay 开销仅 3.3%。
- **生产级运维** — systemd 安装向导、TUI 管理界面（Bubbletea）、SSH 远程部署工具、doctor 体检。
- **多数据库后端** — GORM 支持 SQLite（纯 Go，零 CGO）/ PostgreSQL / MySQL。
- **跨平台** — Linux（amd64/arm64）、macOS（amd64/arm64）预编译发布。

---

## 🏗️ 架构

### 三种角色

| 角色 | 二进制 | 职责 |
|------|--------|------|
| **Controller** | `corelink-controller` | 控制面中枢：CA/PKI、IPAM、节点注册、ACL、配置下发、拓扑优化、Steward 还政、管理面 API + SPA |
| **Node** | `corelink-node` | 统一节点程序：agent 数据面 + relay 中转能力合一；角色（LEAF 叶子 / TRANSIT 中转）由 Controller 拓扑下发动态决定 |
| **CLI** | `corelink` | 管理命令行：节点 / ACL / 密钥 / 证书 / 路由 / DNS 等管理操作 |

> Node 是「统一二进制」——每个节点都同时具备 agent + relay 全部能力，是否承担中转由拓扑分配的 `TopologyAssignment.Role` 决定，支持运行时角色翻转。

### 配置下发流

1. Controller 为每个节点维护单调递增的 `generation`。
2. 节点通过三通道 failover 同步：`WatchConfig`（gRPC 流）> `/v1/watch`（WebSocket）> `/v1/config`（HTTP 轮询）。
3. Controller 仅推送轻量 `ChangeSignal`，节点收到后 HTTP 拉取完整 `NodeConfig`。
4. `NodeConfig` 含：peers / routes / relays / CRL / 拓扑分配（`TopologyAssignment`）/ DNS / 发布前缀 / 出口规则。

### 拓扑优化（智能并网）

- **输入**：入口上报（`IngressSet`）+ 质量矩阵（`QualityReport`）+ 边事件（`EdgeEvent`）。
- **输出**：per-node `TopologyAssignment`（角色 / 邻居 / 基线路由 / 探测目标 / FIB）。
- 拓扑结果持久化到 `topostore`，Controller 重启后 `Load()` 立即可服务（重启即服务）。
- damping 节流，避免事件风暴下频繁重算。

### Steward（故障转移）

TRANSIT 节点内置 Steward 决策层：

- 周期探活 Controller（`/v1/health`），连续失败判 lost。
- Controller 失联时，通过 mesh aliveness / coronation 帧在 TRANSIT 间选举新 Steward。
- 当选后自动起 A 档服务（降级配置下发），对外仍可服务。
- Controller 恢复后经 `/v1/steward-handoff` 推送快照并还政，epoch 抬升确保 Controller 始终领先。

---

## 🚀 快速开始

### 方式一：源码构建（推荐首次体验）

```bash
# 需要 Go 1.26+
git clone https://github.com/x6nux/corelink.git
cd corelink

# 构建三个主二进制
go build ./cmd/corelink-controller
go build ./cmd/corelink-node
go build ./cmd/corelink
```

**1. 启动 Controller**

```bash
# 交互式配置向导（生成 /etc/corelink-controller.json）
sudo ./corelink-controller config

# 启动服务（serve 为默认子命令）
sudo ./corelink-controller serve -config /etc/corelink-controller.json

# 或直接安装为 systemd 服务
sudo ./corelink-controller install
sudo systemctl start corelink-controller
```

**2. 创建节点注册密钥（enroll key）**

```bash
# 通过管理 CLI 创建注册密钥（详见 corelink key --help）
./corelink key create
```

**3. 启动 Node**

```bash
# 交互式配置向导（填写 controller 地址 + 注册密钥）
sudo ./corelink-node config

# 启动节点（需 root / NET_ADMIN 以创建 TUN）
sudo ./corelink-node -config /etc/corelink-node.json
```

**4. 查看状态**

```bash
./corelink status          # 全网状态
./corelink node ls         # 节点列表
./corelink-controller tui  # Controller 管理 TUI
./corelink-node tui        # Node 管理 TUI
```

### 方式二：Docker Compose（一键拉起 controller + 多 node）

```bash
cd deploy/docker
# 构建镜像（Dockerfile 位于本目录）
docker build -t corelink:test .
# 起一个 controller + 三个 node 的测试拓扑
CORELINK_CA_ENC_KEY="<your-base64-key>" docker compose up
```

> Compose 编排会启动 1 个 controller + 3 个 node（带 `NET_ADMIN` 与 `/dev/net/tun`），适合本地验证 mesh 组网。

### 方式三：下载预编译发布物

前往 [Releases](https://github.com/x6nux/corelink/releases) 页面，下载对应平台（`linux/darwin` × `amd64/arm64`）的 `corelink-controller-*` 与 `corelink-node-*`，校验 `checksums.txt` 后即可使用。

---

## 📦 二进制与子命令

| 二进制 | 用途 |
|--------|------|
| `corelink-controller` | 主控制器（生产） |
| `corelink-node` | 统一节点（生产） |
| `corelink` | 管理 CLI |
| `corelink-deploy` | SSH 远程部署编排工具（测试网运维专用） |

`corelink-controller` 与 `corelink-node` 共享一套运维子命令：

```text
serve          运行主服务（controller 默认子命令）
config         交互式配置向导（生成 JSON）
tui            终端管理界面
install        安装为 systemd 服务
uninstall      卸载服务
update         在线更新二进制
start|stop|restart|enable|disable   服务控制
status         运行状态
info           安装信息
doctor         体检诊断
log            查看日志
version        版本信息
passwd         修改管理面密码（仅 controller）
```

其余管理操作（`node` / `acl` / `key` / `relay` / `cert` / `ca` / `login` / `route` / `dns` / `alias` / `split-tunnel` / `geoip` 等）复用 `corelink` CLI 命令树，详见各子命令 `--help`。

---

## ⚙️ 配置

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

- `ListenAddr` 为统一监听端口（gRPC + HTTP + Admin 共享同一端口，VerifyClientCertIfGiven）。旧字段 `GRPCEnrollAddr`/`GRPCAddr`/`HTTPAddr` 仍可用，加载时自动映射到 `ListenAddr`。
- `DBDSN` 支持 `sqlite://<path>` / `postgres://...` / `mysql://...`。
- CA 加密密钥首次启动自动生成并持久化到数据库（也可用 `CORELINK_CA_ENC_KEY` 环境变量提供）。

### Node (`/etc/corelink-node.json`)

```json
{
  "controller_enroll_addr": "controller:7443",
  "controller_mtls_addr": "controller:7444",
  "controller_http_addr": "controller:8080",
  "enrollment_key": "<enroll-key>",
  "controller_ca_hash": "sha256:<hex>",
  "data_dir": "/var/lib/corelink",
  "role": "agent",
  "tun_name": "corelink%d"
}
```

> 推荐使用 `corelink-controller config` / `corelink-node config` 向导生成，避免手填出错。

---

## 🔌 默认端口

| 端口 | 用途 |
|------|------|
| `:7443` | Controller 统一端口（gRPC + HTTP + Admin 共享，VerifyClientCertIfGiven） |
| `:7445` | STUN 反射 UDP |
| `:7447` | 数据面 TLS 监听（DataPlane Listener，节点间帧传输） |
| Unix socket | `/var/run/corelink-controller.sock`、`/var/run/corelink-node.sock`（TUI RPC） |

---

## 🛠️ 开发

```bash
# 构建
go build ./cmd/corelink-controller
go build ./cmd/corelink-node
go build ./cmd/corelink

# 测试
make test                 # go test ./...
make test-integration     # go test -tags=integration ./...

# 代码检查 / 整理
make lint                 # go vet ./...
make tidy                 # go mod tidy

# Protobuf 代码生成（需 protoc + protoc-gen-go + protoc-gen-go-grpc）
make proto
```

### 前端（管理面 SPA）

```bash
cd web && npm install && npm run build   # Vite + React + TypeScript
```

构建产物 `web/dist/` 通过 `go:embed` 嵌入 Go 二进制，由管理面 HTTP server 提供 SPA 服务。

### 技术栈

- **语言/运行时**：Go 1.26
- **数据面**：自研 TLS 帧传输（bufio 批量写 + channel 消费者模型）、TUN 设备（真实 / fake）
- **自治选路**：ProbeRouter（L1 RTT 探测 + 加权评分 + 自动 failover）
- **智能分流**：gVisor 用户态协议栈 + IPIP 封装 + GeoIP 路由
- **RPC**：gRPC + Protobuf（生成包别名 `genv1`）
- **存储**：GORM（SQLite / PostgreSQL / MySQL）
- **终端 UI**：[Bubbletea](https://github.com/charmbracelet/bubbletea)
- **CLI**：[Cobra](https://github.com/spf13/cobra)
- **前端**：React + Vite + TypeScript
- **隧道传输**：TLS / WebSocket / gRPC / TCP（mTLS 指纹校验）

---

## 📁 目录结构

```text
cmd/                 入口二进制
  corelink-controller/   主控制器（拓扑大脑、Steward 还政、TUI、install/wizard）
  corelink-node/         统一节点（LEAF/TRANSIT 角色自动切换）
  corelink/              管理 CLI（Cobra）
  corelink-deploy/       SSH 远程部署工具

internal/
  controller/         Controller 侧（admin/ca/config/configsvc/enroll/ingress/
                      ipam/relayroster/server/snapshot/store/topology/topoadapter/
                      topostore/acl/routepolicy）
  nodecore/           Node 侧（dataplane/connpool/splittunnel/proberouter/
                      config/dnsproxy/discovery/enroll/firewall/flowtrack/
                      geoip/ingress/keystore/multirelay/portmap/probe/
                      relayclient/snapstore/steward/sync/tun）
  relay/              Relay 中转（server/mesh/forward/session/...）
  rpc/  tui/  pki/  featureflag/  version/  integration/

pkg/
  proto/              Protobuf 定义（corelink/v1/*.proto）与生成代码（gen/）
  tunnel/             隧道传输层（TLS/WS/gRPC/TCP + mTLS）

web/                  管理面 React SPA（Vite + TypeScript）
deploy/docker/        Docker / docker-compose 部署
```

---

## 🗺️ 项目状态

CoreLink 正在积极开发中，已落地核心能力：

- ✅ 节点注册 / CA / IPAM / ACL / 配置下发
- ✅ 拓扑优化大脑（K 路径 / DAG / FIB / 增量优化）
- ✅ 自研数据面全量落地（TLS 帧传输 / ConnPool / DataPlane Listener / 中继转发）
- ✅ VIP 直通路由（FIB / ECMP / DAG / 批量写 / sync.Pool）
- ✅ Probe 自治选路（ProbeRouter / 多维探测 / 加权评分 / 路由缓存）
- ✅ GeoIP 智能分流（gVisor / IPIP 封装 / TUN 层 DNS 拦截）
- ✅ 性能优化（bufio batch Flush + channel 消费者 TUN Write：98% 线速，CPU 30%）
- 🚧 Steward 故障转移（代码已就位，待集成到 corelink-node 二进制）
- 🚧 数据面不中断的角色翻转仍在打磨

---

## 📄 License

本项目**尚未指定开源许可证**——在添加 `LICENSE` 文件前，默认保留所有权利（All Rights Reserved）。

如需使用、二次开发或贡献，请先联系作者确定许可证（推荐 Apache-2.0 或 MIT），或提交 PR 添加 `LICENSE` 文件。

---

<p align="center"><sub>Built with ❤️ by the CoreLink contributors · 一个会自己选路的 mesh 网络</sub></p>
