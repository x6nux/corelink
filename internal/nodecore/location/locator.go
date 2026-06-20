// Package location 实现 node 出口综合定位：多源探测，中国 IP 优先 my.ip.cn。
//
// 定位策略：
//  1. CF /meta 提供客户端公网 IP 的 country + lat/lon + colo（Anycast 机房，仅展示）
//  2. 中国 IP（country=CN）→ my.ip.cn 解析中文归属地（省/市）→ 城市坐标库精确定位
//  3. 非中国 IP → CF client IP 的 lat/lon
//  4. Fastly x-served-by / CF colo 仅记录展示（Anycast 路由不代表节点物理位置）
//
// 注意：CF colo 是 Anycast 路由结果（如成都联通被路由到 LAX/SIN），不反映节点位置，
// 故不参与定位坐标决策。CF 对中国 IP 的 GeoIP 也有波动，故中国 IP 用 my.ip.cn 校正。
package location

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/x6nux/corelink/pkg/tunnel"
)

// browserUA 模拟浏览器 UA，绕过 CF/my.ip.cn 对非浏览器请求的拦截。
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Location 综合定位结果。
type Location struct {
	ColIATA   string  `json:"col_iata"` // CF/Fastly colo IATA（仅展示）
	Latitude  float64 `json:"latitude"` // 最终采用坐标
	Longitude float64 `json:"longitude"`
	City      string  `json:"city"`              // 城市名
	Country   string  `json:"country"`           // 国家码
	Accuracy  string  `json:"accuracy"`          // "high" | "low" | "manual" | "none"
	ColLat    float64 `json:"col_lat,omitempty"` // colo 坐标（展示）
	ColLon    float64 `json:"col_lon,omitempty"`
	ClientLat float64 `json:"client_lat,omitempty"` // CF client IP 坐标
	ClientLon float64 `json:"client_lon,omitempty"`
	Source    string  `json:"source"`              // "manual"|"ipcn"|"cf-client"|"none"
	CFRttMs   float64 `json:"cf_rtt_ms,omitempty"` // 到 CF 边缘 RTT（展示）
	IPCN      string  `json:"ipcn,omitempty"`      // my.ip.cn 原始归属地（展示）
}

// Locator 综合定位器。URL/HTTP 客户端可注入，便于测试。
type Locator struct {
	httpClient   *http.Client
	locationsURL string // CF /locations 全量机房映射（展示用）
	metaURL      string // CF /meta client IP + country + colo
	fastlyURL    string // Fastly HEAD（展示用 colo IATA）
	ipcnURL      string // my.ip.cn 中国 IP 精确归属地
	pingTarget   string // TCP ping 目标（仅测 CF RTT 展示）

	manual *Location // 人工修正坐标（非 nil 时覆盖自动定位）

	coloMu     sync.Mutex
	coloLoaded bool
	coloMap    map[string]cfColo
	coloErr    error
}

type cfColo struct {
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Cca2 string  `json:"cca2"`
	City string  `json:"city"`
}

// cfMetaRaw CF /meta 原始 JSON —— latitude/longitude 可能是数字也可能是字符串。
type cfMetaRaw struct {
	Latitude  json.Number `json:"latitude"`
	Longitude json.Number `json:"longitude"`
	Country   string      `json:"country"`
	City      string      `json:"city"`
	Colo      struct {
		IATA string      `json:"iata"`
		Lat  json.Number `json:"lat"`
		Lon  json.Number `json:"lon"`
	} `json:"colo"`
}

type cfMeta struct {
	Latitude  float64
	Longitude float64
	Country   string
	City      string
	Colo      struct {
		IATA string
		Lat  float64
		Lon  float64
	}
}

func parseCFMeta(raw cfMetaRaw) cfMeta {
	f := func(n json.Number) float64 { v, _ := n.Float64(); return v }
	m := cfMeta{
		Latitude:  f(raw.Latitude),
		Longitude: f(raw.Longitude),
		Country:   raw.Country,
		City:      raw.City,
	}
	m.Colo.IATA = raw.Colo.IATA
	m.Colo.Lat = f(raw.Colo.Lat)
	m.Colo.Lon = f(raw.Colo.Lon)
	return m
}

// New 创建默认 Locator。
// HTTP 请求通过 tunnel.BypassTransport 绕过 TUN 策略路由（SO_BINDTODEVICE + SO_MARK），
// 确保定位请求从物理网卡出去，拿到的是节点自身的公网 IP 而非 overlay 出口 IP。
func New() *Locator {
	return &Locator{
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: tunnel.BypassTransport(nil),
		},
		locationsURL: "https://speed.cloudflare.com/locations",
		metaURL:      "https://speed.cloudflare.com/meta",
		fastlyURL:    "https://any.pops.fastly-analytics.com/",
		ipcnURL:      "https://my.ip.cn/",
		pingTarget:   "speed.cloudflare.com:443",
	}
}

