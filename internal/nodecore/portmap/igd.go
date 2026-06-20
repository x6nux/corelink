// 本文件实现 UPnP-IGD 端口映射（标准库，无第三方 UPnP 库）。
//
// UPnP IGD 不像 NAT-PMP/PCP 用定长二进制 UDP，而是基于 HTTP/SOAP：
//   - SSDP 发现：向组播地址 239.255.255.250:1900 发 M-SEARCH，收集网关返回的
//     device 描述 XML 的 LOCATION URL。
//   - device XML：HTTP GET LOCATION，解析出 WANIPConnection / WANPPPConnection
//     service 的 controlURL。
//   - SOAP 操作：AddPortMapping / GetExternalIPAddress / DeletePortMapping，
//     构成 igdMap/igdRefresh/igdUnmap 三件套。
//
// 收发与解析对超时/坏报文/坏 XML 做边界防御，返回 error 而非 panic（与
// natpmp.go/pcp.go 同风格）。SSDP 的 UDP 收发抽象成可注入接口，测试不依赖真实组播。
package portmap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/x6nux/corelink/pkg/tunnel"
)

// SSDP/UPnP 协议常量。
const (
	// ssdpMulticastAddr 为 SSDP 组播地址与端口（RFC / UPnP Device Architecture）。
	ssdpMulticastAddr = "239.255.255.250:1900"

	// igdDeviceST 为 M-SEARCH 的 ST（search target）：IGD v1 根设备。
	igdDeviceST = "urn:schemas-upnp-org:device:InternetGatewayDevice:1"

	// WAN 连接服务类型：优先 IP，回退 PPP。
	serviceTypeWANIPConnection  = "urn:schemas-upnp-org:service:WANIPConnection:1"
	serviceTypeWANPPPConnection = "urn:schemas-upnp-org:service:WANPPPConnection:1"
)

// buildMSearch 构造 SSDP M-SEARCH 请求报文。注意：MAN 值带双引号；每行 CRLF 结尾；
// 报文以空行（额外 CRLF）结束。mx 为 MX 头（最大响应延迟秒数）。
func buildMSearch(mx int) []byte {
	var b strings.Builder
	b.WriteString("M-SEARCH * HTTP/1.1\r\n")
	b.WriteString("HOST: " + ssdpMulticastAddr + "\r\n")
	b.WriteString("MAN: \"ssdp:discover\"\r\n")
	b.WriteString("ST: " + igdDeviceST + "\r\n")
	fmt.Fprintf(&b, "MX: %d\r\n", mx)
	b.WriteString("\r\n")
	return []byte(b.String())
}

// parseSSDPLocation 从一个 SSDP 响应报文中提取 LOCATION header 值。
//
// SSDP 响应是 HTTP 响应格式（HTTP/1.1 200 OK + headers）。header 名大小写不敏感，
// 顺序任意，值可能有多余首尾空白。无 LOCATION（或报文不可解析）返回 ok=false，
// 不 panic。本函数是纯函数，便于单测。
func parseSSDPLocation(resp []byte) (location string, ok bool) {
	r := bufio.NewReader(bytes.NewReader(resp))
	// 跳过状态行（HTTP/1.1 200 OK）。允许缺失/异常：后续逐行扫描仍能找到 header。
	if _, err := r.ReadString('\n'); err != nil && err != io.EOF {
		return "", false
	}
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			// 空行：header 区结束。
			if strings.TrimSpace(line) == "" {
				break
			}
			name, value, found := strings.Cut(line, ":")
			if found && strings.EqualFold(strings.TrimSpace(name), "LOCATION") {
				v := strings.TrimSpace(value)
				if v != "" {
					return v, true
				}
			}
		}
		if err != nil {
			break
		}
	}
	return "", false
}

