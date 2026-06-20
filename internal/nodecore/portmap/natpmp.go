// 本文件实现一个轻量、无依赖的 NAT-PMP 客户端（RFC 6886）。
//
// NAT-PMP 使用定长大端二进制报文，经网关的 5351/UDP 端口收发：
//   - Map 请求（12B）/响应（16B）：在网关上建立/续期/删除一条端口映射。
//   - External IP 请求（2B）/响应（12B）：查询网关 WAN 口对外 IPv4。
//
// 仅使用标准库（net/encoding/binary/context/time）。报文解码对截断/字段非法
// 的输入做边界防御，返回 error 而非 panic（与 ingress/stun.go 同风格）。
package portmap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/x6nux/corelink/pkg/tunnel"
)

// NAT-PMP 协议常量（RFC 6886）。
const (
	natpmpPort = "5351" // 网关 NAT-PMP 服务端口。

	natpmpOpExternalIP byte = 0 // 查询外部 IP 的 opcode。
	natpmpOpMapUDP     byte = 1 // 映射 UDP 端口的 opcode。
	natpmpOpMapTCP     byte = 2 // 映射 TCP 端口的 opcode。

	natpmpRespOffset byte = 128 // 响应 opcode = 请求 opcode + 128。
)

// natpmpTimeout 为单次请求的短超时。RFC 6886 建议指数退避重试（初始 250ms，
// 最多 9 次），本实现取确定性的固定值并做少量重试，足以应对偶发丢包，同时不
// 拖累上层整体预算。ctx 取消/到期会立即中断阻塞读。
const (
	natpmpTimeout    = 2 * time.Second
	natpmpMaxRetries = 3
)

// encodeNATPMPMapRequest 编码 12 字节 Map 请求（大端）：
// version(1)=0 | opcode(1) | reserved(2)=0 | internalPort(2) | suggestedExt(2) | lifetime(4)。
func encodeNATPMPMapRequest(opcode byte, internalPort, suggestedExt uint16, lifetime uint32) []byte {
	b := make([]byte, 12)
	b[0] = 0 // version
	b[1] = opcode
	// b[2:4] reserved = 0
	binary.BigEndian.PutUint16(b[4:6], internalPort)
	binary.BigEndian.PutUint16(b[6:8], suggestedExt)
	binary.BigEndian.PutUint32(b[8:12], lifetime)
	return b
}

// decodeNATPMPMapResponse 解析 16 字节 Map 响应（大端），返回 resultCode 与端口/
// 租约字段。非零 resultCode 仍视为可解码（由调用方判断成功与否）；仅当长度不足、
// version≠0 或 opcode 非 Map 响应（1+128 / 2+128）时返回 error，不 panic。
func decodeNATPMPMapResponse(b []byte) (resultCode, internalPort, externalPort uint16, lifetime uint32, err error) {
	if len(b) < 16 {
		return 0, 0, 0, 0, fmt.Errorf("natpmp map response too short: %d bytes", len(b))
	}
	if b[0] != 0 {
		return 0, 0, 0, 0, fmt.Errorf("natpmp map response bad version: %d", b[0])
	}
	if b[1] != natpmpOpMapUDP+natpmpRespOffset && b[1] != natpmpOpMapTCP+natpmpRespOffset {
		return 0, 0, 0, 0, fmt.Errorf("natpmp map response bad opcode: %d", b[1])
	}
	resultCode = binary.BigEndian.Uint16(b[2:4])
	// b[4:8] epoch（忽略）。
	internalPort = binary.BigEndian.Uint16(b[8:10])
	externalPort = binary.BigEndian.Uint16(b[10:12])
	lifetime = binary.BigEndian.Uint32(b[12:16])
	return resultCode, internalPort, externalPort, lifetime, nil
}

// encodeNATPMPExternalIPRequest 编码 2 字节外部 IP 请求：version(1)=0 | opcode(1)=0。
func encodeNATPMPExternalIPRequest() []byte {
	return []byte{0, natpmpOpExternalIP}
}

