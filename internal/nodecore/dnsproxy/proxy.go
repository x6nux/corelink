// Package dnsproxy 提供最小 DNS 代理：内部域名直接应答，其余转发上游。
package dnsproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"github.com/x6nux/corelink/pkg/tunnel"
)

// Proxy 是一个 DNS UDP 代理。
type Proxy struct {
	listenAddr string
	upstreams  []string
	records    map[string]string // fqdn(lower) → IP

	conn *net.UDPConn
	mu   sync.RWMutex
	done chan struct{}
}

// New 创建 DNS Proxy。
func New(cfg *genv1.DNSConfig) *Proxy {
	records := make(map[string]string, len(cfg.Records))
	for _, r := range cfg.Records {
		records[strings.ToLower(r.Fqdn)] = r.TargetIp
	}
	addr := fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.ListenPort)
	return &Proxy{
		listenAddr: addr,
		upstreams:  cfg.Upstreams,
		records:    records,
		done:       make(chan struct{}),
	}
}

// Start 启动 DNS proxy 监听。
func (p *Proxy) Start(ctx context.Context) error {
	udpAddr, err := net.ResolveUDPAddr("udp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("resolve addr: %w", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}
	p.conn = conn

	go p.serve(ctx)
	return nil
}

func (p *Proxy) serve(ctx context.Context) {
	defer close(p.done)
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = p.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, raddr, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		go p.handlePacket(pkt, raddr)
	}
}

func (p *Proxy) handlePacket(pkt []byte, raddr *net.UDPAddr) {
	if len(pkt) < 12 {
		return
	}
	qname, qtype := extractQuestion(pkt)
	if qname == "" {
		return
	}

	p.mu.RLock()
	ip, found := p.records[strings.ToLower(qname)]
	p.mu.RUnlock()

	if found && (qtype == 1 || qtype == 28) { // A or AAAA
		resp := buildAResponse(pkt, qname, ip)
		if resp != nil {
			_, _ = p.conn.WriteToUDP(resp, raddr)
			return
		}
	}

	// 转发到上游
	if len(p.upstreams) == 0 {
		_, _ = p.conn.WriteToUDP(buildServfail(pkt), raddr)
		return
	}
	upstream := p.upstreams[0]
	resp, err := forwardUDP(upstream, pkt, 3*time.Second)
	if err != nil {
		slog.Debug("dns forward failed", "upstream", upstream, "err", err)
		_, _ = p.conn.WriteToUDP(buildServfail(pkt), raddr)
		return
	}
	_, _ = p.conn.WriteToUDP(resp, raddr)
}

// UpdateRecords 动态更新内部记录。
func (p *Proxy) UpdateRecords(records []*genv1.DNSRecord) {
	m := make(map[string]string, len(records))
	for _, r := range records {
		m[strings.ToLower(r.Fqdn)] = r.TargetIp
	}
	p.mu.Lock()
	p.records = m
	p.mu.Unlock()
}

// Close 关闭 proxy。
func (p *Proxy) Close() error {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	<-p.done
	return nil
}

// Addr 返回监听地址（在 Start 之后有效）。
func (p *Proxy) Addr() net.Addr {
	if p.conn != nil {
		return p.conn.LocalAddr()
	}
	return nil
}

func forwardUDP(upstream string, pkt []byte, timeout time.Duration) ([]byte, error) {
	if !strings.Contains(upstream, ":") {
		upstream = upstream + ":53"
	}
	// 使用 BindControl 绑定物理网卡，确保上游 DNS 查询不走 TUN /1 路由
	dialer := net.Dialer{
		Timeout: timeout,
		Control: tunnel.BindControl,
	}
	conn, err := dialer.Dial("udp", upstream)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(pkt); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func extractQuestion(pkt []byte) (string, uint16) {
	if len(pkt) < 12 {
		return "", 0
	}
	qdcount := binary.BigEndian.Uint16(pkt[4:6])
	if qdcount == 0 {
		return "", 0
	}
	offset := 12
	var labels []string
	for offset < len(pkt) {
		length := int(pkt[offset])
		if length == 0 {
			offset++
			break
		}
		if offset+1+length > len(pkt) {
			return "", 0
		}
		labels = append(labels, string(pkt[offset+1:offset+1+length]))
		offset += 1 + length
	}
	if offset+4 > len(pkt) {
		return "", 0
	}
	qtype := binary.BigEndian.Uint16(pkt[offset : offset+2])
	return strings.Join(labels, "."), qtype
}

func buildAResponse(query []byte, name, ip string) []byte {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil
	}
	ipv4 := parsed.To4()
	if ipv4 == nil {
		return nil // TODO: AAAA support
	}

	resp := make([]byte, len(query))
	copy(resp, query)
	// Flags: QR=1, AA=1, RCODE=0
	resp[2] = 0x84
	resp[3] = 0x00
	// ANCOUNT=1, NSCOUNT=0, ARCOUNT=0
	binary.BigEndian.PutUint16(resp[6:8], 1)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)

	// 截断到 header + question，去掉原查询中可能携带的 OPT/附加记录。
	qEnd := 12
	for qEnd < len(resp) {
		if resp[qEnd] == 0 {
			qEnd += 1 + 4 // null terminator + QTYPE(2) + QCLASS(2)
			break
		}
		qEnd += int(resp[qEnd]) + 1
	}
	if qEnd > len(resp) {
		qEnd = len(resp)
	}
	resp = resp[:qEnd]

	// Answer: pointer to question name + A record
	answer := []byte{
		0xc0, 0x0c, // pointer to offset 12 (question name)
		0x00, 0x01, // TYPE A
		0x00, 0x01, // CLASS IN
		0x00, 0x00, 0x00, 0x3c, // TTL 60s
		0x00, 0x04, // RDLENGTH 4
	}
	answer = append(answer, ipv4...)
	resp = append(resp, answer...)
	return resp
}

func buildServfail(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	resp := make([]byte, len(query))
	copy(resp, query)
	resp[2] = 0x80 // QR=1
	resp[3] = 0x02 // RCODE=SERVFAIL
	return resp
}
