// Package acl 实现 Tailscale 风格的 ACL 策略解析、校验与配置生成。
package acl

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Policy 是 Tailscale 风格声明式策略（§6.1）。
//
// JSON 格式示例：
//
//	{
//	  "groups":    { "group:dev": ["alice", "bob"] },
//	  "tagOwners": { "tag:server": ["group:ops"] },
//	  "acls": [
//	    { "action": "accept", "src": ["group:dev"], "dst": ["tag:server:22,443"] }
//	  ]
//	}
type Policy struct {
	Groups    map[string][]string `json:"groups"`    // "group:x" → []user
	TagOwners map[string][]string `json:"tagOwners"` // "tag:x"   → []user|group
	ACLs      []ACLRule           `json:"acls"`
}

// ACLRule 是单条 ACL 规则。
// Src 支持：user、"group:x"、"tag:x"、"*"。
// Dst 支持：user、"group:x"、"tag:x"、"*:*"、"<selector>:<ports>"（"tag:server:22,443"）。
type ACLRule struct {
	Action string   `json:"action"` // 必须为 "accept"
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
}

// ParsePolicy 解析 JSON 字节为 Policy，并调用 Validate。
func ParsePolicy(data []byte) (*Policy, error) {
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("acl: parse policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate 校验 Policy 语义合法性：
//   - action 必须为 "accept"
//   - src/dst 中引用的 "group:x" 必须在 Groups 定义
//   - dst 中的端口语法合法（逗号分隔正整数，0-65535）
func (p *Policy) Validate() error {
	for i, rule := range p.ACLs {
		if rule.Action != "accept" {
			return fmt.Errorf("acl: rule[%d]: unsupported action %q (only \"accept\" allowed)", i, rule.Action)
		}
		for _, s := range rule.Src {
			if err := validateSrc(p, s, i); err != nil {
				return err
			}
		}
		for _, d := range rule.Dst {
			if err := validateDst(p, d, i); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSrc(p *Policy, s string, ruleIdx int) error {
	if s == "*" {
		return nil
	}
	if strings.HasPrefix(s, "group:") {
		name := s
		if _, ok := p.Groups[name]; !ok {
			return fmt.Errorf("acl: rule[%d].src: undefined group %q", ruleIdx, name)
		}
		return nil
	}
	// tag: 或普通 user 都合法（不需要额外引用检查）
	return nil
}

func validateDst(p *Policy, d string, ruleIdx int) error {
	// "*:*" 特殊通配
	if d == "*:*" {
		return nil
	}

	// 解析 dst 形式：selector 或 selector:ports
	selector, ports, err := parseDstSpec(d)
	if err != nil {
		return fmt.Errorf("acl: rule[%d].dst %q: %w", ruleIdx, d, err)
	}

	// 校验 selector 中若含 group: 引用
	if strings.HasPrefix(selector, "group:") {
		if _, ok := p.Groups[selector]; !ok {
			return fmt.Errorf("acl: rule[%d].dst: undefined group %q", ruleIdx, selector)
		}
	}

	// 校验端口号
	for _, portStr := range ports {
		portStr = strings.TrimSpace(portStr)
		if portStr == "" {
			continue
		}
		n, err := strconv.Atoi(portStr)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("acl: rule[%d].dst %q: invalid port %q", ruleIdx, d, portStr)
		}
	}
	return nil
}

// parseDstSpec 解析 "selector" 或 "selector:ports" 或 "tag:name:ports" 形式。
//
// 规则：
//   - "tag:server:22,443" → selector="tag:server", ports=["22","443"]
//   - "group:dev:80"      → selector="group:dev", ports=["80"]
//   - "alice:22"          → selector="alice", ports=["22"]
//   - "group:dev"         → selector="group:dev", ports=[]
//   - "*"                 → selector="*", ports=[]
//   - "*:*"               → 由调用方在上层特判
//
// 由于 tag/group 前缀自身含冒号，需要按前缀判断分割位置。
func parseDstSpec(d string) (selector string, ports []string, err error) {
	// 以下前缀含有冒号，需特殊处理
	prefixes := []string{"tag:", "group:"}
	for _, pfx := range prefixes {
		if strings.HasPrefix(d, pfx) {
			rest := d[len(pfx):]
			// rest = "name" 或 "name:ports"
			idx := strings.Index(rest, ":")
			if idx == -1 {
				// 无端口部分
				return d, nil, nil
			}
			name := rest[:idx]
			portPart := rest[idx+1:]
			selector = pfx + name
			if portPart == "*" {
				// "group:dev:*" → 所有端口，用空 ports 表示
				return selector, nil, nil
			}
			ports = strings.Split(portPart, ",")
			return selector, ports, nil
		}
	}
	// 普通 selector（user / "*"）
	idx := strings.Index(d, ":")
	if idx == -1 {
		return d, nil, nil
	}
	selector = d[:idx]
	portPart := d[idx+1:]
	if portPart == "*" {
		return selector, nil, nil
	}
	ports = strings.Split(portPart, ",")
	return selector, ports, nil
}

// ErrUndefinedGroup 在引用了未定义的 group 时返回（包含在 error 链中）。
var ErrUndefinedGroup = errors.New("undefined group")

// ErrInvalidAction 在 action 不合法时返回。
var ErrInvalidAction = errors.New("invalid action")

// ErrInvalidPort 在端口号语法错误时返回。
var ErrInvalidPort = errors.New("invalid port")
