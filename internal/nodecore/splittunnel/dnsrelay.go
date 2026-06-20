package splittunnel

import (
	"encoding/binary"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x6nux/corelink/internal/transport"
)

// DNSRelay TUN 层透明 DNS 中继：拦截 TUN 读出的 DNS 查询包，
// 通过 overlay DNS 帧发到出口节点解析，响应构造为 IP 包注入回 TUN。
type DNSRelay struct {
	exitVIP netip.Addr
	selfVIP netip.Addr

	// fnMu 保护 sendFn/injectFn 的并发读写（SetSendFn/SetInjectFn 写、InterceptFromTUN/HandleResponse 读）。
	fnMu sync.Mutex
	// sendFn 发送 DNS 帧到出口节点
	sendFn func(dstVIP netip.Addr, dnsPayload []byte) error
	// injectFn 将构造的响应 IP 包注入 TUN（由 DataPlane.InjectInbound 提供）
	injectFn func(pkt []byte)

	// closed 标记是否已关闭，防止 Close 后继续写入 pending
	closed atomic.Bool
	// done 用于通知 cleanupLoop 退出
	done chan struct{}

	// pending DNS 查询（按 DNS Transaction ID 匹配响应）
	mu      sync.Mutex
	pending map[uint16]*pendingDNS
}

// pendingDNS 记录一个待响应的 DNS 查询的原始 IP 包头信息。
type pendingDNS struct {
	origPkt []byte // 原始完整 IP 包（用于构造响应包头）
	sentAt  time.Time
}

// NewDNSRelay 创建 TUN 层 DNS 中继。
func NewDNSRelay(selfVIP, exitVIP netip.Addr) *DNSRelay {
	r := &DNSRelay{
		exitVIP: exitVIP,
		selfVIP: selfVIP,
		pending: make(map[uint16]*pendingDNS),
		done:    make(chan struct{}),
	}
	go r.cleanupLoop()
	return r
}

// SetSendFn 注入 DNS 帧发送函数（线程安全）。
func (r *DNSRelay) SetSendFn(fn func(dstVIP netip.Addr, dnsPayload []byte) error) {
	r.fnMu.Lock()
	r.sendFn = fn
	r.fnMu.Unlock()
}

// SetInjectFn 注入 TUN 写入函数（线程安全，用于将 DNS 响应注入回 TUN）。
func (r *DNSRelay) SetInjectFn(fn func(pkt []byte)) {
	r.fnMu.Lock()
	r.injectFn = fn
	r.fnMu.Unlock()
}

// InterceptFromTUN 处理 wrapper.Read 拦截到的 DNS 查询包。
// origPkt 是完整 IP 包，dnsPayload 是提取的 DNS 报文。
func (r *DNSRelay) InterceptFromTUN(origPkt []byte, dnsPayload []byte) {
	if len(dnsPayload) < 12 {
		return
	}
	// 已关闭则不再接受新查询
	if r.closed.Load() {
		return
	}
	txID := binary.BigEndian.Uint16(dnsPayload[:2])

	// 保存原始包（用于构造响应）
	saved := make([]byte, len(origPkt))
	copy(saved, origPkt)

	r.mu.Lock()
	r.pending[txID] = &pendingDNS{origPkt: saved, sentAt: time.Now()}
	r.mu.Unlock()

	r.fnMu.Lock()
	fn := r.sendFn
	r.fnMu.Unlock()
	if fn != nil {
		if err := fn(r.exitVIP, dnsPayload); err != nil {
			slog.Debug("dnsrelay: 发送 DNS 帧失败", "err", err)
		}
	}
}

// HandleResponse 处理从出口节点返回的 DNS 响应。
// 构造 IP/UDP 响应包注入回 TUN，让应用收到干净的 DNS 解析结果。
func (r *DNSRelay) HandleResponse(dnsPayload []byte) {
	if len(dnsPayload) < 12 {
		return
	}
	txID := binary.BigEndian.Uint16(dnsPayload[:2])

	r.mu.Lock()
	pq, ok := r.pending[txID]
	if ok {
		delete(r.pending, txID)
	}
	r.mu.Unlock()

	r.fnMu.Lock()
	inject := r.injectFn
	r.fnMu.Unlock()
	if !ok || inject == nil {
		return
	}

	// 从原始查询包构造 UDP/IP 响应包
	respPkt := buildDNSResponsePacket(pq.origPkt, dnsPayload)
	if respPkt != nil {
		inject(respPkt)
	}
}

// buildDNSResponsePacket 从原始查询 IP 包和 DNS 响应 payload 构造响应 IP/UDP 包。
// 交换 src/dst IP 和端口，填入 DNS 响应 payload。
func buildDNSResponsePacket(origPkt []byte, dnsResp []byte) []byte {
	if len(origPkt) < 20 {
		return nil
	}
	proto := origPkt[9]
	ihl := int(origPkt[0]&0x0f) * 4

	if proto == 17 && len(origPkt) >= ihl+8 {
		// UDP 响应
		totalLen := ihl + 8 + len(dnsResp)
		pkt := make([]byte, totalLen)

		// 复制 IP 头
		copy(pkt, origPkt[:ihl])
		// 交换 src/dst IP
		copy(pkt[12:16], origPkt[16:20])
		copy(pkt[16:20], origPkt[12:16])
		// 更新 total length
		binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
		pkt[8] = 64 // TTL
		// 清除校验和后重算
		pkt[10], pkt[11] = 0, 0
		binary.BigEndian.PutUint16(pkt[10:12], ipChecksum(pkt[:ihl]))

		// UDP 头：交换 src/dst 端口
		udpOff := ihl
		copy(pkt[udpOff:udpOff+2], origPkt[ihl+2:ihl+4]) // src = orig dst
		copy(pkt[udpOff+2:udpOff+4], origPkt[ihl:ihl+2]) // dst = orig src
		udpLen := uint16(8 + len(dnsResp))
		binary.BigEndian.PutUint16(pkt[udpOff+4:udpOff+6], udpLen)
		pkt[udpOff+6], pkt[udpOff+7] = 0, 0 // UDP 校验和置 0

		// DNS payload
		copy(pkt[udpOff+8:], dnsResp)
		return pkt
	}

	// TCP 暂不支持响应注入（复杂的 TCP 状态机），后续扩展
	return nil
}

// SendDNSFrame 通过 Framer 发送 DNS 帧（工具函数）。
func SendDNSFrame(f *transport.Framer, dstVIP netip.Addr, dnsPayload []byte) error {
	return f.WriteDNS(dstVIP, dnsPayload)
}

// cleanupLoop 每 10s 清理超时的 pending 查询，收到 done 信号后退出。
func (r *DNSRelay) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			r.mu.Lock()
			now := time.Now()
			for id, pq := range r.pending {
				if now.Sub(pq.sentAt) > 5*time.Second {
					delete(r.pending, id)
				}
			}
			r.mu.Unlock()
		}
	}
}

// Close 关闭 DNS 中继，停止 cleanupLoop 并清空 pending。
func (r *DNSRelay) Close() {
	if r.closed.Swap(true) {
		return // 已关闭，避免重复 close(done)
	}
	close(r.done)
	r.mu.Lock()
	r.pending = make(map[uint16]*pendingDNS)
	r.mu.Unlock()
}
