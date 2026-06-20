package location

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 成都坐标 30.5728, 104.0668（chinaCityCoords）
// HKG 22.308901, 113.915001

const locationsResp = `[{"iata":"HKG","lat":22.308901,"lon":113.915001,"cca2":"HK","city":"Hong Kong"},{"iata":"LAX","lat":33.942501,"lon":-118.407997,"cca2":"US","city":"Los Angeles"}]`

// mockCDN 构造 mock CF/ip.cn/Fastly server 的 Locator，关闭 ping。
func mockCDN(t *testing.T, locFn, metaFn, ipcnFn func(http.ResponseWriter, *http.Request)) (*Locator, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/locations", locFn)
	mux.HandleFunc("/meta", metaFn)
	mux.HandleFunc("/ipcn", ipcnFn)
	srv := httptest.NewServer(mux)
	loc := &Locator{
		httpClient:   srv.Client(),
		locationsURL: srv.URL + "/locations",
		metaURL:      srv.URL + "/meta",
		ipcnURL:      srv.URL + "/ipcn",
	}
	loc.pingTarget = ""
	return loc, srv.Close
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }

// TestLocate_ManualOverride 人工修正优先
func TestLocate_ManualOverride(t *testing.T) {
	loc, closeFn := mockCDN(t, okHandler, okHandler, okHandler)
	defer closeFn()
	loc.SetManual(Location{Latitude: 39.9, Longitude: 116.4, City: "Beijing"})

	got, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got.Source != "manual" || got.Accuracy != "manual" || got.Latitude != 39.9 {
		t.Errorf("人工修正失败: %+v", got)
	}
}

// TestLocate_ChinaIP_IPCN 中国 IP 用 my.ip.cn 定位
func TestLocate_ChinaIP_IPCN(t *testing.T) {
	loc, closeFn := mockCDN(t,
		func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(locationsResp)) },
		func(w http.ResponseWriter, _ *http.Request) {
			// CF meta: 中国 IP（联通成都），但 colo=LAX（Anycast 路由）
			w.Write([]byte(`{"latitude":30.67,"longitude":104.07,"country":"CN","city":"Chengdu","colo":{"iata":"LAX","lat":33.94,"lon":-118.41}}`))
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("ip：119.4.86.5 归属地：中国 四川 成都 郫都 联通"))
		},
	)
	defer closeFn()

	got, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got.Source != "ipcn" {
		t.Errorf("中国 IP 应用 ipcn: source=%s", got.Source)
	}
	if got.City != "成都" {
		t.Errorf("city=%s, want 成都", got.City)
	}
	// 坐标应为成都（chinaCityCoords），不是 LAX/CF-client
	if got.Latitude < 30 || got.Latitude > 31 {
		t.Errorf("lat=%v, want ~30.57(成都)", got.Latitude)
	}
	// colo 仅展示，记录 LAX
	if got.ColIATA != "LAX" {
		t.Errorf("col_iata=%s, want LAX(展示)", got.ColIATA)
	}
}

// TestLocate_NonChinaIP_CFClient 非中国 IP 用 CF client 定位
func TestLocate_NonChinaIP_CFClient(t *testing.T) {
	loc, closeFn := mockCDN(t,
		func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(locationsResp)) },
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"latitude":22.27,"longitude":114.17,"country":"HK","city":"Hong Kong","colo":{"iata":"HKG","lat":22.31,"lon":113.92}}`))
		},
		func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ip：1.2.3.4 归属地：香港")) },
	)
	defer closeFn()

	got, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got.Source != "cf-client" || got.Accuracy != "high" {
		t.Errorf("非中国应用 cf-client high: source=%s acc=%s", got.Source, got.Accuracy)
	}
	if got.City != "Hong Kong" {
		t.Errorf("city=%s", got.City)
	}
}

// TestLocate_ChinaIP_IPCNFail_FallbackCF 中国 IP 但 my.ip.cn 失败 → CF client（low）
func TestLocate_ChinaIP_IPCNFail(t *testing.T) {
	loc, closeFn := mockCDN(t,
		func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(locationsResp)) },
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"latitude":30.67,"longitude":104.07,"country":"CN","city":"Chengdu","colo":{"iata":"LAX"}}`))
		},
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, // ip.cn 失败
	)
	defer closeFn()

	got, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got.Source != "cf-client" || got.Accuracy != "low" {
		t.Errorf("ipcn 失败应回退 cf-client low: source=%s acc=%s", got.Source, got.Accuracy)
	}
}

