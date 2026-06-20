package ctrlmethods

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/x6nux/corelink/internal/controller/store"
	"github.com/x6nux/corelink/internal/rpc"
)

// certDTO is the RPC view of a certificate.
type certDTO struct {
	Serial    string     `json:"serial"`
	NodeID    string     `json:"node_id"`
	NotAfter  time.Time  `json:"not_after"`
	Revoked   bool       `json:"revoked"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// caInfoResult is the response for ca.info.
type caInfoResult struct {
	CACertPEM string `json:"ca_cert_pem"`
	CAHash    string `json:"ca_hash"`
}

func registerCertsMethods(s *rpc.Server, deps Deps) {
	s.Register("certs.list", handleCertsList(deps))
	s.Register("ca.info", handleCAInfo(deps))
}

func handleCertsList(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		certs, err := deps.Store.ListCerts()
		if err != nil {
			return nil, err
		}
		out := make([]certDTO, 0, len(certs))
		for i := range certs {
			out = append(out, toCertDTO(&certs[i]))
		}
		return out, nil
	}
}

func handleCAInfo(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		if deps.CA == nil {
			return nil, fmt.Errorf("CA not available")
		}
		pem, err := deps.CA.CACertPEM()
		if err != nil {
			return nil, err
		}
		h, err := deps.CA.CAPublicKeyHash()
		if err != nil {
			return nil, err
		}
		return caInfoResult{
			CACertPEM: string(pem),
			CAHash:    h,
		}, nil
	}
}

func toCertDTO(c *store.Cert) certDTO {
	return certDTO{
		Serial:    c.Serial,
		NodeID:    c.NodeID,
		NotAfter:  c.NotAfter,
		Revoked:   c.Revoked,
		RevokedAt: c.RevokedAt,
	}
}
