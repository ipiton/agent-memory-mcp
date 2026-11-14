package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"go.uber.org/zap"
)

// lockSessionCapture serialises session writes from hook processes on one bank
// (T173). auto-capture and checkpoint decide "is this a duplicate" by reading
// the bank and then write; two hook processes doing that at once both see no
// duplicate and both write. The store also loads its cache when it opens, so
// the lock has to be held from before the open until after the write — taken
// later, the second process would check against a cache that predates the
// first one's record. The kernel drops the lock if the process dies.
func lockSessionCapture(dbPath string) (unlock func(), err error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "session-capture.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock session capture: %w", err)
	}
	return func() { _ = file.Close() }, nil
}

// initMemoryStore creates an embedder and memory store from config.
// Returns store, cleanup function, and error.
func initMemoryStore(cfg config.Config) (*memory.Store, func(), error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Memory.DBPath), 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create memory directory: %w", err)
	}

	emb, err := embedder.New(cfg.EmbedderConfig(), zap.NewNop())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: embedder unavailable, semantic search disabled: %v\n", err)
		emb = nil
	}

	store, err := memory.NewStore(cfg.Memory.DBPath, embedder.AsService(emb), zap.NewNop())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open memory store: %w", err)
	}
	// T68/T76a/T121: match server-side recall scoring exactly — decay rate,
	// which types decay, and the centered score. A CLI that ranks differently
	// from the service is a second opinion nobody asked for.
	store.SetRecallHalfLife(cfg.Sediment.RecallHalfLifeDays)
	store.SetRecallDecayTypes(memory.TypesFromStrings(cfg.Sediment.RecallDecayTypes))
	store.SetRecallCentered(cfg.RecallCentered)

	cleanup := func() { _ = store.Close() }
	return store, cleanup, nil
}

// initRAGEngine creates a RAG engine for CLI use (no auto-indexing or file watcher).
func initRAGEngine(cfg config.Config) (*rag.Engine, error) {
	cfg.RAG.AutoIndex = false
	cfg.RAG.FileWatcher = false

	engine := rag.NewEngine(cfg, nil)
	if engine == nil {
		return nil, fmt.Errorf("failed to initialize RAG engine (check embedding provider configuration)")
	}
	return engine, nil
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

const maxStdinBytes = 100 * 1024 * 1024 // 100 MB

// readStdin reads data from stdin up to maxStdinBytes.
func readStdin() ([]byte, error) {
	return io.ReadAll(io.LimitReader(os.Stdin, maxStdinBytes))
}