// ssdpTransport 抽象 SSDP 的 UDP 收发，便于测试注入：发出 m-search 报文，在 timeout
// 内收集所有响应报文（每个响应一个 []byte）。默认实现 udpSSDPTransport 用真实组播；
// 测试注入 fake 返回预设字节，不依赖真实网络。
type ssdpTransport interface {
	// SendRecv 发送 msearch 报文，并在 timeout 内（或 ctx 取消前）收集所有响应。
	// 返回已收集的响应（可能为空）；底层错误返回 err。ctx 取消/超时不算错误，返回
	// 已收集到的部分。
	SendRecv(ctx context.Context, msearch []byte, timeout time.Duration) ([][]byte, error)
}

// udpSSDPTransport 是 ssdpTransport 的默认实现，用真实 UDP 组播收发。
type udpSSDPTransport struct{}

// SendRecv 向组播地址发 M-SEARCH，并在 timeout 内读取多个响应。ctx 取消会立即解除
// 阻塞读。socket 资源 defer 关闭，无泄漏。
func (udpSSDPTransport) SendRecv(ctx context.Context, msearch []byte, timeout time.Duration) ([][]byte, error) {
	raddr, err := net.ResolveUDPAddr("udp4", ssdpMulticastAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve ssdp multicast addr: %w", err)
	}
	// 绑定本地任意端口的 UDP socket，向组播地址发送；注入 SO_BINDTODEVICE 绕过 TUN 路由。
	lc := net.ListenConfig{Control: tunnel.BindControl}
	pc, err := lc.ListenPacket(ctx, "udp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("listen udp for ssdp: %w", err)
	}
	conn := pc.(*net.UDPConn)
	defer conn.Close()

	// ctx 取消时立即解除阻塞读（与 natpmp.go 同模式）。
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set ssdp deadline: %w", err)
	}

	if _, err := conn.WriteToUDP(msearch, raddr); err != nil {
		return nil, fmt.Errorf("send m-search: %w", err)
	}

	var responses [][]byte
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if n > 0 {
			responses = append(responses, append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			// 超时/ctx 取消/读错误：返回已收集到的部分，不算失败。
			break
		}
	}
	return responses, nil
}

// ssdpDiscover 通过 SSDP 组播发现 IGD：发 M-SEARCH，在 timeout 内收集响应，解析出每个
// 响应的 LOCATION（device XML URL），去重后返回。组播收发经可注入的 ssdpTransport
// 完成（默认真实组播）。ctx 取消/timeout 到期返回已收集到的（可能为空）+ 不 panic。
func ssdpDiscover(ctx context.Context, timeout time.Duration) ([]string, error) {
	return ssdpDiscoverWith(ctx, udpSSDPTransport{}, timeout)
}

// ssdpDiscoverWith 是 ssdpDiscover 的可注入实现，供测试传入 fake transport。
func ssdpDiscoverWith(ctx context.Context, tr ssdpTransport, timeout time.Duration) ([]string, error) {
	// MX 取 2 秒（与 timeout 同量级，按 UPnP 约定网关会在 [0,MX] 内随机延迟响应）。
	mx := max(int(timeout/time.Second), 1)
	responses, err := tr.SendRecv(ctx, buildMSearch(mx), timeout)
	if err != nil {
		return nil, err
	}

	var locations []string
	seen := make(map[string]struct{})
	for _, resp := range responses {
		loc, ok := parseSSDPLocation(resp)
		if !ok {
			continue // 无 LOCATION 的响应跳过。
		}
		if _, dup := seen[loc]; dup {
			continue // 去重。
		}
		seen[loc] = struct{}{}
		locations = append(locations, loc)
	}
	return locations, nil
}

// upnpService 对应 device XML 中 <service> 节点（只取本任务关心的字段）。
type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

// upnpDevice 对应 device XML 中 <device> 节点，可递归嵌套 deviceList。
type upnpDevice struct {
	DeviceType  string        `xml:"deviceType"`
	ServiceList []upnpService `xml:"serviceList>service"`
	DeviceList  []upnpDevice  `xml:"deviceList>device"`
}

