package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// T173: the lock is an flock on its own open file, so a second open blocks
// even inside one process — the same exclusion two hook processes get.
func TestLockSessionCaptureExcludesSecondHolder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bank", "memories.db")

	unlock, err := lockSessionCapture(dbPath)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	acquired := make(chan func(), 1)
	go func() {
		second, err := lockSessionCapture(dbPath)
		if err != nil {
			t.Errorf("second lock: %v", err)
			close(acquired)
			return
		}
		acquired <- second
	}()

	select {
	case <-acquired:
		t.Fatal("second holder got the lock while the first still held it")
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	select {
	case second := <-acquired:
		if second != nil {
			second()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second holder never got the lock after the first released it")
	}
}

// T173: two auto-captures of one session at once used to both pass the
// duplicate check and both write. Serialised, the second sees the first.
func TestConcurrentAutoCapturesWriteOneRecord(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mem.db")
	t.Setenv("MCP_MEMORY_DB_PATH", dbPath)
	t.Setenv("MCP_RAG_ENABLED", "false")

	const summary = "the archive sweep promoted every procedural memory at threshold 1.0 because the counter it read was the sweep-driven one, fixed by reading the targeted counter"

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- runAutoCapture([]string{"--summary", summary, "--context", "lockproj"})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("auto-capture: %v", err)
		}
	}

	store, err := memory.NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("open bank: %v", err)
	}
	defer func() { _ = store.Close() }()
	items, err := store.List(context.Background(), memory.Filters{Context: "lockproj", Type: memory.TypeEpisodic}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	terminal := 0
	for _, m := range items {
		if memory.IsTerminalRecord(m) {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("two concurrent captures of one session wrote %d records, want 1", terminal)
	}
}
