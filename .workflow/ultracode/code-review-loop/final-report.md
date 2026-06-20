# 全仓库代码审计 — 最终报告

## 审计概览

| 项目 | 数值 |
|------|------|
| 代码规模 | 516 Go 文件, 103,732 行 |
| 审计包数 | ~70 个 (分 6 批并行) |
| 总轮次 | 6 轮 (4 轮修复 + 2 轮确认) |
| 总计修复 | **9 P0 + 16 P1** |
| P2 记录 | ~51 项 (不修复) |
| 提交数 | 4 个 (未推送) |

## 提交历史

| 提交 | 说明 |
|------|------|
| `452c6d8` | Round 1: 修复 7 P0 + 14 P1 (20 文件, +167/-60) |
| `57122b6` | Round 2: 修复 1 P1 (split_tunnel_repo errors.Is) |
| `db6624d` | Round 3: 修复 1 P0 (chanConn double-close) |
| `feb0bc5` | Round 4: 修复 1 P0 + 1 P1 (splittunnel 竞态) |

## P0 修复清单 (9)

1. **admin RecomputeAndNotify 零参** — aliases/routes/dns/split_tunnel 配置变更不下发
2. **admin Notify nil 解引用** — 部分初始化时 HTTP server panic
3. **steward server 资源泄漏** — 快速重选举时 DB/socket/goroutine 泄漏
4. **interconnect 指纹校验绕过** — RegisterInboundConn 零信任白名单被绕过
5. **featureflag FromMap 数据竞争** — 无锁写入并发可见
6. **ecmp RendezvousSelect panic** — weights/peerIDs 长度不匹配
7. **ecmp 全零权重逻辑错误** — 返回 index 0 而非 -1
8. **chanConn double-close panic** — 共享 done channel 各自 sync.Once
9. **splittunnel gstack nil 解引用** — Cleanup 并发置 nil + Read 路径无锁读

## P1 修复清单 (16)

1. store/admin_repo — errors.Is(gorm.ErrRecordNotFound)
2. ipam — /0 CIDR 整数溢出边界校验
3. configsvc/discovery — RecomputeAndNotify 传入 nodeID
4. topology/fib — InjectPublishedPrefixes NextHops 深拷贝
5. routepolicy — buildDNSConfig JSON 解析错误日志
6. relayroster — UpsertRelayInfo 错误日志
7. routepolicy — discoveredTargetsByRoute 提升到循环外 + 边界检查
8. dnsproxy — buildAResponse 清零 NSCOUNT/ARCOUNT
9. firewall — EnsureChains/Cleanup 统一走 run() 路径回退
10. relay/server — SetPeerIndexMap/SetVIPInject atomic.Pointer
11. rpc/logbuffer — Handle 输出 WithAttrs 属性
12. corelink-node — Close() 锁内取出字段锁外清理
13. corelink-node — UpdateTopology goroutine 持锁捕获 ic
14. controller(legacy) — 密码从 slog 改为 stderr
15. split_tunnel_repo — GetLatestGeoIPMeta errors.Is
16. splittunnel — localVIP/exitVIP atomic.Pointer[vipConfig]

## 结束条件

Round 5 + Round 6 连续两轮 0 P0/P1 → 循环终止。
