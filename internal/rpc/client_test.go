package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestClient_Call(t *testing.T) {
	s := NewServer()
	s.Register("add", func(params json.RawMessage) (any, error) {
		var args struct {
			A int `json:"a"`
			B int `json:"b"`
		}
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, err
		}
		return args.A + args.B, nil
	})

	sockPath := startServer(t, s)

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	var sum int
	if err := c.Call("add", map[string]int{"a": 1, "b": 2}, &sum); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if sum != 3 {
		t.Fatalf("expected sum=3, got %d", sum)
	}
}

func TestClient_CallError(t *testing.T) {
	s := NewServer()
	s.Register("fail", func(params json.RawMessage) (any, error) {
		return nil, fmt.Errorf("intentional failure")
	})

	sockPath := startServer(t, s)

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	err = c.Call("fail", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != CodeInternalError {
		t.Fatalf("expected code %d, got %d", CodeInternalError, rpcErr.Code)
	}
}

func TestClient_MethodNotFound(t *testing.T) {
	s := NewServer()
	sockPath := startServer(t, s)

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	err = c.Call("nonexistent", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("expected code %d, got %d", CodeMethodNotFound, rpcErr.Code)
	}
}

func TestClient_Stream(t *testing.T) {
	const streamCount = 5

	s := NewServer()
	s.RegisterStream("events", func(ctx context.Context, params json.RawMessage, send func(any) error) error {
		for i := range streamCount {
			if err := send(map[string]int{"seq": i}); err != nil {
				return err
			}
		}
		return nil
	})

	sockPath := startServer(t, s)

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	var received []int
	err = c.Stream("events", nil, func(data json.RawMessage) error {
		var m map[string]int
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		received = append(received, m["seq"])
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(received) != streamCount {
		t.Fatalf("expected %d items, got %d", streamCount, len(received))
	}
	for i, v := range received {
		if v != i {
			t.Fatalf("item %d: expected seq=%d, got %d", i, i, v)
		}
	}
}

func TestClient_CallNilResult(t *testing.T) {
	s := NewServer()
	s.Register("ping", func(params json.RawMessage) (any, error) {
		return "pong", nil
	})

	sockPath := startServer(t, s)

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// result=nil — must not panic and must return nil error.
	if err := c.Call("ping", nil, nil); err != nil {
		t.Fatalf("Call with nil result: %v", err)
	}
}

func TestClient_ConcurrentCalls(t *testing.T) {
	s := NewServer()
	s.Register("double", func(params json.RawMessage) (any, error) {
		var args struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, err
		}
		return args.N * 2, nil
	})

	sockPath := startServer(t, s)

	c, err := Dial(sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	const goroutines = 5
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := range goroutines {
		wg.Go(func() {
			var result int
			if err := c.Call("double", map[string]int{"n": i}, &result); err != nil {
				errs <- fmt.Errorf("goroutine %d: Call: %w", i, err)
				return
			}
			if result != i*2 {
				errs <- fmt.Errorf("goroutine %d: expected %d, got %d", i, i*2, result)
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}
