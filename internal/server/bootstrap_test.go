package server

import (
	"path/filepath"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
)

// TestNewWiresMemoryStore covers the init* decomposition (Round 3 H17): New with
// memory enabled must actually construct and wire a memory store. The existing
// test helpers set memoryStore manually and so never exercised New's memory
// init path.
func TestNewWiresMemoryStore(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		RootPath:   root,
		OutputMode: "line",
		Memory: config.MemoryConfig{
			Enabled: true,
			DBPath:  filepath.Join(root, "memory.db"),
		},
	}
	guard, err := paths.NewGuard(cfg)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	srv := New(cfg, guard)
	if srv == nil {
		t.Fatal("New returned nil")
	}
	t.Cleanup(srv.Shutdown)

	if srv.memoryStore == nil {
		t.Fatal("New with MemoryEnabled must wire a memory store")
	}
	if srv.toolHandlers == nil {
		t.Fatal("New must build tool handlers")
	}
}

// TestNewMemoryDisabledLeavesStoreNil pins the other branch: no memory store,
// no steward service, and the embedder adapts to a true nil interface.
func TestNewMemoryDisabledLeavesStoreNil(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{RootPath: root, OutputMode: "line", Memory: config.MemoryConfig{Enabled: false}}
	guard, err := paths.NewGuard(cfg)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	srv := New(cfg, guard)
	t.Cleanup(srv.Shutdown)

	if srv.memoryStore != nil {
		t.Fatal("memory store must be nil when MemoryEnabled=false")
	}
	if srv.stewardService != nil {
		t.Fatal("steward service must be nil without a memory store")
	}
	if srv.embedder != nil {
		t.Fatal("embedder must be a nil interface when memory is disabled")
	}
}
