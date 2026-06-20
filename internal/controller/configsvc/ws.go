package configsvc

import (
	"net/http"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// wsSignal 是 WebSocket 推送的 JSON 信号体（与 proto ChangeSignal 对齐）。
type wsSignal struct {
	Changed    bool   `json:"changed"`
	Generation uint64 `json:"generation"`
	Epoch      uint64 `json:"epoch"`
}

// ConfigWS 是 WebSocket 备通道 handler。
//
// mTLS 在外层 http.Server.TLSConfig{ClientAuth:RequireAndVerifyClientCert} 实现；
// nodeID 从 r.TLS.PeerCertificates[0].Subject.CommonName 取。
//
// 每个 WebSocket 连接：
//  1. 提取 nodeID（mTLS CN）。
//  2. 订阅 Notify，循环将信号以 JSON 推送给客户端。
//  3. 连接关闭 → Unsubscribe。
type ConfigWS struct {
	notify *Notify
	getter NodeInfoGetter

	// epoch 指向 Services.epoch（共享原子值），读取当前 控制平面纪元。
	// nil 时退化为恒 0，保持向后兼容。
	epoch *atomic.Uint64
}

// NewConfigWS 构造 ConfigWS handler。
// epoch 字段为 nil（退化恒 0，向后兼容），如需动态 epoch 请用 newConfigWSWithEpoch。
func NewConfigWS(n *Notify, getter NodeInfoGetter) *ConfigWS {
	return &ConfigWS{notify: n, getter: getter}
}

// newConfigWSWithEpoch 是 New() 使用的内部构造函数，注入共享 epoch 指针。
func newConfigWSWithEpoch(n *Notify, getter NodeInfoGetter, epoch *atomic.Uint64) *ConfigWS {
	return &ConfigWS{notify: n, getter: getter, epoch: epoch}
}

// loadEpoch 安全读取 epoch 值；epoch 指针为 nil 时返回 0（向后兼容）。
func (h *ConfigWS) loadEpoch() uint64 {
	if h.epoch == nil {
		return 0
	}
	return h.epoch.Load()
}

// ServeHTTP 实现 http.Handler。
// 挂载到 mTLS HTTP mux 的路径上（例如 /v1/watch）。
func (h *ConfigWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 从 mTLS 证书提取 nodeID
	nodeID, err := NodeIDFromTLSCerts(r.TLS)
	if err != nil {
		http.Error(w, "mTLS 认证失败: "+err.Error(), http.StatusUnauthorized)
		return
	}

	// 查询初始 generation
	info, err := h.getter.GetNodeInfo(nodeID)
	if err != nil {
		http.Error(w, "节点不存在", http.StatusNotFound)
		return
	}

	serveWSWithNodeID(w, r, nodeID, info.Generation, h.notify, h.loadEpoch())
}

// serveWSWithNodeID 升级 WebSocket 并循环推送变更信号。
// 抽取为独立函数方便测试直接调用（绕过 mTLS 证书提取）。
// initEpoch 是连接建立时的 epoch 快照（由调用方通过 loadEpoch() 传入）。
// 后续推送使用相同 epoch 快照：WS 连接期间 epoch 不会因 SetEpoch 突变（简化实现，
// 连接重建时自动取到新 epoch）。
func serveWSWithNodeID(w http.ResponseWriter, r *http.Request, nodeID string, initGen uint64, n *Notify, initEpoch uint64) {
	// 升级为 WebSocket
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept 内部已写入 HTTP 错误响应
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()

	// 推送初始信号（S3-A3：携带当前 epoch）
	initSig := wsSignal{Changed: true, Generation: initGen, Epoch: initEpoch}
	if err := wsjson.Write(ctx, conn, initSig); err != nil {
		return
	}

	// 订阅变更通知
	sid, ch := n.Subscribe(nodeID)
	defer n.Unsubscribe(nodeID, sid)

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case msg, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			sig := wsSignal{Changed: msg.Changed, Generation: msg.Generation, Epoch: initEpoch} // S3-A3：使用连接建立时的 epoch 快照
			if err := wsjson.Write(ctx, conn, sig); err != nil {
				return
			}
		}
	}
}
