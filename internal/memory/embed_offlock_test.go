package memory

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

// slowEmbedder blocks for a fixed time per call and records how many calls are
// in flight at once.
type slowEmbedder struct {
	delay    time.Duration
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (s *slowEmbedder) EmbedDetailed(_ context.Context, _ string) (*embedder.EmbeddingResult, error) {
	cur := s.inFlight.Add(1)
	for {
		prev := s.peak.Load()
		if cur <= prev || s.peak.CompareAndSwap(prev, cur) {
			break
		}
	}
	time.Sleep(s.delay)
	s.inFlight.Add(-1)
	return &embedder.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}, ModelID: "test-model"}, nil
}

func (s *slowEmbedder) EmbedQueryDetailed(ctx context.Context, text string) (*embedder.EmbeddingResult, error) {
	return s.EmbedDetailed(ctx, text)
}

func (s *slowEmbedder) BatchEmbedDetailed(_ context.Context, texts []string) (*embedder.BatchEmbeddingResult, error) {
	embs := make([][]float32, len(texts))
	for i := range embs {
		embs[i] = []float32{0.1, 0.2, 0.3}
	}
	return &embedder.BatchEmbeddingResult{Embeddings: embs, ModelID: "test-model"}, nil
}

func (s *slowEmbedder) Dimensions() int { return 3 }

func (s *slowEmbedder) Close() {}

func newTestStoreWithEmbedder(t *testing.T, emb embedder.Service) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "memory.db"), emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// T90 M1. The embedding is a provider round-trip. Computing it inside writeMu
// made every concurrent writer queue behind it, so store throughput was bounded
// by embedder latency rather than by the database. Concurrent Store calls must
// now overlap in the embedder.
func TestConcurrentStoresOverlapInTheEmbedder(t *testing.T) {
	emb := &slowEmbedder{delay: 50 * time.Millisecond}
	store := newTestStoreWithEmbedder(t, emb)
	ctx := context.Background()

	const writers = 4
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := &Memory{Content: "concurrent write under a slow embedder", Type: TypeSemantic}
			if err := store.Store(ctx, m); err != nil {
				t.Errorf("Store %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if peak := emb.peak.Load(); peak < 2 {
		t.Fatalf("peak concurrent embed calls = %d, want >1 — the embedding is still serialized by the write lock", peak)
	}
}

// The embedding still lands on the stored row: moving the call out of the lock
// must not drop the vector.
func TestStoreKeepsEmbeddingComputedOffLock(t *testing.T) {
	store := newTestStoreWithEmbedder(t, &slowEmbedder{})

	m := &Memory{Content: "vector must survive the hoist", Type: TypeSemantic}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Embedding) != 3 {
		t.Fatalf("embedding = %v, want the 3-element test vector", got.Embedding)
	}
	if got.EmbeddingModel != "test-model" {
		t.Fatalf("embedding model = %q, want test-model", got.EmbeddingModel)
	}
}

func TestUpdateKeepsEmbeddingComputedOffLock(t *testing.T) {
	store := newTestStoreWithEmbedder(t, &slowEmbedder{})
	ctx := context.Background()

	m := &Memory{Content: "original content", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if err := store.Update(ctx, m.ID, Update{Content: "rewritten content"}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "rewritten content" {
		t.Fatalf("content = %q, want the rewritten value", got.Content)
	}
	if len(got.Embedding) != 3 || got.EmbeddingModel != "test-model" {
		t.Fatalf("embedding = %v / %q, want the re-embedded vector", got.Embedding, got.EmbeddingModel)
	}
}
