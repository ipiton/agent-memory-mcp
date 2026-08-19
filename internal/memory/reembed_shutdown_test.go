package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

// countingEmbeddingServer serves the OpenAI embeddings shape, counts requests
// and optionally stalls on each one. A real HTTP round-trip is what the store
// does in production; a hand-written fake would not have caught either bug
// here, both of which live in how often that call is made.
func countingEmbeddingServer(t *testing.T, delay time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{0.5, 0.4, 0.3, 0.2}}},
		})
	}))
	return srv, &calls
}

func newStoreWithServer(t *testing.T, dbPath string, srv *httptest.Server) *Store {
	t.Helper()
	emb, err := embedder.New(embedder.Config{
		OpenAIToken:   "test-token",
		OpenAIBaseURL: srv.URL,
		OpenAIModel:   "test-model",
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New embedder: %v", err)
	}
	store, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// TestReembedAllSkipsCurrentModelWithoutEmbedderCalls pins the cost of a
// re-embed to the number of stale rows. Before the model was probed up front,
// every row was embedded and only then compared, so one stale row among a
// thousand current ones still cost a thousand calls.
func TestReembedAllSkipsCurrentModelWithoutEmbedderCalls(t *testing.T) {
	srv, calls := countingEmbeddingServer(t, 0)
	defer srv.Close()

	store := newStoreWithServer(t, filepath.Join(t.TempDir(), "test.db"), srv)
	t.Cleanup(func() { _ = store.Close() })

	for i := 0; i < 5; i++ {
		if err := store.Store(context.Background(), &Memory{
			Content: fmt.Sprintf("current memory %d with enough content to store", i),
			Type:    TypeSemantic,
		}); err != nil {
			t.Fatalf("Store current %d: %v", i, err)
		}
	}
	if err := store.Store(context.Background(), &Memory{
		Content:        "stale memory carrying a retired model id",
		Type:           TypeProcedural,
		Embedding:      []float32{1, 0, 0, 0},
		EmbeddingModel: "legacy:model:4",
	}); err != nil {
		t.Fatalf("Store stale: %v", err)
	}

	calls.Store(0)
	result, err := store.ReembedAll(context.Background())
	if err != nil {
		t.Fatalf("ReembedAll: %v", err)
	}

	if result.Reembedded != 1 {
		t.Errorf("Reembedded = %d, want 1", result.Reembedded)
	}
	if result.AlreadyCurrent != 5 {
		t.Errorf("AlreadyCurrent = %d, want 5", result.AlreadyCurrent)
	}
	// One probe plus one stale row. The old code made six.
	if got := calls.Load(); got != 2 {
		t.Errorf("embedder calls = %d, want 2 (probe + one stale row)", got)
	}
}

// TestCloseCancelsStartupReembed keeps a short-lived process short-lived. The
// SessionEnd hook died on exactly this: Close waits on the worker group, the
// startup re-embed walks the whole bank, and Claude Code killed the hook at its
// 60s timeout before the walk finished.
func TestCloseCancelsStartupReembed(t *testing.T) {
	srv, calls := countingEmbeddingServer(t, 200*time.Millisecond)
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	seed := newStoreWithServer(t, dbPath, srv)
	for i := 0; i < 30; i++ {
		if err := seed.Store(context.Background(), &Memory{
			Content:        fmt.Sprintf("stale memory %d awaiting a re-embed", i),
			Type:           TypeSemantic,
			Embedding:      []float32{1, 0, 0, 0},
			EmbeddingModel: "legacy:model:4",
		}); err != nil {
			t.Fatalf("Store seed %d: %v", i, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("Close seed: %v", err)
	}

	// Reopening detects the model mismatch and starts the background walk:
	// 30 rows at 200ms is at least 6s of work.
	store := newStoreWithServer(t, dbPath, srv)
	calls.Store(0)

	start := time.Now()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("Close took %v, want under 2s — the startup re-embed was not cancelled", elapsed)
	}
	if got := calls.Load(); got >= 30 {
		t.Errorf("embedder calls during shutdown = %d, want well under 30 — the walk ran to completion", got)
	}
}
