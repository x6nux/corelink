package rpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewRequest(t *testing.T) {
	type params struct {
		Name string `json:"name"`
	}
	r, err := NewRequest(1, "echo", params{Name: "hello"})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if r.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want %q", r.JSONRPC, "2.0")
	}
	if r.Method != "echo" {
		t.Errorf("Method = %q, want %q", r.Method, "echo")
	}
	if r.ID == nil || *r.ID != 1 {
		t.Errorf("ID = %v, want 1", r.ID)
	}
	// Params should be valid JSON containing "hello"
	var p params
	if err := json.Unmarshal(r.Params, &p); err != nil {
		t.Fatalf("Unmarshal Params: %v", err)
	}
	if p.Name != "hello" {
		t.Errorf("Params.Name = %q, want %q", p.Name, "hello")
	}
}

func TestNewResponse(t *testing.T) {
	id := 42
	type result struct {
		Value int `json:"value"`
	}
	resp, err := NewResponse(&id, result{Value: 7})
	if err != nil {
		t.Fatalf("NewResponse: %v", err)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want %q", resp.JSONRPC, "2.0")
	}
	if resp.ID == nil || *resp.ID != 42 {
		t.Errorf("ID = %v, want 42", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("Error = %v, want nil", resp.Error)
	}
	var r result
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("Unmarshal Result: %v", err)
	}
	if r.Value != 7 {
		t.Errorf("Result.Value = %d, want 7", r.Value)
	}
}

func TestNewErrorResponse(t *testing.T) {
	id := 10
	resp := NewErrorResponse(&id, CodeMethodNotFound, "method not found")
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want %q", resp.JSONRPC, "2.0")
	}
	if resp.ID == nil || *resp.ID != 10 {
		t.Errorf("ID = %v, want 10", resp.ID)
	}
	if resp.Result != nil {
		t.Errorf("Result = %s, want nil", resp.Result)
	}
	if resp.Error == nil {
		t.Fatal("Error is nil, want non-nil")
	}
	if resp.Error.Code != CodeMethodNotFound {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, CodeMethodNotFound)
	}
	if resp.Error.Message != "method not found" {
		t.Errorf("Error.Message = %q, want %q", resp.Error.Message, "method not found")
	}
}

func TestEncodeDecodeRequest(t *testing.T) {
	type params struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	orig, err := NewRequest(5, "add", params{X: 3, Y: 4})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	data, err := EncodeRequest(orig)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	// Must end with newline
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("EncodeRequest output does not end with newline")
	}

	decoded, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if decoded.JSONRPC != orig.JSONRPC {
		t.Errorf("JSONRPC = %q, want %q", decoded.JSONRPC, orig.JSONRPC)
	}
	if decoded.Method != orig.Method {
		t.Errorf("Method = %q, want %q", decoded.Method, orig.Method)
	}
	if decoded.ID == nil || *decoded.ID != *orig.ID {
		t.Errorf("ID mismatch")
	}

	var p params
	if err := json.Unmarshal(decoded.Params, &p); err != nil {
		t.Fatalf("Unmarshal decoded Params: %v", err)
	}
	if p.X != 3 || p.Y != 4 {
		t.Errorf("Params = {%d,%d}, want {3,4}", p.X, p.Y)
	}
}

func TestEncodeDecodeResponse(t *testing.T) {
	id := 99
	orig, err := NewResponse(&id, map[string]string{"status": "ok"})
	if err != nil {
		t.Fatalf("NewResponse: %v", err)
	}

	data, err := EncodeResponse(orig)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}

	if !strings.HasSuffix(string(data), "\n") {
		t.Error("EncodeResponse output does not end with newline")
	}

	decoded, err := DecodeResponse(data)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if decoded.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want %q", decoded.JSONRPC, "2.0")
	}
	if decoded.ID == nil || *decoded.ID != 99 {
		t.Errorf("ID = %v, want 99", decoded.ID)
	}
	if decoded.Error != nil {
		t.Errorf("Error = %v, want nil", decoded.Error)
	}

	var m map[string]string
	if err := json.Unmarshal(decoded.Result, &m); err != nil {
		t.Fatalf("Unmarshal Result: %v", err)
	}
	if m["status"] != "ok" {
		t.Errorf("Result[status] = %q, want %q", m["status"], "ok")
	}
}

func TestDecodeResponse_Error(t *testing.T) {
	id := 7
	orig := NewErrorResponse(&id, CodeInternalError, "something broke")

	data, err := EncodeResponse(orig)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}

	decoded, err := DecodeResponse(data)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if decoded.Error == nil {
		t.Fatal("Error is nil after decode, want non-nil")
	}
	if decoded.Error.Code != CodeInternalError {
		t.Errorf("Error.Code = %d, want %d", decoded.Error.Code, CodeInternalError)
	}
	if decoded.Error.Message != "something broke" {
		t.Errorf("Error.Message = %q, want %q", decoded.Error.Message, "something broke")
	}
	if decoded.Result != nil {
		t.Errorf("Result = %s, want nil", decoded.Result)
	}
}

func TestRPCError_ErrorInterface(t *testing.T) {
	e := &RPCError{Code: CodeParseError, Message: "parse error"}
	var err error = e // must satisfy error interface
	s := err.Error()
	if s == "" {
		t.Error("Error() returned empty string")
	}
	if !strings.Contains(s, "parse error") {
		t.Errorf("Error() = %q, want it to contain %q", s, "parse error")
	}
	if !strings.Contains(s, "-32700") {
		t.Errorf("Error() = %q, want it to contain the error code", s)
	}
}

func TestRequest_Notification(t *testing.T) {
	// A notification has ID = nil
	r := &Request{
		JSONRPC: "2.0",
		Method:  "notify",
	}
	data, err := EncodeRequest(r)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	// The serialized JSON should NOT contain an "id" field
	var raw map[string]json.RawMessage
	trimmed := strings.TrimSpace(string(data))
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["id"]; ok {
		t.Error("notification request should not have 'id' field in JSON")
	}

	// Round-trip: decoded ID should be nil
	decoded, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if decoded.ID != nil {
		t.Errorf("decoded notification ID = %v, want nil", decoded.ID)
	}
}

func TestErrorCodes(t *testing.T) {
	tests := []struct {
		name string
		code int
		want int
	}{
		{"ParseError", CodeParseError, -32700},
		{"MethodNotFound", CodeMethodNotFound, -32601},
		{"InvalidParams", CodeInvalidParams, -32602},
		{"InternalError", CodeInternalError, -32603},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.code != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.code, tt.want)
			}
		})
	}
}
