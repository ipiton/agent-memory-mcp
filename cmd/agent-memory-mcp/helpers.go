package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"go.uber.org/zap"
)

// initMemoryStore creates an embedder and memory store from config.
// Returns store, cleanup function, and error.
func initMemoryStore(cfg config.Config) (*memory.Store, func(), error) {
	if err := os.MkdirAll(filepath.Dir(cfg.MemoryDBPath), 0755); err != nil {
		return nil, nil, fmt.Errorf("failed to create memory directory: %w", err)
	}

	emb, err := embedder.New(cfg.EmbedderConfig(), zap.NewNop())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: embedder unavailable, semantic search disabled: %v\n", err)
		emb = nil
	}

	store, err := memory.NewStore(cfg.MemoryDBPath, emb, zap.NewNop())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open memory store: %w", err)
	}

	cleanup := func() { _ = store.Close() }
	return store, cleanup, nil
}

// initRAGEngine creates a RAG engine for CLI use (no auto-indexing or file watcher).
func initRAGEngine(cfg config.Config) (*rag.Engine, error) {
	cfg.AutoIndex = false
	cfg.FileWatcher = false

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
