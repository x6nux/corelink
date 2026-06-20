package proberouter

import (
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/x6nux/corelink/internal/transport"
)

// bandwidthTestSize 单次测速数据量（字节）。
const bandwidthTestSize = 2 * 1024 * 1024 // 2MB

// bandwidthFramePayloadSize 单帧填充数据大小（不含帧头开销，约 MTU 可用空间）。
const bandwidthFramePayloadSize = 1350

// ─── 发送端 ─────────────────────────────────────────────────────

// sendBandwidthProbe 对指定目标路由发送带宽测速。
// routeVIPs 为完整路由（via... + target），返回吞吐量 MB/s。
func (pr *ProbeRouter) sendBandwidthProbe(routeVIPs []netip.Addr) (float64, error) {
	if len(routeVIPs) == 0 {
		return 0, nil
	}
	targetVIP := routeVIPs[len(routeVIPs)-1]
	nonce := uint64(time.Now().UnixNano())
	totalPackets := uint32(bandwidthTestSize / bandwidthFramePayloadSize)

	// 注册回复等待
	ch := registerBandwidthWaiter(nonce)
	defer unregisterBandwidthWaiter(nonce)

	// 连续发送填充帧
	for i := uint32(0); i < totalPackets; i++ {
		frame := &transport.BandwidthFrame{
			Nonce:        nonce,
			SeqNo:        i,
			TotalPackets: totalPackets,
			IsLast:       i == totalPackets-1,
			SourceVIP:    pr.cfg.SelfVIP,
		}
		payload := transport.EncodeBandwidthFrame(frame)
		// 填充到 MTU
		padded := make([]byte, bandwidthFramePayloadSize)
		copy(padded, payload)

		// 发送到第一跳（使用标准帧写入，FlagBandwidth 标记）
		pr.dp.SendBandwidthFrame(targetVIP, padded)
	}

	// 等待回复
	select {
	case reply := <-ch:
		mbps := float64(reply.ThroughputBps) / (1024 * 1024)
		slog.Info("bandwidth: 测速完成", "target", targetVIP, "mbps", mbps, "received", reply.ReceivedCount, "total", totalPackets)
		return mbps, nil
	case <-time.After(5 * time.Second):
		return 0, nil
	}
}

// ─── 接收端 ─────────────────────────────────────────────────────

// BandwidthReceiver 管理进行中的带宽测速接收会话。
type BandwidthReceiver struct {
	mu       sync.Mutex
	sessions map[uint64]*bwSession
}

type bwSession struct {
	firstAt       time.Time
	lastAt        time.Time
	receivedCount uint32
	receivedBytes int64
	totalPackets  uint32
	sourceVIP     netip.Addr
	done          bool
}

// NewBandwidthReceiver 创建接收端。
func NewBandwidthReceiver() *BandwidthReceiver {
	return &BandwidthReceiver{sessions: make(map[uint64]*bwSession)}
}

// OnFrame 处理收到的带宽测速帧，返回是否产生 Reply。
func (br *BandwidthReceiver) OnFrame(payload []byte) *transport.BandwidthReply {
	frame, err := transport.DecodeBandwidthFrame(payload)
	if err != nil {
		return nil
	}

	br.mu.Lock()
	s, ok := br.sessions[frame.Nonce]
	if !ok {
		s = &bwSession{
			firstAt:      time.Now(),
			totalPackets: frame.TotalPackets,
			sourceVIP:    frame.SourceVIP,
		}
		br.sessions[frame.Nonce] = s
	}
	s.lastAt = time.Now()
	s.receivedCount++
	s.receivedBytes += int64(len(payload))

	// 终止条件：收到最后一包
	shouldReply := frame.IsLast && !s.done
	if shouldReply {
		s.done = true
	}
	br.mu.Unlock()

	if !shouldReply {
		return nil
	}

	duration := s.lastAt.Sub(s.firstAt)
	if duration <= 0 {
		duration = time.Millisecond
	}
	throughputBps := uint64(float64(s.receivedBytes) / duration.Seconds())

	// 清理会话
	go func() {
		time.Sleep(3 * time.Second)
		br.mu.Lock()
		delete(br.sessions, frame.Nonce)
		br.mu.Unlock()
	}()

	return &transport.BandwidthReply{
		Nonce:         frame.Nonce,
		ThroughputBps: throughputBps,
		DurationNs:    duration.Nanoseconds(),
		ReceivedCount: s.receivedCount,
	}
}

// ─── 等待回复 ───────────────────────────────────────────────────

var bwWaitersMu sync.Mutex
var bwWaiters = make(map[uint64]chan *transport.BandwidthReply)

func registerBandwidthWaiter(nonce uint64) chan *transport.BandwidthReply {
	ch := make(chan *transport.BandwidthReply, 1)
	bwWaitersMu.Lock()
	bwWaiters[nonce] = ch
	bwWaitersMu.Unlock()
	return ch
}

func unregisterBandwidthWaiter(nonce uint64) {
	bwWaitersMu.Lock()
	delete(bwWaiters, nonce)
	bwWaitersMu.Unlock()
}

// DeliverBandwidthReply 投递带宽回复到等待者。
func DeliverBandwidthReply(reply *transport.BandwidthReply) {
	bwWaitersMu.Lock()
	ch, ok := bwWaiters[reply.Nonce]
	bwWaitersMu.Unlock()
	if ok {
		select {
		case ch <- reply:
		default:
		}
	}
}
