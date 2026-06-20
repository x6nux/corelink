// Package rpc defines JSON-RPC 2.0 request/response/error types and
// serialization helpers. It is transport-agnostic — no networking logic.
package rpc

import (
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      *int            `json:"id,omitempty"` // nil = notification
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      *int            `json:"id"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// ---------------------------------------------------------------------------
// Standard error codes
// ---------------------------------------------------------------------------

const (
	CodeParseError     = -32700
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewRequest builds a JSON-RPC 2.0 request. params is marshalled to
// json.RawMessage; pass nil if the method takes no parameters.
func NewRequest(id int, method string, params any) (*Request, error) {
	r := &Request{
		JSONRPC: "2.0",
		Method:  method,
		ID:      &id,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		r.Params = raw
	}
	return r, nil
}

// NewResponse builds a successful JSON-RPC 2.0 response.
func NewResponse(id *int, result any) (*Response, error) {
	resp := &Response{
		JSONRPC: "2.0",
		ID:      id,
	}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("marshal result: %w", err)
		}
		resp.Result = raw
	}
	return resp, nil
}

// NewErrorResponse builds an error JSON-RPC 2.0 response.
func NewErrorResponse(id *int, code int, message string) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
	}
}

// ---------------------------------------------------------------------------
// Encode / Decode
// ---------------------------------------------------------------------------

// EncodeRequest serializes a Request to JSON bytes with a trailing newline.
func EncodeRequest(r *Request) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// DecodeRequest deserializes a Request from JSON bytes (tolerates trailing
// whitespace / newline).
func DecodeRequest(data []byte) (*Request, error) {
	var r Request
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// EncodeResponse serializes a Response to JSON bytes with a trailing newline.
func EncodeResponse(r *Response) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// DecodeResponse deserializes a Response from JSON bytes (tolerates trailing
// whitespace / newline).
func DecodeResponse(data []byte) (*Response, error) {
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
