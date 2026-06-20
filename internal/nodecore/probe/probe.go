// Package probe 实现 L1 链路状态机与分级上报（规格 §4.5）。
//
// 每条邻居链路（探测目标 ProbeTarget）在 node 本地维护一个 LinkFSM 状态机，
// 由周期探测结果驱动。状态机按"持续时间 + 是否断连"四档逻辑决定：
//   - 瞬时劣化（劣化持续 < THold）：本地消化，不上报；
//   - 断连（连续 DownConfirm 次失败）：立即上报 Down；
//   - 持续劣化（劣化持续 ≥ THold）：上报 Degraded；
//   - 恢复（Down/Degraded 后稳定 ≥ TRecover 回 Healthy）：上报 Recovered。
//
// 双通道上报（Reporter）：
//   - 事件上报（高优先级）：FSM 转换事件 → EmitEvent（EdgeEvent，gRPC ReportEdgeEvent）；
//   - 周期质量上报（低频背景，带 damping）：质量样本矩阵 → EmitQuality（QualityReport）。
//
// 所有时间均通过注入 clock 获取，便于确定性测试（无真实 sleep / 网络）。
package probe

// Prober 是注入的入口级探测函数。
//
// 给定 ingressID（探测目标入口），返回该入口的 RTT（毫秒）、丢包率（千分比）
// 以及探测是否成功（ok=false 表示超时 / 不可达）。
//
// 生产实现由轻量 UDP 探测完成；测试注入 fake Prober 返回确定性结果。
type Prober func(ingressID string) (rttMs uint32, lossPermille uint32, ok bool)

// ProbeTarget 标识一条待探测的邻居链路。
//
// NodeID 是目标物理节点，IngressID 是该节点上被探测的具体入口（拆点后的入口）。
// controller 下发的 probe_targets（Task2）展开为一组 ProbeTarget。
type ProbeTarget struct {
	NodeID    string
	IngressID string
}

// Key 返回 ProbeTarget 的唯一键（用于 Reporter 内部 LinkFSM 索引）。
func (t ProbeTarget) Key() string {
	return t.NodeID + "\x00" + t.IngressID
}

// ProbeOnce 对单个 ProbeTarget 执行一次探测并返回原始结果。
//
// 这是探测调度的最小单元：调用注入的 Prober 探测目标入口。
// 本 task 聚焦状态机 + 上报，探测调度保持简单（外部周期 Tick 驱动 +
// 对候选探测集逐个调用 ProbeOnce，再喂给 Reporter.OnProbe）。
func ProbeOnce(p Prober, t ProbeTarget) (rttMs uint32, lossPermille uint32, ok bool) {
	return p(t.IngressID)
}