// decodeNATPMPExternalIPResponse 解析 12 字节外部 IP 响应（大端），返回 resultCode
// 与外部 IPv4 字符串。长度不足、version≠0 或 opcode≠128 → error，不 panic。
func decodeNATPMPExternalIPResponse(b []byte) (resultCode uint16, ip string, err error) {
	if len(b) < 12 {
		return 0, "", fmt.Errorf("natpmp external-ip response too short: %d bytes", len(b))
	}
	if b[0] != 0 {
		return 0, "", fmt.Errorf("natpmp external-ip response bad version: %d", b[0])
	}
	if b[1] != natpmpOpExternalIP+natpmpRespOffset {
		return 0, "", fmt.Errorf("natpmp external-ip response bad opcode: %d", b[1])
	}
	resultCode = binary.BigEndian.Uint16(b[2:4])
	// b[4:8] epoch（忽略）。
	ip = net.IPv4(b[8], b[9], b[10], b[11]).String()
	return resultCode, ip, nil
}

// natpmpGatewayAddr 规范化网关地址：若 gateway 不含端口则补 :5351；含端口（测试
// 注入 mock server 时）则原样返回。
func natpmpGatewayAddr(gateway string) string {
	if _, _, err := net.SplitHostPort(gateway); err == nil {
		return gateway
	}
	return net.JoinHostPort(gateway, natpmpPort)
}

// natpmpExchange 向网关发送一个请求并读取一个响应（大端定长报文）。每次尝试使用
// 短超时；ctx 取消会立即中断阻塞读。失败做少量重试（应对偶发 UDP 丢包），全部失败
// 返回最后一次 error。respLen 为期望响应长度（用于一次性分配缓冲）。
func natpmpExchange(ctx context.Context, gateway string, req []byte, respLen int) ([]byte, error) {
	raddr, err := net.ResolveUDPAddr("udp", natpmpGatewayAddr(gateway))
	if err != nil {
		return nil, fmt.Errorf("resolve gateway %q: %w", gateway, err)
	}

	// 注入 SO_BINDTODEVICE 绑定物理网卡，绕过 TUN 路由。
	d := net.Dialer{Control: tunnel.BindControl}
	c, err := d.DialContext(ctx, "udp", raddr.String())
	if err != nil {
		return nil, fmt.Errorf("dial gateway %q: %w", gateway, err)
	}
	conn := c.(*net.UDPConn)
	defer conn.Close()

	// ctx 取消时立即解除阻塞读（与 ingress/stun.go 同模式）。
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	var lastErr error
	for range natpmpMaxRetries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// 每次尝试一个短超时，并以 ctx 截止时间为上界（若更早）。
		deadline := time.Now().Add(natpmpTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("set deadline: %w", err)
		}

		if _, err := conn.Write(req); err != nil {
			lastErr = fmt.Errorf("write request: %w", err)
			continue
		}

		buf := make([]byte, respLen)
		n, err := conn.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("natpmp %q: %w", gateway, ctx.Err())
			}
			lastErr = fmt.Errorf("read response: %w", err)
			continue // 超时/丢包：重试。
		}
		return buf[:n], nil
	}
	if lastErr == nil {
		lastErr = errors.New("natpmp exchange failed")
	}
	return nil, lastErr
}

// natpmpQueryExternalIP 查询网关 WAN 口对外 IPv4。
func natpmpQueryExternalIP(ctx context.Context, gateway string) (string, error) {
	resp, err := natpmpExchange(ctx, gateway, encodeNATPMPExternalIPRequest(), 12)
	if err != nil {
		return "", err
	}
	rc, ip, err := decodeNATPMPExternalIPResponse(resp)
	if err != nil {
		return "", err
	}
	if rc != 0 {
		return "", fmt.Errorf("natpmp external-ip result code %d", rc)
	}
	return ip, nil
}

// natpmpSendMap 发送一条 Map 请求并解析响应，返回响应中的端口/租约字段。
// 供 Map/Refresh/Unmap 复用。
func natpmpSendMap(ctx context.Context, gateway string, opcode byte, internalPort, suggestedExt uint16, lifetime uint32) (externalPort uint16, grantedLifetime uint32, err error) {
	req := encodeNATPMPMapRequest(opcode, internalPort, suggestedExt, lifetime)
	resp, err := natpmpExchange(ctx, gateway, req, 16)
	if err != nil {
		return 0, 0, err
	}
	rc, _, extPort, life, err := decodeNATPMPMapResponse(resp)
	if err != nil {
		return 0, 0, err
	}
	if rc != 0 {
		return 0, 0, fmt.Errorf("natpmp map result code %d", rc)
	}
	return extPort, life, nil
}

// natpmpOpcodeForUDP 返回 Map 请求的 opcode：UDP→1，TCP→2。
func natpmpOpcodeForUDP(udp bool) byte {
	if udp {
		return natpmpOpMapUDP
	}
	return natpmpOpMapTCP
}