// TestLocate_ChinaIP_CityNotInLib my.ip.cn 城市不在坐标库 → 回退 CF
func TestLocate_ChinaIP_CityNotInLib(t *testing.T) {
	loc, closeFn := mockCDN(t,
		func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(locationsResp)) },
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"latitude":30.67,"longitude":104.07,"country":"CN","city":"X","colo":{"iata":"LAX"}}`))
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("ip：1.2.3.4 归属地：中国 某省 小县城 联通")) // 小县城不在库
		},
	)
	defer closeFn()

	got, _ := loc.Locate(context.Background())
	if got.Source != "cf-client" {
		t.Errorf("城市不在库应回退 CF: source=%s", got.Source)
	}
}

// TestLocate_AllFail CF 与 ip.cn 都失败
func TestLocate_AllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
	}))
	defer srv.Close()
	loc := &Locator{
		httpClient: srv.Client(), locationsURL: srv.URL, metaURL: srv.URL, ipcnURL: srv.URL,
	}
	loc.pingTarget = ""
	_, err := loc.Locate(context.Background())
	if err == nil {
		t.Fatal("全失败应返回 error")
	}
}

// TestParseIPCN my.ip.cn 归属地解析
func TestParseIPCN(t *testing.T) {
	tests := []struct {
		input string
		city  string
	}{
		{"ip：119.4.86.5 归属地：中国 四川 成都 郫都 联通", "成都"},
		{"ip：1.2.3.4 归属地:中国 广东 深圳 南山 电信", "深圳"},
		{"ip：1.2.3.4 归属地：中国 北京 北京 海淀 联通", "北京"},
	}
	for _, tt := range tests {
		_, city, _ := parseIPCN(tt.input)
		if city != tt.city {
			t.Errorf("parseIPCN(%q) city=%s, want %s", tt.input, city, tt.city)
		}
	}
}

// TestParseIPCN_Malformed 异常格式不 panic
func TestParseIPCN_Malformed(t *testing.T) {
	_, city, _ := parseIPCN("garbage text")
	if city != "" {
		t.Errorf("异常格式 city 应空: %s", city)
	}
}

// TestLookupChinaCity 城市坐标查询
func TestLookupChinaCity(t *testing.T) {
	lat, _, ok := lookupChinaCity("成都")
	if !ok || lat < 30 || lat > 31 {
		t.Errorf("成都: lat=%v ok=%v", lat, ok)
	}
	// 带"市"后缀
	if _, _, ok := lookupChinaCity("成都市"); !ok {
		t.Error("成都市应匹配")
	}
	if _, _, ok := lookupChinaCity("不存在的城市"); ok {
		t.Error("不存在应 false")
	}
}

// TestHaversine 距离
func TestHaversine(t *testing.T) {
	d := haversine(30.5728, 104.0668, 39.9042, 116.4074) // 成都→北京
	if d < 1400 || d > 1600 {
		t.Errorf("成都-北京=%v, want ~1500", d)
	}
}

// TestSetManualClear 清除人工修正
func TestSetManualClear(t *testing.T) {
	loc := New()
	loc.SetManual(Location{Latitude: 1, Longitude: 2})
	if loc.manual == nil {
		t.Fatal("应已设置")
	}
	loc.SetManual(Location{})
	if loc.manual != nil {
		t.Fatal("应已清除")
	}
}
