package nodemethods

import (
	"encoding/json"

	"github.com/x6nux/corelink/internal/rpc"
)

func registerConnectionsMethods(s *rpc.Server, deps Deps) {
	s.Register("connections.list", handleConnectionsList(deps))
}

func handleConnectionsList(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		list := deps.Connections()
		if list == nil {
			list = []ConnectionInfo{}
		}
		return list, nil
	}
}
