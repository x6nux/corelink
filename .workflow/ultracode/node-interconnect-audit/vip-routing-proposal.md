# VIP 路由转发方案（后续开发）

## 核心思想
当前的 envelope/session/relay 六七层嵌套过于复杂。改为每个 TRANSIT 节点有一个 VIP，
WG 直接用 VIP 路由（像普通路由器），不需要 envelope/session/relay 转发机制。

## 架构对比

### 当前（envelope 转发）
```
WG → Bind → envelope.Encode → local relay → session.Lookup → 
LocationLookup → SetDstRelay → router.NextHop → 
Sender.SendForward → interconnect → 对端 relay → 
HandleForwardFrame → session.Lookup → Bind → WG
```

### 目标（VIP 路由）
```
WG → TUN → 内核路由表 → 下一跳 TRANSIT VIP → WG tunnel → 对端 TUN
```

## 好处
- 每个中转维护自己的路由表即可
- 不需要 envelope/session/relay/LSDB/LocationLookup
- 标准 IP 路由，用 ip route 就能调试
- WG 原生 AllowedIPs 即路由

## 实施要点
- 每个 TRANSIT 有一个 VIP（已有）
- WG peer 按对端 VIP 设 AllowedIPs
- 路由通过 controller 下发或 gossip 同步
- 多跳靠 WG 的 peer routing（AllowedIPs 覆盖对端子网）
