package ingress

import (
	"net"
	"net/http"
)

// MyIPHandler is an http.Handler that answers GET /v1/myip with the requester's
// public source IP as plain text (spec §3.3: a node queries this controller
// endpoint to learn its public IP as one of the ingress-discovery signals).
//
// The IP is taken from r.RemoteAddr (the address the controller actually sees on
// the connection). Reverse-proxy / load-balancer scenarios, where the real
// client IP must instead be taken from a trusted X-Forwarded-For header, are
// handled in P8; this task intentionally trusts only RemoteAddr.
type MyIPHandler struct{}

// NewMyIPHandler constructs a MyIPHandler.
func NewMyIPHandler() *MyIPHandler {
	return &MyIPHandler{}
}

// ServeHTTP writes the requester's source IP (host portion of RemoteAddr) as a
// plain-text response. Only GET is accepted.
func (h *MyIPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = host
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(ip))
}
