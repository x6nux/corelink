# 循环代码审计计划

## 目标
以包为单位对 CoreLink 全仓库进行循环代码审计,修复 P0/P1 级别问题,直到连续两轮 review 结果为 0 P0/P1。

## 严重等级定义

| 级别 | 定义 | 处理 |
|------|------|------|
| P0 | 崩溃/panic、死锁、数据损坏、安全漏洞、资源泄漏导致 OOM、核心逻辑错误 | 必须修复 |
| P1 | 竞态条件、错误处理丢失上下文、API 契约违反、热路径内存泄漏、可靠性逻辑错误 | 必须修复 |
| P2+ | 命名风格、轻微低效、注释缺失、测试覆盖率、代码整洁度 | 记录不修复 |

## 包分组 (6 批并行)

| 批次 | 名称 | 包 |
|------|------|-----|
| 1 | controller-core | controller, config, server, store, ca, ipam, enroll, configsvc, admin |
| 2 | controller-advanced | topology, topoadapter, topostore, acl, routepolicy, ingress, relayroster, snapshot, geoipdb |
| 3 | node-core | bind, nodecore/config, wg, tun, sync, nodecore/enroll, keystore, multirelay |
| 4 | node-advanced | probe, steward, ingress, firewall, dnsproxy, discovery, splittunnel, portmap, snapstore, relayclient, geoip, jointoken |
| 5 | relay | server, mesh, forward, session, envelope, wgrouter, ratelimit, health, handoff, keepalive, location, locationcache |
| 6 | shared-cmd | ecmp, pki, featureflag, version, tlsext, rpc, tui, tunnel, cmd/* |

## 循环流程

```
Round N:
  1. 启动 6 个并行 review agent (每批一个)
  2. 收集结果,汇总 P0/P1 发现
  3. 若 P0/P1 > 0:
     a. 修复所有 P0/P1
     b. 跑 go test ./... 必须全绿
     c. 创建提交 (不推送)
     d. consecutive_clean = 0, 进入 Round N+1
  4. 若 P0/P1 == 0:
     a. consecutive_clean++
     b. 若 consecutive_clean >= 2: 结束
     c. 否则进入 Round N+1
```

## 约束
- 不推送 (git push)
- 每轮修复后创建一个提交
- P2+ 记录到 results/ 不修复
- wireguard fork 和 proto/gen 不审计 (外部/生成代码)
- 测试必须全绿才能提交

## 验证
- `go test ./...` 全通过
- `go vet ./...` 无新增警告
- 连续两轮 0 P0/P1
