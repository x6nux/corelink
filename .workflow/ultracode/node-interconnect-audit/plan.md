# Node Interconnection Audit Plan

## Goal
审计 node 互联全链路基础设施是否按 S3-S7 计划正确实现，发现遗漏或实现错误。

## Audit Dimensions

### D1: Ingress Discovery + Topology Optimizer
- node 侧入口发现 5 路合并 (internal/nodecore/ingress/)
- controller 侧入口接收/STUN反射/源观察 (internal/controller/ingress/)
- 拓扑优化器: 拆点图/资格判定/剪枝/K最短路/增量/优化器编排 (internal/controller/topology/)
- 持久化: topostore (internal/controller/topostore/)
- 拓扑服务适配器 (internal/controller/topoadapter/)

### D2: Mesh Routing + Forwarding
- envelope v1/v2/v3 + path hints (internal/relay/envelope/)
- 会话映射 (internal/relay/session/)
- 转发核心: path hint 分支 + 原 dst_relay 分支 (internal/relay/forward/)
- mesh 拆点 K-最短路 + 会话一致性哈希环 (internal/relay/mesh/kpaths.go, session_route.go)
- server 侧 path hint 转发 + 源路由 (internal/relay/server/)
- 互联链路 (internal/relay/mesh/interconnect.go)

### D3: Node Orchestration + Config Flow
- corelink-node 统一编排: 角色动态切换 TRANSIT/LEAF (cmd/corelink-node/)
- 配置同步中 TopologyAssignment 处理 (internal/nodecore/sync/)
- multirelay 按 节点:入口 建链 (internal/nodecore/multirelay/)
- bind 双通道 (internal/nodecore/bind/)
- corelink-controller 装配 (cmd/corelink-controller/)

### D4: Probe + Resilience
- L1 链路状态机四档 (internal/nodecore/probe/linkstate_machine.go)
- 入口级探测 (internal/nodecore/probe/probe.go)
- 分级上报: QualityReport + EdgeEvent (internal/nodecore/probe/report.go)
- 源中转 K 选 1 故障切换 (internal/relay/server/sourceroute.go)
- handoff 迁移窗口 (internal/relay/handoff/)

### D5: Integration + Build Verification
- M4 集成测试覆盖度 (internal/integration/m4_test.go)
- M2/M3 集成测试是否仍通过
- go test -race ./... 全绿
- CGO_ENABLED=0 go build 两个正式二进制
- go vet ./... 通过

## Success Criteria
- 每个维度输出: 已完成项、缺失项、实现错误、风险点
- 高置信度发现需证据支撑（文件路径+行号）
- 最终综合报告包含优先级排序的修复建议
