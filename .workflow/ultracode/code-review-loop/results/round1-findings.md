# Round 1 审计结果

## P0 (7)

| # | 文件 | 问题 |
|---|------|------|
| 1 | admin/aliases.go:68 | RecomputeAndNotify() 零参调用为 no-op，节点永远收不到别名/路由/DNS/分流变更通知 |
| 2 | admin/aliases.go:68 | s.deps.Notify 无 nil 检查，测试或部分初始化时 panic |
| 3 | steward/steward.go:243 | 快速重选举时资源泄漏（DB连接、监听端口、goroutine） |
| 4 | relay/mesh/interconnect.go:945 | RegisterInboundConn 跳过指纹校验，零信任邻居白名单被绕过 |
| 5 | featureflag/flag.go:24 | FromMap 写 flags 无锁保护，数据竞争 |
| 6 | ecmp/ecmp.go:18 | weights/peerIDs 长度不匹配时 panic |
| 7 | ecmp/ecmp.go:15 | 全零权重返回 index 0 而非 -1 |

## P1 (14)

| # | 文件 | 问题 |
|---|------|------|
| 1 | store/admin_repo.go:10 | 字符串比较 vs errors.Is(gorm.ErrRecordNotFound) |
| 2 | ipam/ipam.go:69 | /0 CIDR 整数溢出导致分配循环挂起 |
| 3 | configsvc/discovery.go:102 | RecomputeAndNotify() 零参 (同 P0#1) |
| 4 | topology/fib.go:206 | NextHops 切片指针共享，下游修改会交叉污染 |
| 5 | routepolicy/resolve.go:175 | buildDNSConfig 静默丢弃 JSON 反序列化错误 |
| 6 | relayroster/roster.go:104 | UpsertRelayInfo 错误被静默忽略 |
| 7 | routepolicy/resolve.go:102 | discoveredTargetsByRoute 循环内重复调用 + 索引对齐脆弱 |
| 8 | dnsproxy/proxy.go:212 | DNS 响应未清零 NSCOUNT/ARCOUNT，EDNS 查询解析失败 |
| 9 | firewall/manager.go:44 | iptables 二进制路径不一致，EnsureChains 可能永远失败 |
| 10 | relay/server/server.go:127 | SetPeerIndexMap/SetVIPInject 与 onFrame 数据竞争 |
| 11 | rpc/logbuffer.go:90 | WithAttrs 注册的属性在 Handle 中被丢弃 |
| 12 | cmd/corelink-node/main.go:1819 | Close() vs ApplyConfig() 数据竞争 |
| 13 | cmd/corelink-node/main.go:1678 | UpdateTopology goroutine 读 a.ic 无锁保护 |
| 14 | cmd/controller/main.go:309 | 管理员密码明文写入结构化日志 |

## P2 (51) — 仅记录
各批次 P2 详见 workflow output，不做修复。
