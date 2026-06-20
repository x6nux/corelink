package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

// Client connects to a JSON-RPC 2.0 server over a Unix domain socket.
type Client struct {
	conn   net.Conn
	sc     *bufio.Scanner
	mu     sync.Mutex // protects write + read (sequential requests on a single connection)
	nextID int
}

// Dial connects to the Unix domain socket at sockPath.
func Dial(sockPath string) (*Client, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial unix %s: %w", sockPath, err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Client{
		conn:   conn,
		sc:     sc,
		nextID: 1,
	}, nil
}

// Call performs a synchronous JSON-RPC 2.0 method call. If result is nil the
// response payload is discarded. When the server returns an RPC error the
// returned error is of type *RPCError (callers may type-assert to inspect the
// error code).
func (c *Client) Call(method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID
	c.nextID++

	req, err := NewRequest(id, method, params)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	data, err := EncodeRequest(req)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	if _, err := c.conn.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return fmt.Errorf("read: %w", err)
		}
		return fmt.Errorf("read: unexpected EOF")
	}

	resp, err := DecodeResponse(c.sc.Bytes())
	if err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if resp.Error != nil {
		return resp.Error
	}

	if result != nil && resp.Result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("unmarshal result: %w", err)
		}
	}

	return nil
}

// Stream calls a streaming RPC method. onData is invoked for each intermediate
// result. The stream ends when the server sends a result=null terminator.
func (c *Client) Stream(method string, params any, onData func(json.RawMessage) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := c.nextID
	c.nextID++

	req, err := NewRequest(id, method, params)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	data, err := EncodeRequest(req)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	if _, err := c.conn.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	for {
		if !c.sc.Scan() {
			if err := c.sc.Err(); err != nil {
				return fmt.Errorf("read: %w", err)
			}
			return fmt.Errorf("read: unexpected EOF during stream")
		}

		resp, err := DecodeResponse(c.sc.Bytes())
		if err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		if resp.Error != nil {
			return resp.Error
		}

		// End-of-stream marker: result == null.
		if string(resp.Result) == "null" {
			return nil
		}

		if err := onData(resp.Result); err != nil {
			return fmt.Errorf("onData: %w", err)
		}
	}
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
