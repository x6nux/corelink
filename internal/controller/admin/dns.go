package admin

import (
	"encoding/json"
	"net/http"

	"github.com/x6nux/corelink/internal/controller/store"
)

type dnsDTO struct {
	ID            uint     `json:"id"`
	Enabled       bool     `json:"enabled"`
	Zones         []string `json:"zones"`
	Upstreams     []string `json:"upstreams"`
	InterceptMode string   `json:"intercept_mode"`
	ListenAddr    string   `json:"listen_addr"`
	ListenPort    uint32   `json:"listen_port"`
	LANIfaces     []string `json:"lan_interfaces,omitempty"`
	LANCIDRs      []string `json:"lan_cidrs,omitempty"`
}

type putDNSRequest struct {
	Enabled       bool     `json:"enabled"`
	Zones         []string `json:"zones"`
	Upstreams     []string `json:"upstreams"`
	InterceptMode string   `json:"intercept_mode"`
	ListenAddr    string   `json:"listen_addr"`
	ListenPort    uint32   `json:"listen_port"`
	LANIfaces     []string `json:"lan_interfaces"`
	LANCIDRs      []string `json:"lan_cidrs"`
}

func (s *Server) handleGetDNS(w http.ResponseWriter, _ *http.Request) {
	d, err := s.deps.Store.GetDNSSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取 DNS 设置失败")
		return
	}
	if d == nil {
		writeJSON(w, http.StatusOK, dnsDTO{})
		return
	}
	writeJSON(w, http.StatusOK, toDNSDTO(d))
}

func (s *Server) handlePutDNS(w http.ResponseWriter, r *http.Request) {
	var req putDNSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	zonesJSON, _ := json.Marshal(req.Zones)
	upstreamsJSON, _ := json.Marshal(req.Upstreams)
	lanIfacesJSON, _ := json.Marshal(req.LANIfaces)
	lanCIDRsJSON, _ := json.Marshal(req.LANCIDRs)

	d := &store.DNSSettings{
		ID:            1,
		Enabled:       req.Enabled,
		ZonesJSON:     string(zonesJSON),
		UpstreamsJSON: string(upstreamsJSON),
		InterceptMode: req.InterceptMode,
		ListenAddr:    req.ListenAddr,
		ListenPort:    req.ListenPort,
		LANIfacesJSON: string(lanIfacesJSON),
		LANCIDRsJSON:  string(lanCIDRsJSON),
	}
	if err := s.deps.Store.UpsertDNSSettings(d); err != nil {
		writeError(w, http.StatusInternalServerError, "保存 DNS 设置失败")
		return
	}
	s.recomputeAll()
	writeJSON(w, http.StatusOK, toDNSDTO(d))
}

func toDNSDTO(d *store.DNSSettings) dnsDTO {
	dto := dnsDTO{
		ID:            d.ID,
		Enabled:       d.Enabled,
		InterceptMode: d.InterceptMode,
		ListenAddr:    d.ListenAddr,
		ListenPort:    d.ListenPort,
	}
	if d.ZonesJSON != "" {
		_ = json.Unmarshal([]byte(d.ZonesJSON), &dto.Zones)
	}
	if d.UpstreamsJSON != "" {
		_ = json.Unmarshal([]byte(d.UpstreamsJSON), &dto.Upstreams)
	}
	if d.LANIfacesJSON != "" {
		_ = json.Unmarshal([]byte(d.LANIfacesJSON), &dto.LANIfaces)
	}
	if d.LANCIDRsJSON != "" {
		_ = json.Unmarshal([]byte(d.LANCIDRsJSON), &dto.LANCIDRs)
	}
	return dto
}