// natpmpMap 在网关上建立一条端口映射（RFC 6886）：先发 Map 请求拿到外部端口与
// 实际授予的租约，再查询外部 IP，组装为 *Mapping。任一步骤的 resultCode≠0、超时
// 或坏响应都返回 error。
//
// 已知行为：当 Map 请求已成功、但随后的 ExternalIP 查询失败时，已在网关建立的
// Map 映射不会回滚，依赖其 TTL 过期自动回收，由上层聚合层（U3.1）处理。
func natpmpMap(ctx context.Context, gateway string, internalPort, suggestedExtPort uint16, udp bool, lifetimeSec uint32) (*Mapping, error) {
	extPort, grantedLife, err := natpmpSendMap(ctx, gateway, natpmpOpcodeForUDP(udp), internalPort, suggestedExtPort, lifetimeSec)
	if err != nil {
		return nil, err
	}

	// 校验授予租约：请求非 0 却被授予 0 属异常，拒绝组装 TTL=0 的 Mapping（bug #20）。
	grantedLife, err = validateGrantedTTL(lifetimeSec, grantedLife)
	if err != nil {
		return nil, err
	}

	extIP, err := natpmpQueryExternalIP(ctx, gateway)
	if err != nil {
		return nil, err
	}

	return &Mapping{
		Protocol:     ProtocolNATPMP,
		ExternalIP:   extIP,
		ExternalPort: extPort,
		InternalPort: internalPort,
		TransportUDP: udp,
		TTL:          time.Duration(grantedLife) * time.Second,
		Gateway:      gateway,
	}, nil
}

// natpmpRefresh 通过重发 Map 请求对映射 m 续期（保活）：internalPort 用 m.InternalPort，
// suggestedExt 用 m.ExternalPort（对齐 pcpRefresh），保证续期后外部端口不变；lifetime
// 用当前 TTL。
func natpmpRefresh(ctx context.Context, gateway string, m *Mapping) error {
	if m == nil {
		return errors.New("natpmp refresh: nil mapping")
	}
	lifetimeSec := uint32(m.TTL / time.Second)
	_, _, err := natpmpSendMap(ctx, gateway, natpmpOpcodeForUDP(m.TransportUDP), m.InternalPort, m.ExternalPort, lifetimeSec)
	return err
}

// natpmpUnmap 删除映射 m：RFC 6886 规定发 lifetime=0 且 suggestedExternalPort=0 的
// Map 请求即删除该 internalPort 的映射。
func natpmpUnmap(ctx context.Context, gateway string, m *Mapping) error {
	if m == nil {
		return errors.New("natpmp unmap: nil mapping")
	}
	_, _, err := natpmpSendMap(ctx, gateway, natpmpOpcodeForUDP(m.TransportUDP), m.InternalPort, 0, 0)
	return err
}

// ============================ PCP（RFC 6887）============================
//
// PCP 是 NAT-PMP 的后继，与 NAT-PMP 共用网关的 5351/UDP 端口（按报文首字节的
// version 区分：NAT-PMP=0，PCP=2），因此复用本文件的纯收发层 natpmpExchange
// 与地址规范化 natpmpGatewayAddr，不重复造收发/重试逻辑。
//
// 与 NAT-PMP 不同，PCP 的 MAP opcode 一次往返即同时返回 assigned external IP 与
// port，无需额外发查询 IP 的报文。报文为定长 60 字节：公共头(24B) + MAP 数据(36B)。

// PCP 协议常量（RFC 6887）。
const (
	pcpVersion  byte = 2    // PCP 版本，置于报文首字节。
	pcpOpMap    byte = 1    // MAP opcode（请求 R bit=0，首字节=0x01）。
	pcpRespBit  byte = 0x80 // 响应在 opcode 字节置 R bit（响应首字节=0x81）。
	pcpProtoTCP byte = 6    // MAP 数据中的 IANA 协议号：TCP。
	pcpProtoUDP byte = 17   // MAP 数据中的 IANA 协议号：UDP。
	pcpNonceLen      = 12   // mapping nonce 长度。
	pcpReqLen        = 60   // 请求报文总长：24 公共头 + 36 MAP 数据。
	pcpRespLen       = 60   // 响应报文总长：24 公共头 + 36 MAP 数据。
)

// pcpNonce 为 MAP 请求/响应回显的 12 字节随机数，用于将响应与请求配对。
type pcpNonce [pcpNonceLen]byte

