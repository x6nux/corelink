package nodemethods

import (
	"encoding/json"

	"github.com/x6nux/corelink/internal/rpc"
)

func registerConfigMethods(s *rpc.Server, deps Deps) {
	s.Register("config.get", handleConfigGet(deps))
}

func handleConfigGet(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		return deps.Config(), nil
	}
}