// upnpRoot 对应 device XML 的 <root> 根节点。URLBase 可选，若存在则用于解析相对
// controlURL（优先于 locationURL）。
type upnpRoot struct {
	URLBase string     `xml:"URLBase"`
	Device  upnpDevice `xml:"device"`
}

// findWANService 在 device 树中递归查找目标 serviceType 的 service，返回其 controlURL。
// 优先 WANIPConnection，无则 WANPPPConnection。都无返回 ok=false。
func findWANService(d *upnpDevice, serviceType string) (controlURL string, ok bool) {
	for _, s := range d.ServiceList {
		if strings.EqualFold(s.ServiceType, serviceType) {
			return s.ControlURL, true
		}
	}
	for i := range d.DeviceList {
		if cu, found := findWANService(&d.DeviceList[i], serviceType); found {
			return cu, true
		}
	}
	return "", false
}

// fetchControlURL 拉取 locationURL 处的 device 描述 XML，解析出 WAN 连接 service 的
// 绝对 controlURL 与 serviceType。
//
// controlURL 在 XML 中通常是相对路径，按 URLBase（若存在，优先）或 locationURL 解析为
// 绝对 URL。优先 WANIPConnection，无则 WANPPPConnection；都无 → error。
// httpClient 为 nil 时用 http.DefaultClient。坏 XML / 无 service / HTTP 非 200 → error，
// 不 panic。
func fetchControlURL(ctx context.Context, locationURL string, httpClient *http.Client) (controlURL, serviceType string, err error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, locationURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build device-xml request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("get device xml %q: %w", locationURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("device xml %q: HTTP %d", locationURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 限制 1MiB，防御异常大响应。
	if err != nil {
		return "", "", fmt.Errorf("read device xml: %w", err)
	}

	var root upnpRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return "", "", fmt.Errorf("parse device xml: %w", err)
	}

	// 解析相对 controlURL 的 base：URLBase 优先，否则 locationURL。
	base, err := resolveBaseURL(root.URLBase, locationURL)
	if err != nil {
		return "", "", err
	}

	// 优先 WANIPConnection，回退 WANPPPConnection。
	for _, st := range []string{serviceTypeWANIPConnection, serviceTypeWANPPPConnection} {
		if rawCtl, ok := findWANService(&root.Device, st); ok {
			abs, err := resolveControlURL(base, rawCtl)
			if err != nil {
				return "", "", err
			}
			return abs, st, nil
		}
	}
	return "", "", fmt.Errorf("device xml %q: no WAN connection service found", locationURL)
}

// resolveBaseURL 计算解析相对 controlURL 的基准 URL：urlBase（XML 的 <URLBase>，可空）
// 非空且可解析则用之，否则用 locationURL。
func resolveBaseURL(urlBase, locationURL string) (*url.URL, error) {
	if s := strings.TrimSpace(urlBase); s != "" {
		if u, err := url.Parse(s); err == nil && u.IsAbs() {
			return u, nil
		}
	}
	u, err := url.Parse(locationURL)
	if err != nil {
		return nil, fmt.Errorf("parse location url %q: %w", locationURL, err)
	}
	return u, nil
}

// resolveControlURL 将（可能相对的）controlURL 相对 base 解析为绝对 URL 字符串。
func resolveControlURL(base *url.URL, rawCtl string) (string, error) {
	ref, err := url.Parse(strings.TrimSpace(rawCtl))
	if err != nil {
		return "", fmt.Errorf("parse control url %q: %w", rawCtl, err)
	}
	return base.ResolveReference(ref).String(), nil
}

// ============================ SOAP 操作 ============================
//
// UPnP-IGD 的端口映射通过 SOAP over HTTP 完成：
//   - AddPortMapping：在网关上建立一条端口映射。
//   - GetExternalIPAddress：查询网关 WAN 口外部 IP。
//   - DeletePortMapping：删除一条端口映射。
//
// Mapping.Gateway 编码 "controlURL\tserviceType"（tab 分隔），续期/删除时 Split 解出，
// 避免修改通用 Mapping 结构体。

// igdGatewaySep 为 Mapping.Gateway 中 controlURL 与 serviceType 的分隔符。
const igdGatewaySep = "\t"

// encodeIGDGateway 将 controlURL 和 serviceType 编码为 Mapping.Gateway 值。
func encodeIGDGateway(controlURL, serviceType string) string {
	return controlURL + igdGatewaySep + serviceType
}

// decodeIGDGateway 从 Mapping.Gateway 解出 controlURL 和 serviceType。
func decodeIGDGateway(gateway string) (controlURL, serviceType string, err error) {
	parts := strings.SplitN(gateway, igdGatewaySep, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("igd: malformed gateway %q", gateway)
	}
	return parts[0], parts[1], nil
}

// soapRequest 构造 SOAP 信封并向 controlURL POST，返回响应 body。
//
// action 为 SOAP 方法名（如 "AddPortMapping"）；innerXML 为 <u:Action> 的子元素 XML
// （不含外层信封）。统一处理 HTTP 错误与 SOAP fault。响应 body 限制 1MiB。
func soapRequest(ctx context.Context, httpClient *http.Client, controlURL, serviceType, action, innerXML string) ([]byte, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	envelope := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:` + action + ` xmlns:u="` + serviceType + `">` + innerXML + `</u:` + action + `>
  </s:Body>
</s:Envelope>`

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, strings.NewReader(envelope))
	if err != nil {
		return nil, fmt.Errorf("igd soap %s: build request: %w", action, err)
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+serviceType+"#"+action+`"`)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("igd soap %s: %w", action, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("igd soap %s: read response: %w", action, err)
	}

	// HTTP 500 + SOAP fault 是 UPnP 的标准错误返回方式。
	if resp.StatusCode >= 400 {
		desc := parseSoapFault(body)
		if desc != "" {
			return nil, fmt.Errorf("igd soap %s: %s", action, desc)
		}
		return nil, fmt.Errorf("igd soap %s: HTTP %d", action, resp.StatusCode)
	}

	return body, nil
}

