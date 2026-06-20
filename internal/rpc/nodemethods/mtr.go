package nodemethods

import (
	"encoding/json"
	"fmt"

	"github.com/x6nux/corelink/internal/rpc"
)

// MTRHop 描述 MTR 路径中的一跳统计信息。
type MTRHop struct {
	Hop      int     `json:"hop"`
	NodeID   string  `json:"node_id"`
	VIP      string  `json:"vip"`
	Hostname string  `json:"hostname"`
	Sent     int     `json:"sent"`
	Recv     int     `json:"recv"`
	LossPct  float64 `json:"loss_pct"`
	LastMs   float64 `json:"last_ms"`
	BestMs   float64 `json:"best_ms"`
	AvgMs    float64 `json:"avg_ms"`
	WorstMs  float64 `json:"worst_ms"`
	StdevMs  float64 `json:"stdev_ms"`
}

// MTRResult 是 MTR 追踪结果。
type MTRResult struct {
	Source string   `json:"source"`
	Target string   `json:"target"`
	Via    string   `json:"via"`
	Hops   []MTRHop `json:"hops"`
}

type mtrParam struct {
	Target    string   `json:"target"`
	Count     int      `json:"count"`
	Via       []string `json:"via,omitempty"`        // 指定中继路径（nodeID/VIP/前缀），按顺序多跳
	ReplyMode string   `json:"reply_mode,omitempty"` // 回包模式: "auto"(自然路由) / "trace"(原路返回)，空=按路由模式默认
}

func registerMTRMethods(s *rpc.Server, deps Deps) {
	s.Register("debug.mtr", handleDebugMTR(deps))
	s.Register("debug.mtr_enum", handleDebugMTREnum(deps))
	s.Register("debug.mtr_enum_all", handleDebugMTREnumAll(deps))
}

func handleDebugMTR(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		var p mtrParam
		if err := json.Unmarshal(params, &p); err != nil || p.Target == "" {
			return nil, fmt.Errorf("参数错误: 需要 {\"target\": \"...\", \"count\": 10}")
		}
		if deps.DebugMTR == nil {
			return nil, fmt.Errorf("MTR 接口未注入")
		}
		if p.Count <= 0 {
			p.Count = 10
		}
		if p.Count > 100 {
			p.Count = 100
		}
		result, err := deps.DebugMTR(p.Target, p.Count, p.Via, p.ReplyMode)
		if err != nil {
			return nil, err
		}
		return result, nil
	}
}

// MTREnumRoute 穷举模式中一条路由的探测结果。
type MTREnumRoute struct {
	Label string  `json:"label"`
	RTTMs float64 `json:"rtt_ms"`
	Loss  bool    `json:"loss"`
}

// MTREnumResult 穷举模式单目标结果。
type MTREnumResult struct {
	Source  string         `json:"source"`
	Target  string         `json:"target"`
	Routes  []MTREnumRoute `json:"routes"`
}

type mtrEnumParam struct {
	Target string `json:"target"`
}

// MTREnumAllResult 全量穷举结果（所有目标）。
type MTREnumAllResult struct {
	Results []MTREnumResult `json:"results"`
}

func handleDebugMTREnumAll(deps Deps) rpc.Handler {
	return func(_ json.RawMessage) (any, error) {
		if deps.DebugMTREnumAll == nil {
			return nil, fmt.Errorf("MTR 全量穷举接口未注入")
		}
		return deps.DebugMTREnumAll()
	}
}

func handleDebugMTREnum(deps Deps) rpc.Handler {
	return func(params json.RawMessage) (any, error) {
		if deps.DebugMTREnum == nil {
			return nil, fmt.Errorf("MTR 穷举接口未注入")
		}
		var p mtrEnumParam
		if err := json.Unmarshal(params, &p); err != nil || p.Target == "" {
			return nil, fmt.Errorf("参数错误: 需要 {\"target\": \"...\"}")
		}
		return deps.DebugMTREnum(p.Target)
	}
}
