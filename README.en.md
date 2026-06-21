# CoreLink

**English** | [简体中文](README.md)

> A self-organizing overlay VPN / mesh networking system in Go — with intelligent topology orchestration and control-plane self-healing.

CoreLink stitches arbitrarily distributed nodes into one encrypted virtual private network (mesh), much like [Tailscale](https://tailscale.com/) / [ZeroTier](https://www.zerotier.com/). Nodes auto-enroll, receive virtual IPs, and self-organize based on real link quality. The custom data plane uses TLS-framed transport achieving **98% wire speed** (983 Mbps on 1 Gbps), with NAT traversal, relay forwarding, multi-hop routing, and GeoIP-aware split tunneling — and when the controller goes down, TRANSIT nodes elect a Steward to keep the network running.

```text
                 ┌────────────────────────────────────────────┐
                 │              Controller (control plane)    │
                 │  CA/PKI · IPAM · Enroll · ACL · Topo brain │
                 │  Config delivery · Admin API · Handoff     │
                 └───────────────┬────────────────────────────┘
            gRPC(mTLS) / WS / HTTP│ 3-channel failover config sync
        ┌─────────────────────────┼─────────────────────────┐
        ▼                         ▼                         ▼
  ┌──────────┐              ┌──────────┐              ┌──────────┐
  │  Node A  │◄── mesh ────►│  Node B  │◄── mesh ────►│  Node C  │
  │ (TRANSIT)│   interconn/ │ (TRANSIT)│              │  (LEAF)  │
  └────┬─────┘    relay      └────┬─────┘              └────┬─────┘
       │ Custom data plane (TLS)  │                         │
       └────────── 100.64.0.0/10 virtual subnet (VIP passthrough) ──┘
```

---

## Table of Contents

- [✨ Features](#-features)
- [🏗️ Architecture](#️-architecture)
- [🚀 Quick Start](#-quick-start)
- [📦 Binaries & Subcommands](#-binaries--subcommands)
- [⚙️ Configuration](#️-configuration)
- [🔌 Default Ports](#-default-ports)
- [🛠️ Development](#️-development)
- [📁 Project Layout](#-project-layout)
- [🗺️ Project Status](#️-project-status)
- [📄 License](#-license)

---

## ✨ Features

- **Zero-touch networking** — nodes auto-enroll and receive a virtual IP; no manual IP/route configuration.
- **Intelligent topology orchestration** — the `TopoService` "topology brain" recomputes periodically and on events, assigning LEAF / TRANSIT roles and computing K-paths + FIB tables based on real link quality (RTT / loss).
- **High-performance custom data plane** — a purpose-built TLS framing protocol (4B length-prefix + VIP routing header + payload) with bufio batch writes and a channel-based single-consumer TUN writer; benchmarked at **98% wire speed** (983 Mbps on 1 Gbps) with only 30% CPU usage.
- **Autonomous probe-based routing** — nodes run periodic L1 quality probes (TCP RTT / LinkState FSM / multi-relay 3D probing); ProbeRouter scores paths by throughput×0.6 + latency×0.4 and auto-selects the optimal route.
- **VIP passthrough routing** — FIB tables (ECMP / DAG) for multi-hop routing; relay forwarding via kernel ip_forward is fully transparent.
- **Steward self-healing** — TRANSIT nodes run a decision layer that probes the controller, elect a new Steward over the mesh when it's unreachable, and spin up a degraded control plane (Tier-A service); control is handed back automatically on recovery.
- **3-channel config sync** — gRPC server stream → WebSocket → HTTP polling, with automatic failover; a monotonic `(epoch, generation)` version ensures nodes only ever accept newer configs.
- **NAT traversal** — built-in STUN reflection + public-IP probing + UPnP-IGD / NAT-PMP / PCP port mapping.
- **Security** — mutual TLS with hot CRL revocation interception, built-in CA/PKI and certificate rotation.
- **GeoIP-aware split tunneling** — userspace split tunnel with gVisor network stack + IPIP encapsulation proxy, routing traffic to designated exit nodes by GeoIP/CIDR rules with only 3.3% overlay overhead.
- **Production-grade ops** — systemd install wizards, TUI admin consoles (Bubbletea), an SSH remote-deploy tool, and a `doctor` health check.
- **Multi-database** — GORM backends: SQLite (pure Go, zero CGO) / PostgreSQL / MySQL.
- **Cross-platform** — prebuilt releases for Linux and macOS (amd64 / arm64).

---

## 🏗️ Architecture

### Three Roles

| Role | Binary | Responsibility |
|------|--------|----------------|
| **Controller** | `corelink-controller` | Control-plane hub: CA/PKI, IPAM, enrollment, ACL, config delivery, topology optimization, Steward handoff, admin API + SPA |
| **Node** | `corelink-node` | Unified node binary: agent data plane + relay capability in one; role (LEAF leaf / TRANSIT transit) is assigned dynamically by the controller's topology |
| **CLI** | `corelink` | Management CLI: nodes / ACL / keys / certs / routes / DNS, etc. |

> The Node is a *unified binary* — every node carries the full agent + relay capability; whether it actually transits traffic is decided at runtime by the `TopologyAssignment.Role` it receives. Role flips are supported.

### Config Delivery Flow

1. The controller maintains a monotonically increasing `generation` per node.
2. Nodes sync over three failover channels: `WatchConfig` (gRPC stream) > `/v1/watch` (WebSocket) > `/v1/config` (HTTP polling).
3. The controller pushes only a lightweight `ChangeSignal`; nodes then HTTP-pull the full `NodeConfig`.
4. `NodeConfig` contains: peers / routes / relays / CRL / topology assignment (`TopologyAssignment`) / DNS / advertised prefixes / egress rules.

### Topology Optimization (Smart Meshing)

- **Inputs**: ingress reports (`IngressSet`) + quality matrix (`QualityReport`) + edge events (`EdgeEvent`).
- **Outputs**: per-node `TopologyAssignment` (role / neighbors / baseline routes / probe targets / FIB).
- Results are persisted to `topostore`; on controller restart, `Load()` makes assignments immediately available ("serve on restart").
- Damping throttles recomputation to survive event storms.

### Steward (Failover)

TRANSIT nodes run a Steward decision layer:

- Periodically probe the controller (`/v1/health`); sustained failure marks it lost.
- When the controller is lost, TRANSIT nodes elect a new Steward over the mesh via aliveness / coronation frames.
- The elected Steward spins up Tier-A service (degraded config delivery) so the network keeps serving.
- On controller recovery, the Steward pushes a snapshot to `/v1/steward-handoff` and hands back control; epoch is bumped so the controller always leads.

---

## 🚀 Quick Start

### Option 1: Build from source (recommended for first run)

```bash
# Requires Go 1.26+
git clone https://github.com/x6nux/corelink.git
cd corelink

# Build the three primary binaries
go build ./cmd/corelink-controller
go build ./cmd/corelink-node
go build ./cmd/corelink
```

**1. Start the Controller**

```bash
# Interactive config wizard (writes /etc/corelink-controller.json)
sudo ./corelink-controller config

# Run the service (serve is the default subcommand)
sudo ./corelink-controller serve -config /etc/corelink-controller.json

# Or install as a systemd service
sudo ./corelink-controller install
sudo systemctl start corelink-controller
```

**2. Create an enrollment key**

```bash
# Create an enrollment key via the management CLI (see `corelink key --help`)
./corelink key create
```

**3. Start a Node**

```bash
# Interactive config wizard (fill in controller address + enrollment key)
sudo ./corelink-node config

# Run the node (needs root / NET_ADMIN to create the TUN device)
sudo ./corelink-node -config /etc/corelink-node.json
```

**4. Inspect state**

```bash
./corelink status          # network-wide status
./corelink node ls         # list nodes
./corelink-controller tui  # Controller admin TUI
./corelink-node tui        # Node admin TUI
```

### Option 2: Docker Compose (spin up controller + nodes)

```bash
cd deploy/docker
# Build the image (Dockerfile lives in this directory)
docker build -t corelink:test .
# Bring up one controller + three nodes as a test topology
CORELINK_CA_ENC_KEY="<your-base64-key>" docker compose up
```

> The compose file starts 1 controller + 3 nodes (with `NET_ADMIN` and `/dev/net/tun`) — handy for validating mesh formation locally.

### Option 3: Download a prebuilt release

Visit the [Releases](https://github.com/x6nux/corelink/releases) page, grab `corelink-controller-*` and `corelink-node-*` for your platform (`linux/darwin` × `amd64/arm64`), verify against `checksums.txt`, and run.

---

## 📦 Binaries & Subcommands

| Binary | Purpose |
|--------|---------|
| `corelink-controller` | Primary controller (production) |
| `corelink-node` | Unified node (production) |
| `corelink` | Management CLI |
| `corelink-deploy` | SSH remote-deploy orchestration tool (test-network ops only) |

`corelink-controller` and `corelink-node` share a common ops subcommand set:

```text
serve          run the main service (controller's default subcommand)
config         interactive config wizard (writes JSON)
tui            terminal admin UI
install        install as a systemd service
uninstall      remove the service
update         self-update the binary
start|stop|restart|enable|disable   service control
status         runtime status
info           installation info
doctor         diagnostic health check
log            tail logs
version        version info
passwd         change admin password (controller only)
```

All other management operations (`node` / `acl` / `key` / `relay` / `cert` / `ca` / `login` / `route` / `dns` / `alias` / `split-tunnel` / `geoip`, …) reuse the `corelink` CLI command tree — see each subcommand's `--help`.

---

## ⚙️ Configuration

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

- `ListenAddr` is the unified listen port (gRPC + HTTP + Admin share a single port with VerifyClientCertIfGiven). Legacy fields `GRPCEnrollAddr`/`GRPCAddr`/`HTTPAddr` are still accepted and auto-mapped to `ListenAddr`.
- `DBDSN` accepts `sqlite://<path>` / `postgres://...` / `mysql://...`.
- The CA encryption key is auto-generated on first launch and persisted to the DB (or provide one via the `CORELINK_CA_ENC_KEY` env var).

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

> Prefer the `corelink-controller config` / `corelink-node config` wizards to generate these and avoid hand-editing errors.

---

## 🔌 Default Ports

| Port | Purpose |
|------|---------|
| `:7443` | Controller unified port (gRPC + HTTP + Admin shared, VerifyClientCertIfGiven) |
| `:7445` | STUN reflection UDP |
| `:7447` | DataPlane TLS listener (node-to-node frame transport) |
| Unix socket | `/var/run/corelink-controller.sock`, `/var/run/corelink-node.sock` (TUI RPC) |

---

## 🛠️ Development

```bash
# Build
go build ./cmd/corelink-controller
go build ./cmd/corelink-node
go build ./cmd/corelink

# Test
make test                 # go test ./...
make test-integration     # go test -tags=integration ./...

# Lint / tidy
make lint                 # go vet ./...
make tidy                 # go mod tidy

# Protobuf codegen (requires protoc + protoc-gen-go + protoc-gen-go-grpc)
make proto
```

### Frontend (Admin SPA)

```bash
cd web && npm install && npm run build   # Vite + React + TypeScript
```

The build output in `web/dist/` is embedded into the Go binary via `go:embed` and served as an SPA by the admin HTTP server.

### Tech Stack

- **Language / runtime**: Go 1.26
- **Data plane**: custom TLS framing (bufio batch write + channel consumer model), TUN device (real / fake)
- **Autonomous routing**: ProbeRouter (L1 RTT probing + weighted scoring + auto failover)
- **Smart split tunneling**: gVisor userspace network stack + IPIP encapsulation + GeoIP routing
- **RPC**: gRPC + Protobuf (generated package alias `genv1`)
- **Storage**: GORM (SQLite / PostgreSQL / MySQL)
- **Terminal UI**: [Bubbletea](https://github.com/charmbracelet/bubbletea)
- **CLI**: [Cobra](https://github.com/spf13/cobra)
- **Frontend**: React + Vite + TypeScript
- **Tunnel transport**: TLS / WebSocket / gRPC / TCP (mTLS fingerprint verification)

---

## 📁 Project Layout

```text
cmd/                 entry-point binaries
  corelink-controller/   primary controller (topo brain, Steward handoff, TUI, install/wizard)
  corelink-node/         unified node (LEAF/TRANSIT role auto-switching)
  corelink/              management CLI (Cobra)
  corelink-deploy/       SSH remote-deploy tool

internal/
  controller/         controller side (admin/ca/config/configsvc/enroll/ingress/
                      ipam/relayroster/server/snapshot/store/topology/topoadapter/
                      topostore/acl/routepolicy)
  nodecore/           node side (dataplane/connpool/splittunnel/proberouter/
                      config/dnsproxy/discovery/enroll/firewall/flowtrack/
                      geoip/ingress/keystore/multirelay/portmap/probe/
                      relayclient/snapstore/steward/sync/tun)
  relay/              relay transit (server/mesh/forward/session/...)
  rpc/  tui/  pki/  featureflag/  version/  integration/

pkg/
  proto/              protobuf definitions (corelink/v1/*.proto) and generated code (gen/)
  tunnel/             tunnel transport (TLS/WS/gRPC/TCP + mTLS)

web/                  admin React SPA (Vite + TypeScript)
deploy/docker/        Docker / docker-compose deployment
```

---

## 🗺️ Project Status

CoreLink is under active development. Core capabilities already landed:

- ✅ Enrollment / CA / IPAM / ACL / config delivery
- ✅ Topology optimization brain (K-path / DAG / FIB / incremental optimization)
- ✅ Custom data plane fully landed (TLS framing / ConnPool / DataPlane Listener / relay forwarding)
- ✅ VIP passthrough routing (FIB / ECMP / DAG / batch write / sync.Pool)
- ✅ Probe-based autonomous routing (ProbeRouter / multi-dimensional probing / weighted scoring / route caching)
- ✅ GeoIP-aware split tunneling (gVisor / IPIP encapsulation / TUN-layer DNS interception)
- ✅ Performance optimization (bufio batch Flush + channel consumer TUN Write: 98% wire speed, 30% CPU)
- 🚧 Steward failover (code ready, pending integration into corelink-node binary)
- 🚧 Interruption-free role flips still being polished

---

## 📄 License

This project **does not yet have an open-source license** — until a `LICENSE` file is added, all rights are reserved by default.

If you want to use, extend, or contribute, please contact the author to agree on a license (Apache-2.0 or MIT recommended), or open a PR adding a `LICENSE` file.

---

<p align="center"><sub>Built with ❤️ by the CoreLink contributors · a mesh network that routes itself</sub></p>