// SetManual 设置人工修正坐标（最高优先级）。传零值清除。
func (l *Locator) SetManual(loc Location) {
	if loc.Latitude == 0 && loc.Longitude == 0 {
		l.manual = nil
		return
	}
	loc.Source = "manual"
	loc.Accuracy = "manual"
	l.manual = &loc
}

// Locate 综合定位。人工修正 > 中国IP(my.ip.cn) > CF client IP。
func (l *Locator) Locate(ctx context.Context) (Location, error) {
	if l.manual != nil {
		slog.Info("location: 使用人工修正坐标", "lat", l.manual.Latitude, "lon", l.manual.Longitude)
		return *l.manual, nil
	}

	type srcResult struct {
		name string
		colo map[string]cfColo
		meta *cfMeta
		iata string
		ipcn string
		err  error
	}
	resCh := make(chan srcResult, 4)

	go func() { m, err := l.fetchLocations(ctx); resCh <- srcResult{name: "locations", colo: m, err: err} }()
	go func() {
		m, err := l.fetchMeta(ctx)
		var pm *cfMeta
		if m != nil {
			pm = m
		}
		resCh <- srcResult{name: "meta", meta: pm, err: err}
	}()
	go func() { s, err := l.fetchIPCN(ctx); resCh <- srcResult{name: "ipcn", ipcn: s, err: err} }()
	go func() { i, err := l.fetchFastly(ctx); resCh <- srcResult{name: "fastly", iata: i, err: err} }()

	var coloMap map[string]cfColo
	var meta *cfMeta
	var ipcnText, fastlyIATA string
	for range 4 {
		r := <-resCh
		if r.err != nil {
			slog.Info("location: 源探测失败", "src", r.name, "err", r.err)
			continue
		}
		switch r.name {
		case "locations":
			coloMap = r.colo
		case "meta":
			meta = r.meta
		case "ipcn":
			ipcnText = r.ipcn
		case "fastly":
			fastlyIATA = r.iata
		}
	}
	slog.Info("location: 源汇总", "meta", meta != nil, "ipcn", ipcnText != "", "fastly", fastlyIATA, "coloMap", len(coloMap))

	if meta == nil && ipcnText == "" {
		return Location{Accuracy: "none"}, fmt.Errorf("location: CF meta 与 my.ip.cn 均失败")
	}

	// CF RTT（仅展示）
	var rttMs float64
	if l.pingTarget != "" {
		if rtt, err := tcpPing(ctx, l.pingTarget, 3*time.Second); err == nil {
			rttMs = rtt
		}
	}

	return l.resolve(coloMap, meta, ipcnText, fastlyIATA, rttMs), nil
}

// resolve 融合多源。中国 IP 优先 my.ip.cn，否则 CF client IP。colo 仅展示。
func (l *Locator) resolve(coloMap map[string]cfColo, meta *cfMeta, ipcnText, fastlyIATA string, rttMs float64) Location {
	loc := Location{Accuracy: "none", CFRttMs: rttMs}

	// colo（仅展示）：Fastly 优先，次选 CF colo
	iata := fastlyIATA
	if iata == "" && meta != nil {
		iata = meta.Colo.IATA
	}
	if iata != "" {
		loc.ColIATA = iata
		if c, ok := coloMap[iata]; ok {
			loc.ColLat, loc.ColLon = c.Lat, c.Lon
		} else if meta != nil && meta.Colo.IATA == iata {
			loc.ColLat, loc.ColLon = meta.Colo.Lat, meta.Colo.Lon
		}
	}

	// CF client IP 坐标（展示 + 非中国时主定位）
	if meta != nil {
		loc.ClientLat = meta.Latitude
		loc.ClientLon = meta.Longitude
		if loc.City == "" {
			loc.City = meta.City
		}
		if loc.Country == "" {
			loc.Country = meta.Country
		}
	}
	loc.IPCN = ipcnText

	// 中国 IP → my.ip.cn 解析省/市 → 城市坐标库（最准）
	isCN := meta != nil && (meta.Country == "CN" || meta.Country == "China")
	if isCN && ipcnText != "" {
		_, city, _ := parseIPCN(ipcnText)
		if city != "" {
			if lat, lon, ok := lookupChinaCity(city); ok {
				loc.Latitude, loc.Longitude = lat, lon
				loc.City = city
				loc.Country = "CN"
				loc.Source = "ipcn"
				loc.Accuracy = "high"
				slog.Info("location: 中国 IP 用 my.ip.cn 定位", "city", city, "ipcn", ipcnText)
				return loc
			}
			slog.Info("location: my.ip.cn 城市未在坐标库，回退 CF", "city", city, "ipcn", ipcnText)
		}
	}

	// 非中国 或 my.ip.cn 失败 → CF client IP 定位
	if meta != nil && meta.Latitude != 0 {
		loc.Latitude = meta.Latitude
		loc.Longitude = meta.Longitude
		loc.Source = "cf-client"
		// 非中国 CF 定位置信 high；中国但 my.ip.cn 失败则 low
		if isCN {
			loc.Accuracy = "low"
		} else {
			loc.Accuracy = "high"
		}
		return loc
	}

	return loc
}

