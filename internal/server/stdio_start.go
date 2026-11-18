package server

import (
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// stdioStartWait is how long after launch a stdio request may wait for the
// server to finish starting. It is shorter than the 30 s the shortest client
// timeout seen in T175 allows, and it is one budget for the whole start: requests
// are served one at a time, so a wait per request would add up past that timeout.
const stdioStartWait = 20 * time.Second

// StdioStart is a stdio server that is still being built. Opening the stores
// costs seconds of CPU, which on a machine with load 50 and 8 cores turns into
// tens of seconds of waiting for a core, and the client's handshake used to wait
// for all of it (T175). The HTTP transport has the same gap covered by its 503.
type StdioStart struct {
	cfg      config.Config
	ready    chan struct{}
	deadline time.Time

	mu  sync.Mutex
	srv *MCPServer
	// early holds what was answered or dropped before the server existed, so it
	// still sees the handshake and the notifications, in order.
	early []rpcRequest
}

// StartStdio runs build in the background and returns at once.
func StartStdio(cfg config.Config, build func() *MCPServer) *StdioStart {
	return startStdio(cfg, build, stdioStartWait)
}

func startStdio(cfg config.Config, build func() *MCPServer, wait time.Duration) *StdioStart {
	st := &StdioStart{
		cfg:      cfg,
		ready:    make(chan struct{}),
		deadline: time.Now().Add(wait),
	}
	go func() {
		srv := build()
		st.mu.Lock()
		defer st.mu.Unlock()
		for _, req := range st.early {
			srv.handle(req)
		}
		st.early = nil
		st.srv = srv
		close(st.ready)
	}()
	return st
}

// Server returns the finished server, or nil while it is still starting.
func (st *StdioStart) Server() *MCPServer {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.srv
}

func (st *StdioStart) handle(req rpcRequest) *rpcResponse {
	notification := isNotification(req.ID)
	if notification || req.Method == "initialize" {
		st.mu.Lock()
		srv := st.srv
		if srv == nil {
			st.early = append(st.early, req)
		}
		st.mu.Unlock()
		switch {
		case srv != nil:
			return srv.handle(req)
		case notification:
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: initializeResult()}
	}

	select {
	case <-st.ready:
	case <-time.After(time.Until(st.deadline)):
	}
	if srv := st.Server(); srv != nil {
		return srv.handle(req)
	}

	// A client asks for the tools once per connection and listChanged is false:
	// an error here would leave it without tools for the whole session. The list
	// follows the configuration; a store that then fails to open answers each
	// call with the usual "not available" error.
	if req.Method == "tools/list" {
		starting := &MCPServer{config: st.cfg}
		tools := starting.toolDefsFor(st.cfg.RAG.Enabled, st.cfg.Memory.Enabled, st.cfg.Memory.Enabled && st.cfg.Steward.Enabled)
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: starting.toolsListResult(tools)}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: startingError(
		"server is still starting: the indexes and the memory cache are loading; retry in a few seconds",
	)}
}

// startingError is the retryable answer for work the server cannot do yet.
func startingError(message string) *rpcError {
	return &rpcError{
		Code:    rpcErrServerError,
		Message: message,
		Data:    map[string]any{"status": "starting", "retry_after_seconds": 5},
	}
}
