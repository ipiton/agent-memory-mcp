package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

// fakeEmbedder implements embedder.Service without any network or httptest
// server. It is the payoff of Round 3 M23: NewStore accepts the interface, so
// tests inject a deterministic embedding instead of standing up a fake HTTP
// provider (compare TestRecallFallsBackToTextForMismatchedEmbeddingModel).
type fakeEmbedder struct {
	vec     []float32
	modelID string
	calls   int
}

func (f *fakeEmbedder) EmbedDetailed(_ context.Context, _ string) (*embedder.EmbeddingResult, error) {
	f.calls++
	return &embedder.EmbeddingResult{Embedding: f.vec, ModelID: f.modelID}, nil
}

func (f *fakeEmbedder) EmbedQueryDetailed(_ context.Context, _ string) (*embedder.EmbeddingResult, error) {
	return &embedder.EmbeddingResult{Embedding: f.vec, ModelID: f.modelID}, nil
}

func (f *fakeEmbedder) BatchEmbedDetailed(_ context.Context, texts []string) (*embedder.BatchEmbeddingResult, error) {
	embs := make([][]float32, len(texts))
	for i := range embs {
		embs[i] = f.vec
	}
	return &embedder.BatchEmbeddingResult{Embeddings: embs, ModelID: f.modelID}, nil
}

func (f *fakeEmbedder) Dimensions() int { return len(f.vec) }

func (f *fakeEmbedder) Close() {}

func TestNewStoreAcceptsEmbedderServiceFake(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3, 0.4}, modelID: "fake:test:4"}

	store, err := NewStore(filepath.Join(t.TempDir(), "memory.db"), fake, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := &Memory{Content: "deploy rollback runbook", Type: TypeProcedural, Importance: 0.8}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if fake.calls != 1 {
		t.Fatalf("expected fake embedder called once, got %d", fake.calls)
	}
	if m.EmbeddingModel != fake.modelID {
		t.Fatalf("expected embedding model %q, got %q", fake.modelID, m.EmbeddingModel)
	}
	if len(m.Embedding) != len(fake.vec) {
		t.Fatalf("expected embedding of dim %d, got %d", len(fake.vec), len(m.Embedding))
	}
}
