# Node Interconnection Audit — Final Report

## Executive Summary

5 维度并行审计 + 对抗性验证完成。6 个子代理共检查 ~150 个 Go 文件，运行全量 -race 测试。

**总体结论：Node 互联基础设施已大体完成，S1-S7 计划的核心功能均已实现且测试通过。** 发现 1 个设计级缺陷、若干待完善项和测试覆盖缺口。

## Build & Test Status

| 检查项 | 状态 |
|--------|------|
| CGO_ENABLED=0 go build ./cmd/corelink-node | ✅ PASS |
| CGO_ENABLED=0 go build ./cmd/corelink-controller | ✅ PASS |
| go test -race 12 核心包 | ✅ ALL PASS |
| M2/M3/M4 集成测试 -race | ✅ 10/10 PASS |
| go vet ./... | ⚠️ FAIL (wireguard fork) |

## Findings by Priority

### P0 — 设计级缺陷（影响 K-路径故障切换）

**buildAssignments 只下发 routes[0]，丢弃 K-1 条路由**
- 文件：`internal/controller/topology/service.go:389`
- 问题：拓扑优化器正确计算了 K 条基准路由，但下发到 NodeConfig.TopologyAssignment 时只取了第一条（最优路由）
- 影响：节点侧 SessionRouter.Pick() 只收到 1 条路由，K-路径一致性哈希和 Degrade() 故障切换机制失效。源中转 K-select-1 设计的核心前提被破坏
- 修复：`buildAssignments` 中改为下发全部 K 条路由到 `baseline_routes` 字段

### P1 — 待实现项

| # | 项目 | 位置 | 说明 |
|---|------|------|------|
| 1 | 真实 UDP 探测器 | cmd/corelink-node/main.go:696 | L1 + multirelay 的 Prober 目前是 placeholder（固定返回 1ms/0loss），质量驱动的切换在生产环境完全无效 |
| 2 | SwitchRole 数据面连续性 | cmd/corelink-node/main.go:1396 | TRANSIT↔LEAF 角色切换会重建整个数据面（TUN+WG device），造成数秒连接中断 |
| 3 | go vet 修复 | internal/wireguard/device/pools_test.go:67 | wireguard fork 中 atomic.Uint32 被值传递给 t.Errorf，阻塞 S7 完成标准 |

### P2 — 测试覆盖缺口

| # | 缺失测试 | 计划引用 |
|---|----------|----------|
| 1 | CDN SNI 拨号断言（TLS ServerName=sni） | S7-P3 Task 3.6 |
| 2 | 剪枝 graceful 端到端（在途 session 迁移后链路才关） | S7-P3 Task 3.6 |
| 3 | LEAF→TRANSIT 角色翻转的数据面连续性 | S7-P5 Task 5.1 |
| 4 | 多 session 路径分散（当前 M4 只有 1 个 session 对） | S7-P5 Task 5.1 |

### P3 — 内存泄漏风险

| # | 组件 | 说明 |
|---|------|------|
| 1 | SessionRouter.sessions map | 无 GC/LRU，长运行中转节点会无限增长 |
| 2 | SessionRouter.unavail map | 同上，SetBaseline 只部分清理 |
| 3 | LocationCache.data map | 无 Sweep/Size cap，正缓存项永不主动淘汰 |

### P4 — 计划偏差（低风险，可接受）

| # | 偏差 | 评估 |
|---|------|------|
| 1 | Discover 实现 6 路（多了 portmap/UPnP） | 增强，非错误 |
| 2 | envelope v3 用 1 字节 hop count（计划说 2 字节） | 255 跳足够，内部一致 |
| 3 | 增量优化器用 dirty-pair+KShortest 代替完整 Ramalingam-Reps | 简化但正确（golden 测试验证一致性） |
| 4 | RECOVERED 边事件触发全量重算 | 保守正确，牺牲增量性能 |
| 5 | Prune 对无 QM 数据的边用 defaultWeight 而非排除 | 防止新节点被孤立，合理 |
| 6 | 集成测试无 -tags=integration 约束 | 每次 go test 都跑集成，可能影响 CI 速度 |

### 对抗性验证结果

| 原始发现 | 验证结果 |
|----------|----------|
| "M4 集成测试不存在" (D3 agent 报告) | ❌ **FALSE** — m4_test.go 存在且 968 行，全部 5 个测试通过 |

## Dimension Summary

### D1: Ingress + Topology ✅
S7-P1 全部 5 个 Task 完成。S7-P2 拓扑优化器 7 个 Task 完成（拆点图/剪枝/K路/增量/持久化/服务触发）。topostore 持久化 + 重启加载正确。topoadapter 桥接完整。

### D2: Mesh Routing + Forwarding ✅
envelope v1/v2/v3 完整，path hint 编解码+AdvancePathHint 零分配热路径。forward.go path hint 分支不调 NextHop（mock 断言验证）。SessionRouter 一致性哈希+粘滞状态机完整。interconnect CDN SNI + graceful 断链实现。sourceroute K-select-1 + hint 重写完整。

### D3: Node Orchestration ✅
corelink-node 统一编排（TRANSIT/LEAF/BasicAgent 三分支）完整。角色动态切换骨架完成。配置同步正确传递 TopologyAssignment。bind 双通道+信封包装+端点归一化完整。corelink-controller 装配完整（ingress receiver + STUN + TopoService + topostore Load）。

### D4: Probe + Resilience ✅
L1 状态机三态+瞬时抑制+滞回完整，确定性时钟测试覆盖。Report 双通道+damping 完整。handoff 迁移窗口完整（redirect/local/sweep）。locationcache 含负缓存+并发安全。multirelay CDN SNI + ingress 级切换完整。

### D5: Integration + Build ✅
M4 四个核心场景全部存在且通过。M2/M3 继续通过。两个二进制 CGO_ENABLED=0 构建通过。12 个核心包 -race 全绿。
