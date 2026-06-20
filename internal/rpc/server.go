package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// Handler processes a single RPC method call and returns a result or error.
type Handler func(params json.RawMessage) (any, error)

// StreamHandler processes a streaming RPC (e.g. log push). It sends data
// continuously via send until ctx is cancelled or the handler returns.
type StreamHandler func(ctx context.Context, params json.RawMessage, send func(any) error) error

// Server is a JSON-RPC 2.0 server over Unix domain sockets.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	streams  map[string]StreamHandler
	ln       net.Listener
	done     chan struct{}
	wg       sync.WaitGroup

	connMu sync.Mutex
	conns  map[net.Conn]struct{}

	closeOnce sync.Once
}

// NewServer creates a new Server ready to register handlers.
func NewServer() *Server {
	return &Server{
		handlers: make(map[string]Handler),
		streams:  make(map[string]StreamHandler),
		done:     make(chan struct{}),
		conns:    make(map[net.Conn]struct{}),
	}
}

// Register registers a plain RPC method handler.
func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// RegisterStream registers a streaming RPC method handler.
func (s *Server) RegisterStream(method string, h StreamHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams[method] = h
}

// Serve listens on the given Unix socket path and serves requests. It blocks
// until Close is called or a fatal listener error occurs.
// If sockPath already exists (stale socket) it is removed first.
// The socket file permission is set to 0600 (owner only).
func (s *Server) Serve(sockPath string) error {
	// Remove stale socket file (idempotent).
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0600); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	return s.acceptLoop(ln)
}

// acceptLoop 持续 Accept 新连接。临时错误（net.Error.Temporary）采用带上限的
// 指数退避后重试，避免 fd 耗尽等瞬时故障导致 busy-loop；listener 关闭（s.done
// 已关闭）时优雅返回 nil；其它非临时错误视为致命错误并返回。
func (s *Server) acceptLoop(ln net.Listener) error {
	const (
		baseDelay = 5 * time.Millisecond
		maxDelay  = 1 * time.Second
	)
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			// 正在关闭：优雅退出。
			select {
			case <-s.done:
				return nil
			default:
			}
			// 临时错误：指数退避后重试，不退出。
			var ne net.Error
			if errors.As(err, &ne) && ne.Temporary() {
				if delay == 0 {
					delay = baseDelay
				} else {
					delay *= 2
				}
				if delay > maxDelay {
					delay = maxDelay
				}
				time.Sleep(delay)
				continue
			}
			// 非临时错误：致命，退出。
			return fmt.Errorf("accept: %w", err)
		}
		// 成功 accept → 重置退避。
		delay = 0
		// 在 connMu 临界区内先查 done 再 wg.Add+登记，与 Close 的 close(done)+
		// wg.Wait() 串行化，杜绝 Add-after-Wait：正在关闭则丢弃本连接、优雅退出。
		s.connMu.Lock()
		select {
		case <-s.done:
			s.connMu.Unlock()
			_ = conn.Close()
			return nil
		default:
		}
		s.wg.Add(1)
		s.conns[conn] = struct{}{}
		s.connMu.Unlock()
		go s.handleConn(conn)
	}
}

// Close stops accepting new connections and waits for all active connections
// to finish processing.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.RLock()
		ln := s.ln
		s.mu.RUnlock()
		// close(done) 与关闭活动连接放入 connMu 临界区，与 acceptLoop 的
		// 「查 done→wg.Add→登记」串行化，杜绝 Add-after-Wait。
		s.connMu.Lock()
		close(s.done)
		for c := range s.conns {
			c.Close()
		}
		s.connMu.Unlock()
		// 关 listener 解除 Accept 阻塞（在 connMu 外，避免与 acceptLoop 互等）。
		if ln != nil {
			ln.Close()
		}
	})
	s.wg.Wait()
	return nil
}

// handleConn processes a single client connection.
func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()

	// conn 已由 acceptLoop 在 connMu 临界区登记（与 Close 串行）；这里只负责退出清理。
	defer func() {
		conn.Close()
		s.connMu.Lock()
		delete(s.conns, conn)
		s.connMu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// connWriter serialises all writes on this connection.
	var writeMu sync.Mutex

	writeResponse := func(resp *Response) {
		data, err := EncodeResponse(resp)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, _ = conn.Write(data)
	}

	scanner := bufio.NewScanner(conn)
	// 1 MB max token size to handle large payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		req, err := DecodeRequest(line)
		if err != nil {
			// Parse error — only write a response if we can't tell it's a
			// notification (which by definition has no parsable ID anyway,
			// so we always respond on parse errors).
			writeResponse(NewErrorResponse(nil, CodeParseError, "parse error: "+err.Error()))
			continue
		}

		s.mu.RLock()
		handler, okHandler := s.handlers[req.Method]
		stream, okStream := s.streams[req.Method]
		s.mu.RUnlock()

		switch {
		case okStream:
			// 流处理器在独立 goroutine 中运行，避免阻塞本连接的读循环——
			// 否则长流（如 system.logs 的 <-ctx.Done()）会卡住 Scan()，
			// 导致连接关闭/Close 无法触发 defer cancel()，流永不退出。
			// 用 s.wg 跟踪，保证 Close 的 wg.Wait() 等待流处理器退出。
			// s.wg.Add(1) 在派生 goroutine 之前、且发生在已被 wg.Add(1)
			// 跟踪的 handleConn 内，wg.Wait() 在 handleConn 返回前不会越过
			// 这里，无 Add-after-Wait。
			s.wg.Add(1)
			go func(req *Request, h StreamHandler) {
				defer s.wg.Done()
				s.handleStream(ctx, req, h, writeResponse)
			}(req, stream)
		case okHandler:
			s.handleCall(req, handler, writeResponse)
		default:
			// Method not found.
			if req.ID != nil {
				writeResponse(NewErrorResponse(req.ID, CodeMethodNotFound, "method not found: "+req.Method))
			}
		}
	}
}

// handleCall executes a plain Handler and writes the response.
func (s *Server) handleCall(req *Request, h Handler, write func(*Response)) {
	result, err := h(req.Params)
	if req.ID == nil {
		// Notification — no response.
		return
	}
	if err != nil {
		write(NewErrorResponse(req.ID, CodeInternalError, err.Error()))
		return
	}
	resp, encErr := NewResponse(req.ID, result)
	if encErr != nil {
		write(NewErrorResponse(req.ID, CodeInternalError, encErr.Error()))
		return
	}
	write(resp)
}

// handleStream executes a StreamHandler, sending intermediate results and a
// final end-of-stream marker.
func (s *Server) handleStream(ctx context.Context, req *Request, h StreamHandler, write func(*Response)) {
	send := func(v any) error {
		resp, err := NewResponse(req.ID, v)
		if err != nil {
			return err
		}
		write(resp)
		return nil
	}

	err := h(ctx, req.Params, send)
	if req.ID == nil {
		return
	}
	if err != nil {
		write(NewErrorResponse(req.ID, CodeInternalError, err.Error()))
		return
	}
	// End-of-stream marker: result=null.
	write(&Response{
		JSONRPC: "2.0",
		Result:  json.RawMessage("null"),
		ID:      req.ID,
	})
}