// pcpProtoForUDP 返回 MAP 数据中的 IANA 协议号：UDP→17，TCP→6。
func pcpProtoForUDP(udp bool) byte {
	if udp {
		return pcpProtoUDP
	}
	return pcpProtoTCP
}

// ipToMapped16 将 IPv4 转为 16 字节的 IPv4-mapped-IPv6（::ffff:a.b.c.d）。
// 非 IPv4（含 nil）返回全 0 的 16 字节（PCP 允许 suggested external IP 为 0）。
func ipToMapped16(ip net.IP) [16]byte {
	var out [16]byte
	if v4 := ip.To4(); v4 != nil {
		out[10], out[11] = 0xff, 0xff
		copy(out[12:16], v4)
	}
	return out
}

// mapped16ToIPv4String 从 16 字节地址提取 IPv4 字符串。若是 IPv4-mapped-IPv6 或
// 本就是 IPv4，返回点分十进制；否则（真 IPv6/全 0）返回 net.IP 的默认字符串。
func mapped16ToIPv4String(b []byte) string {
	ip := net.IP(append([]byte(nil), b...))
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// pcpLocalClientIP 返回连接网关时本地出口 IP。DialUDP 不发包，仅触发路由选择，
// 从 LocalAddr 取出本机用于到达该网关的源 IP，填入请求的 client IP 字段。
func pcpLocalClientIP(gateway string) (net.IP, error) {
	raddr, err := net.ResolveUDPAddr("udp", natpmpGatewayAddr(gateway))
	if err != nil {
		return nil, fmt.Errorf("resolve gateway %q: %w", gateway, err)
	}
	// 注入 SO_BINDTODEVICE 绑定物理网卡，绕过 TUN 路由。
	d := net.Dialer{Control: tunnel.BindControl}
	conn, err := d.DialContext(context.Background(), "udp", raddr.String())
	if err != nil {
		return nil, fmt.Errorf("dial gateway %q: %w", gateway, err)
	}
	defer conn.Close()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil {
		return nil, errors.New("pcp: cannot determine local client IP")
	}
	return la.IP, nil
}

// encodePCPMapRequest 编码 60 字节 PCP MAP 请求（大端）：
//
//	公共头(24B)：version(1)=2 | opcode(1)=1 | reserved(2)=0 | lifetime(4) | clientIP(16)
//	MAP 数据(36B)：nonce(12) | protocol(1) | reserved(3)=0 | internalPort(2) |
//	               suggestedExtPort(2) | suggestedExtIP(16)
//
// clientIP/suggestedExtIP 均为 16 字节 IPv4-mapped-IPv6（或全 0）。
func encodePCPMapRequest(nonce pcpNonce, clientIP net.IP, proto byte, internalPort, suggestedExtPort uint16, suggestedExtIP net.IP, lifetime uint32) []byte {
	b := make([]byte, pcpReqLen)
	// 公共头。
	b[0] = pcpVersion
	b[1] = pcpOpMap // R bit=0（请求）。
	// b[2:4] reserved = 0
	binary.BigEndian.PutUint32(b[4:8], lifetime)
	cip := ipToMapped16(clientIP)
	copy(b[8:24], cip[:])
	// MAP 数据。
	copy(b[24:36], nonce[:])
	b[36] = proto
	// b[37:40] reserved = 0
	binary.BigEndian.PutUint16(b[40:42], internalPort)
	binary.BigEndian.PutUint16(b[42:44], suggestedExtPort)
	eip := ipToMapped16(suggestedExtIP)
	copy(b[44:60], eip[:])
	return b
}

// decodePCPMapResponse 解析 60 字节 PCP MAP 响应（大端），返回 resultCode、回显
// nonce、协议、端口与 assigned external IP 字符串。非零 resultCode 仍视为可解码
// （由调用方判断成功）；长度不足、version≠2 或 opcode≠0x81（MAP 响应）→ error，
// 不 panic（与 NAT-PMP 边界防御同风格）。
func decodePCPMapResponse(b []byte) (resultCode byte, nonce pcpNonce, proto byte, internalPort, externalPort uint16, externalIP string, lifetime uint32, err error) {
	if len(b) < pcpRespLen {
		return 0, nonce, 0, 0, 0, "", 0, fmt.Errorf("pcp map response too short: %d bytes", len(b))
	}
	if b[0] != pcpVersion {
		return 0, nonce, 0, 0, 0, "", 0, fmt.Errorf("pcp map response bad version: %d", b[0])
	}
	if b[1] != pcpOpMap|pcpRespBit {
		return 0, nonce, 0, 0, 0, "", 0, fmt.Errorf("pcp map response bad opcode: 0x%02x", b[1])
	}
	// 公共头：b[2] reserved | b[3] result code | b[4:8] lifetime | b[8:12] epoch | b[12:24] reserved。
	resultCode = b[3]
	lifetime = binary.BigEndian.Uint32(b[4:8])
	// MAP 数据。
	copy(nonce[:], b[24:36])
	proto = b[36]
	internalPort = binary.BigEndian.Uint16(b[40:42])
	externalPort = binary.BigEndian.Uint16(b[42:44])
	externalIP = mapped16ToIPv4String(b[44:60])
	return resultCode, nonce, proto, internalPort, externalPort, externalIP, lifetime, nil
}

// pcpSendMap 生成 nonce、构造 MAP 请求、经共用收发层往返，并校验响应：version/
// opcode（由 decode 负责）、nonce 必须回显一致（否则丢弃为坏响应）、resultCode 须
// 为 0。供 pcpMap/pcpRefresh/pcpUnmap 复用。
func pcpSendMap(ctx context.Context, gateway string, udp bool, internalPort, suggestedExtPort uint16, lifetime uint32) (externalPort uint16, externalIP string, grantedLifetime uint32, err error) {
	clientIP, err := pcpLocalClientIP(gateway)
	if err != nil {
		return 0, "", 0, err
	}

	var nonce pcpNonce
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, "", 0, fmt.Errorf("pcp: generate nonce: %w", err)
	}

	req := encodePCPMapRequest(nonce, clientIP, pcpProtoForUDP(udp), internalPort, suggestedExtPort, nil, lifetime)
	resp, err := natpmpExchange(ctx, gateway, req, pcpRespLen)
	if err != nil {
		return 0, "", 0, err
	}

	rc, gotNonce, _, _, extPort, extIP, life, err := decodePCPMapResponse(resp)
	if err != nil {
		return 0, "", 0, err
	}
	if gotNonce != nonce {
		return 0, "", 0, errors.New("pcp map response nonce mismatch")
	}
	if rc != 0 {
		return 0, "", 0, fmt.Errorf("pcp map result code %d", rc)
	}
	return extPort, extIP, life, nil
}

