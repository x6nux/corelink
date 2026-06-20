package acl

import (
	"strings"
	"testing"
)

// ─── ParsePolicy / Validate 测试 ─────────────────────────────────────────────

func TestParsePolicy_ValidFull(t *testing.T) {
	data := []byte(`{
		"groups":    { "group:dev": ["alice", "bob"], "group:ops": ["carol"] },
		"tagOwners": { "tag:server": ["group:ops"] },
		"acls": [
			{ "action": "accept", "src": ["group:dev"], "dst": ["tag:server:22,443"] },
			{ "action": "accept", "src": ["group:ops"], "dst": ["*:*"] }
		]
	}`)
	p, err := ParsePolicy(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Groups) != 2 {
		t.Errorf("expected 2 groups, got %d", len(p.Groups))
	}
	if len(p.ACLs) != 2 {
		t.Errorf("expected 2 ACLs, got %d", len(p.ACLs))
	}
}

func TestParsePolicy_EmptyPolicy(t *testing.T) {
	data := []byte(`{}`)
	p, err := ParsePolicy(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.ACLs) != 0 {
		t.Error("expected empty ACL list")
	}
}

func TestParsePolicy_InvalidJSON(t *testing.T) {
	_, err := ParsePolicy([]byte(`{ invalid`))
	if err == nil {
		t.Error("expected parse error")
	}
}

func TestValidate_InvalidAction(t *testing.T) {
	data := []byte(`{
		"acls": [{ "action": "deny", "src": ["alice"], "dst": ["bob"] }]
	}`)
	_, err := ParsePolicy(data)
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
	if !strings.Contains(err.Error(), "action") {
		t.Errorf("error should mention 'action', got: %v", err)
	}
}

func TestValidate_UndefinedGroupInSrc(t *testing.T) {
	data := []byte(`{
		"acls": [{ "action": "accept", "src": ["group:unknown"], "dst": ["alice"] }]
	}`)
	_, err := ParsePolicy(data)
	if err == nil {
		t.Fatal("expected error for undefined group in src")
	}
	if !strings.Contains(err.Error(), "group") {
		t.Errorf("error should mention 'group', got: %v", err)
	}
}

func TestValidate_UndefinedGroupInDst(t *testing.T) {
	data := []byte(`{
		"acls": [{ "action": "accept", "src": ["alice"], "dst": ["group:ghost"] }]
	}`)
	_, err := ParsePolicy(data)
	if err == nil {
		t.Fatal("expected error for undefined group in dst")
	}
	if !strings.Contains(err.Error(), "group") {
		t.Errorf("error should mention 'group', got: %v", err)
	}
}

func TestValidate_InvalidPort(t *testing.T) {
	data := []byte(`{
		"acls": [{ "action": "accept", "src": ["alice"], "dst": ["alice:99999"] }]
	}`)
	_, err := ParsePolicy(data)
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("error should mention 'port', got: %v", err)
	}
}

func TestValidate_InvalidPortNaN(t *testing.T) {
	data := []byte(`{
		"acls": [{ "action": "accept", "src": ["alice"], "dst": ["alice:abc"] }]
	}`)
	_, err := ParsePolicy(data)
	if err == nil {
		t.Fatal("expected error for NaN port")
	}
}

func TestValidate_ValidTagWithPorts(t *testing.T) {
	data := []byte(`{
		"tagOwners": { "tag:server": ["alice"] },
		"acls": [{ "action": "accept", "src": ["alice"], "dst": ["tag:server:22,443"] }]
	}`)
	_, err := ParsePolicy(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_WildcardDst(t *testing.T) {
	data := []byte(`{
		"acls": [{ "action": "accept", "src": ["alice"], "dst": ["*:*"] }]
	}`)
	_, err := ParsePolicy(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_UserSrcDst(t *testing.T) {
	data := []byte(`{
		"acls": [{ "action": "accept", "src": ["alice"], "dst": ["bob:80"] }]
	}`)
	_, err := ParsePolicy(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseDstSpec_TagWithPorts(t *testing.T) {
	sel, ports, err := parseDstSpec("tag:server:22,443")
	if err != nil {
		t.Fatal(err)
	}
	if sel != "tag:server" {
		t.Errorf("expected selector 'tag:server', got %q", sel)
	}
	if len(ports) != 2 || ports[0] != "22" || ports[1] != "443" {
		t.Errorf("expected ports [22 443], got %v", ports)
	}
}

func TestParseDstSpec_GroupNoPorts(t *testing.T) {
	sel, ports, err := parseDstSpec("group:dev")
	if err != nil {
		t.Fatal(err)
	}
	if sel != "group:dev" {
		t.Errorf("expected selector 'group:dev', got %q", sel)
	}
	if len(ports) != 0 {
		t.Errorf("expected no ports, got %v", ports)
	}
}

func TestParseDstSpec_UserPort(t *testing.T) {
	sel, ports, err := parseDstSpec("alice:22")
	if err != nil {
		t.Fatal(err)
	}
	if sel != "alice" {
		t.Errorf("expected selector 'alice', got %q", sel)
	}
	if len(ports) != 1 || ports[0] != "22" {
		t.Errorf("expected ports [22], got %v", ports)
	}
}