// soapFaultEnvelope 用于解析 SOAP fault 响应。
type soapFaultEnvelope struct {
	XMLName xml.Name  `xml:"Envelope"`
	Body    soapFBody `xml:",any"`
}

type soapFBody struct {
	Fault soapFault `xml:",any"`
}

type soapFault struct {
	FaultString string      `xml:"faultstring"`
	Detail      soapFDetail `xml:"detail"`
}

type soapFDetail struct {
	UPnPError soapUPnPError `xml:",any"`
}

type soapUPnPError struct {
	ErrorDescription string `xml:"errorDescription"`
}

// parseSoapFault 尝试从 SOAP fault XML 中提取人类可读的错误描述。
// 解析失败（坏 XML 等）返回空字符串，不 panic。
func parseSoapFault(body []byte) string {
	var env soapFaultEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return ""
	}
	if d := env.Body.Fault.Detail.UPnPError.ErrorDescription; d != "" {
		return d
	}
	if s := env.Body.Fault.FaultString; s != "" {
		return s
	}
	return ""
}

// soapGetExternalIPEnvelope 用于解析 GetExternalIPAddress 的 SOAP 响应。
type soapGetExternalIPEnvelope struct {
	XMLName xml.Name              `xml:"Envelope"`
	Body    soapGetExternalIPBody `xml:",any"`
}

type soapGetExternalIPBody struct {
	Response soapGetExternalIPResponse `xml:",any"`
}

type soapGetExternalIPResponse struct {
	ExternalIP string `xml:"NewExternalIPAddress"`
}