// pcpMap 在网关上建立一条端口映射（RFC 6887 MAP opcode）：一次往返即同时拿到
// assigned external IP+port 与授予租约，组装为 *Mapping。nonce 不回显 / resultCode≠0
// / 报文截断/超时都返回 error。
func pcpMap(ctx context.Context, gateway string, internalPort, suggestedExtPort uint16, udp bool, lifetimeSec uint32) (*Mapping, error) {
	extPort, extIP, grantedLife, err := pcpSendMap(ctx, gateway, udp, internalPort, suggestedExtPort, lifetimeSec)
	if err != nil {
		return nil, err
	}
	// 校验授予租约：请求非 0 却被授予 0 属异常，拒绝组装 TTL=0 的 Mapping（bug #20）。
	grantedLife, err = validateGrantedTTL(lifetimeSec, grantedLife)
	if err != nil {
		return nil, err
	}
	return &Mapping{
		Protocol:     ProtocolPCP,
		ExternalIP:   extIP,
		ExternalPort: extPort,
		InternalPort: internalPort,
		TransportUDP: udp,
		TTL:          time.Duration(grantedLife) * time.Second,
		Gateway:      gateway,
	}, nil
}

// pcpRefresh 对映射 m 续期：RFC 6887 的续期就是重发同 internalPort、当前 TTL 的
// MAP 请求（nonce 每次新生成即可，PCP 用 nonce 配对单次往返，不要求跨请求保持）。
func pcpRefresh(ctx context.Context, m *Mapping) error {
	if m == nil {
		return errors.New("pcp refresh: nil mapping")
	}
	lifetimeSec := uint32(m.TTL / time.Second)
	_, _, _, err := pcpSendMap(ctx, m.Gateway, m.TransportUDP, m.InternalPort, m.ExternalPort, lifetimeSec)
	return err
}

// pcpUnmap 删除映射 m：RFC 6887 规定 lifetime=0 的 MAP 请求即删除该映射。
func pcpUnmap(ctx context.Context, m *Mapping) error {
	if m == nil {
		return errors.New("pcp unmap: nil mapping")
	}
	_, _, _, err := pcpSendMap(ctx, m.Gateway, m.TransportUDP, m.InternalPort, 0, 0)
	return err
}
