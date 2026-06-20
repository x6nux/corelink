package nodemethods

import (
	"encoding/json"

	"github.com/x6nux/corelink/internal/rpc"
)

func registerIngressMethods(s *rpc.Server, deps Deps) {
	s.Register("ingress.list", handleIngressList(deps))
}

func handleIngressList(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		list := deps.Ingresses()
		if list == nil {
			list = []IngressInfo{}
		}
		return list, nil
	}
}