// parseIPCN 解析 my.ip.cn 归属地文本。
// 输入示例："ip：119.4.86.5 归属地：中国 四川 成都 郫都 联通"
// 返回 (省, 市, 原始)。城市用子串匹配坐标库（最健壮，不依赖文本分段格式）。
func parseIPCN(text string) (province, city, raw string) {
	raw = text
	// 城市匹配：在文本里搜坐标库城市名，取最长命中（避免短名误匹配）
	for name := range chinaCityCoords {
		if strings.Contains(text, name) && len(name) > len(city) {
			city = name
		}
	}
	// 省份：归属地后第1段
	re := regexp.MustCompile(`归属地[：:]\s*(.+)`)
	if m := re.FindStringSubmatch(text); len(m) >= 2 {
		if fields := strings.Fields(m[1]); len(fields) >= 2 {
			province = fields[1]
		}
	}
	return province, city, raw
}

// fetchLocations 拉取 CF /locations 全量机房映射（展示用），成功后缓存。
func (l *Locator) fetchLocations(ctx context.Context) (map[string]cfColo, error) {
	l.coloMu.Lock()
	defer l.coloMu.Unlock()
	if l.coloLoaded {
		return l.coloMap, l.coloErr
	}
	l.coloMap, l.coloErr = l.doFetchLocations(ctx)
	if l.coloErr == nil {
		l.coloLoaded = true
	}
	return l.coloMap, l.coloErr
}

func (l *Locator) doFetchLocations(ctx context.Context) (map[string]cfColo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.locationsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://speed.cloudflare.com/")
	req.Header.Set("Accept", "application/json")

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("locations 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("locations HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	type rawColo struct {
		IATA string  `json:"iata"`
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		Cca2 string  `json:"cca2"`
		City string  `json:"city"`
	}
	var arr []rawColo
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, fmt.Errorf("locations 解析失败: %w", err)
	}
	m := make(map[string]cfColo, len(arr))
	for _, c := range arr {
		if c.IATA != "" {
			m[c.IATA] = cfColo{Lat: c.Lat, Lon: c.Lon, Cca2: c.Cca2, City: c.City}
		}
	}
	slog.Info("location: CF 机房映射已加载", "colos", len(m))
	return m, nil
}

// fetchMeta 拉取 CF /meta（client IP + country + colo）。
func (l *Locator) fetchMeta(ctx context.Context) (*cfMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.metaURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://speed.cloudflare.com/")

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meta HTTP %d", resp.StatusCode)
	}
	var raw cfMetaRaw
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("meta 解析失败: %w", err)
	}
	m := parseCFMeta(raw)
	return &m, nil
}

// fetchIPCN 拉取 my.ip.cn 中国 IP 归属地文本。
func (l *Locator) fetchIPCN(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.ipcnURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUA)

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ip.cn 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ip.cn HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// fetchFastly HEAD Fastly 取 x-served-by（展示用 colo IATA）。
func (l *Locator) fetchFastly(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, l.fastlyURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := l.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fastly 请求失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	served := resp.Header.Get("x-served-by")
	if served == "" {
		return "", fmt.Errorf("fastly 无 x-served-by")
	}
	parts := strings.Split(served, "-")
	if len(parts) == 0 {
		return "", fmt.Errorf("fastly 无法解析: %q", served)
	}
	iata := strings.TrimSpace(parts[len(parts)-1])
	if len(iata) != 3 {
		return "", fmt.Errorf("fastly IATA 长度异常: %q", served)
	}
	return strings.ToUpper(iata), nil
}

// tcpPing 测到 target 的 TCP 握手 RTT（毫秒，仅展示）。
// 使用 tunnel.BindControl 绕过 TUN 策略路由，确保从物理网卡出去。
func tcpPing(ctx context.Context, target string, timeout time.Duration) (float64, error) {
	d := net.Dialer{Timeout: timeout, Control: tunnel.BindControl}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return 0, err
	}
	rtt := float64(time.Since(start).Microseconds()) / 1000.0
	conn.Close()
	return rtt, nil
}

// haversine 球面距离（公里），保留供测试/展示用。
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const earthKm = 6371.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthKm * c
}