// igdGetExternalIP 通过 SOAP GetExternalIPAddress 查询网关 WAN 口外部 IP。
func igdGetExternalIP(ctx context.Context, httpClient *http.Client, controlURL, serviceType string) (string, error) {
	body, err := soapRequest(ctx, httpClient, controlURL, serviceType, "GetExternalIPAddress", "")
	if err != nil {
		return "", err
	}

	var env soapGetExternalIPEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("igd GetExternalIPAddress: parse response: %w", err)
	}
	ip := strings.TrimSpace(env.Body.Response.ExternalIP)
	if ip == "" {
		return "", errors.New("igd GetExternalIPAddress: empty external IP in response")
	}
	return ip, nil
}

// isIGDConflictFault 判断 AddPortMapping 失败是否为「外部端口已被占用」类冲突。
// UPnP 标准 errorCode 718 = ConflictInMappingEntry，错误信息里会带该描述。
func isIGDConflictFault(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "ConflictInMappingEntry")
}

// nextCandidateExternalPort 在端口冲突后给出下一个候选外部端口。
// 在 49152-65535（IANA 动态/私有端口区间）内基于上一次端口做确定性偏移，
// 避开 0 与原内部端口的重复。attempt 从 1 开始。
func nextCandidateExternalPort(internalPort, prev uint16, attempt int) uint16 {
	const dynLow, dynHigh = 49152, 65535
	span := uint32(dynHigh - dynLow + 1)
	// 以内部端口为种子做线性步进，保证每次都得到区间内不同的端口。
	off := (uint32(internalPort) + uint32(attempt)*7919) % span
	cand := uint16(dynLow + off)
	if cand == prev || cand == internalPort || cand == 0 {
		cand = uint16(dynLow + (off+1)%span)
	}
	return cand
}

// igdMap 在 IGD 网关上建立一条端口映射：SOAP AddPortMapping + GetExternalIPAddress。
//
// 端口冲突（ConflictInMappingEntry/718）时换候选外部端口有限次重试，避免「第二轮
// 重复同端口」（bug #35）。本仓库仅实现 IGDv1（无 AddAnyPortMapping），故用换端口
// 重试策略实现「冲突后另选端口」。
//
// 已知行为：当 AddPortMapping 成功、但随后 GetExternalIPAddress 失败时，已在网关
// 建立的映射不会回滚，依赖其 TTL 过期自动回收（与 natpmpMap 同理）。
func igdMap(ctx context.Context, controlURL, serviceType string, internalPort, externalPort uint16, internalClient string, udp bool, lifetimeSec uint32, httpClient *http.Client) (*Mapping, error) {
	if externalPort == 0 {
		externalPort = internalPort // 0 = 同端口（IGDv1 无 AddAnyPortMapping，先按同端口尝试）
	}
	proto := "TCP"
	if udp {
		proto = "UDP"
	}

	// 端口冲突（ConflictInMappingEntry/718）时换候选端口重试，避免「第二轮重复同端口」。
	// 最多尝试 maxAttempts 次，保证有界、不无限循环。
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		innerXML := "<NewRemoteHost></NewRemoteHost>" +
			"<NewExternalPort>" + strconv.FormatUint(uint64(externalPort), 10) + "</NewExternalPort>" +
			"<NewProtocol>" + proto + "</NewProtocol>" +
			"<NewInternalPort>" + strconv.FormatUint(uint64(internalPort), 10) + "</NewInternalPort>" +
			"<NewInternalClient>" + internalClient + "</NewInternalClient>" +
			"<NewEnabled>1</NewEnabled>" +
			"<NewPortMappingDescription>CoreLink</NewPortMappingDescription>" +
			"<NewLeaseDuration>" + strconv.FormatUint(uint64(lifetimeSec), 10) + "</NewLeaseDuration>"

		_, err := soapRequest(ctx, httpClient, controlURL, serviceType, "AddPortMapping", innerXML)
		if err == nil {
			lastErr = nil // 本轮成功，清除上一轮冲突错误（否则误判为「所有候选都冲突」）。
			break
		}
		lastErr = err
		// 仅对端口冲突换端口重试；其它错误（鉴权/参数等）立即返回。
		if !isIGDConflictFault(err) {
			return nil, err
		}
		externalPort = nextCandidateExternalPort(internalPort, externalPort, attempt+1)
	}
	if lastErr != nil && isIGDConflictFault(lastErr) {
		// 所有候选端口都冲突：返回保留冲突描述的错误。
		return nil, lastErr
	}

	// 保留 #20c 的 validateGrantedTTL 守卫：UPnP 不回授予租约，请求 0（部分 IGDv1
	// 当永久）会让上层得到 TTL=0，复用统一校验拒绝（bug #20）。必须放在冲突重试成功
	// 跳出后、组装 Mapping 之前。
	if _, err := validateGrantedTTL(lifetimeSec, lifetimeSec); err != nil {
		return nil, err
	}

	extIP, err := igdGetExternalIP(ctx, httpClient, controlURL, serviceType)
	if err != nil {
		return nil, err
	}

	return &Mapping{
		Protocol:     ProtocolUPnP,
		ExternalIP:   extIP,
		ExternalPort: externalPort,
		InternalPort: internalPort,
		TransportUDP: udp,
		TTL:          time.Duration(lifetimeSec) * time.Second,
		Gateway:      encodeIGDGateway(controlURL, serviceType),
	}, nil
}

