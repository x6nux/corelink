package nodemethods

import (
	"encoding/json"

	"github.com/x6nux/corelink/internal/rpc"
)

func registerPortmapMethods(s *rpc.Server, deps Deps) {
	s.Register("portmap.list", handlePortmapList(deps))
	s.Register("portmap.status", handlePortmapStatus(deps))
}

func handlePortmapList(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		list := deps.PortmapMappings()
		if list == nil {
			list = []MappingInfo{}
		}
		return list, nil
	}
}

func handlePortmapStatus(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		return deps.PortmapStatus(), nil
	}
}
