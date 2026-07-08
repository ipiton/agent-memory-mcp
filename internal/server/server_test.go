package server

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"go.uber.org/zap"
)

func newTestServer(t *testing.T, authToken string) *MCPServer {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		RootPath:      root,
		HTTPHost:      "127.0.0.1",
		HTTPAuthToken: authToken,
		OutputMode:    "line",
	}
	guard, err := paths.NewGuard(cfg)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return New(cfg, guard)
}

func buildMux(s *MCPServer) http.Handler {
	return buildHTTPMux(s)
}

func newMemoryTestServer(t *testing.T) *MCPServer {
	t.Helper()
	s := newTestServer(t, "")
	store, err := memory.NewStore(filepath.Join(t.TempDir(), "memory.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.memoryStore = store
	return s
}

func newAutoSessionTestServer(t *testing.T, idleTimeout time.Duration, checkpointInterval time.Duration, minEvents int) *MCPServer {
	t.Helper()
	s := newMemoryTestServer(t)
	s.config.SessionTrackingEnabled = true
	s.config.SessionIdleTimeout = idleTimeout
	s.config.SessionCheckpointInterval = checkpointInterval
	s.config.SessionMinEvents = minEvents
	s.sessionTracker = newSessionTracker(s.config, s.memoryStore, nil)
	return s
}

func containsTag(tags []string, wanted string) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}