// igdRefresh 对已有 IGD 映射续期：UPnP 的续期就是覆盖写同端口同参数的
// AddPortMapping。controlURL 与 serviceType 从 Mapping.Gateway 解出。
func igdRefresh(ctx context.Context, m *Mapping, internalClient string, httpClient *http.Client) error {
	if m == nil {
		return errors.New("igd refresh: nil mapping")
	}
	controlURL, serviceType, err := decodeIGDGateway(m.Gateway)
	if err != nil {
		return err
	}

	proto := "TCP"
	if m.TransportUDP {
		proto = "UDP"
	}
	lifetimeSec := uint32(m.TTL / time.Second)

	innerXML := "<NewRemoteHost></NewRemoteHost>" +
		"<NewExternalPort>" + strconv.FormatUint(uint64(m.ExternalPort), 10) + "</NewExternalPort>" +
		"<NewProtocol>" + proto + "</NewProtocol>" +
		"<NewInternalPort>" + strconv.FormatUint(uint64(m.InternalPort), 10) + "</NewInternalPort>" +
		"<NewInternalClient>" + internalClient + "</NewInternalClient>" +
		"<NewEnabled>1</NewEnabled>" +
		"<NewPortMappingDescription>CoreLink</NewPortMappingDescription>" +
		"<NewLeaseDuration>" + strconv.FormatUint(uint64(lifetimeSec), 10) + "</NewLeaseDuration>"

	_, err = soapRequest(ctx, httpClient, controlURL, serviceType, "AddPortMapping", innerXML)
	return err
}

// igdUnmap 删除已有 IGD 映射：SOAP DeletePortMapping。controlURL 与 serviceType 从
// Mapping.Gateway 解出。
func igdUnmap(ctx context.Context, m *Mapping, httpClient *http.Client) error {
	if m == nil {
		return errors.New("igd unmap: nil mapping")
	}
	controlURL, serviceType, err := decodeIGDGateway(m.Gateway)
	if err != nil {
		return err
	}

	proto := "TCP"
	if m.TransportUDP {
		proto = "UDP"
	}

	innerXML := "<NewRemoteHost></NewRemoteHost>" +
		"<NewExternalPort>" + strconv.FormatUint(uint64(m.ExternalPort), 10) + "</NewExternalPort>" +
		"<NewProtocol>" + proto + "</NewProtocol>"

	_, err = soapRequest(ctx, httpClient, controlURL, serviceType, "DeletePortMapping", innerXML)
	return err
}
