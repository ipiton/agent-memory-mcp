package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

// sizeLimitedEmbedder refuses inputs above a rune budget, the way llama-server
// refuses an input larger than its physical batch (T120).
type sizeLimitedEmbedder struct {
	maxRunes int
	calls    []int // rune count of every attempt, in order
}

func (e *sizeLimitedEmbedder) EmbedDetailed(_ context.Context, text string) (*embedder.EmbeddingResult, error) {
	runes := len([]rune(text))
	e.calls = append(e.calls, runes)
	if runes > e.maxRunes {
		return nil, errors.New("input is too large to process. increase the physical batch size")
	}
	return &embedder.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}, ModelID: "test-model"}, nil
}

func (e *sizeLimitedEmbedder) BatchEmbedDetailed(ctx context.Context, texts []string) (*embedder.BatchEmbeddingResult, error) {
	out := &embedder.BatchEmbeddingResult{ModelID: "test-model"}
	for _, text := range texts {
		r, err := e.EmbedDetailed(ctx, text)
		if err != nil {
			return nil, err
		}
		out.Embeddings = append(out.Embeddings, r.Embedding)
	}
	return out, nil
}

func (e *sizeLimitedEmbedder) EmbedQueryDetailed(ctx context.Context, text string) (*embedder.EmbeddingResult, error) {
	return e.EmbedDetailed(ctx, text)
}

func (e *sizeLimitedEmbedder) Dimensions() int { return 3 }

func (e *sizeLimitedEmbedder) Close() {}

func newSizeLimitedStore(t *testing.T, maxRunes int) (*Store, *sizeLimitedEmbedder) {
	t.Helper()
	emb := &sizeLimitedEmbedder{maxRunes: maxRunes}
	store, err := NewStore(t.TempDir()+"/memory.db", emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, emb
}

// A body the encoder refuses whole must still get a vector, from its opening,
// and must say so. Before T120 it was stored with no vector at all and reported
// as a success — invisible to semantic recall, and silent about it.
func TestOversizeBodyEmbedsItsOpening(t *testing.T) {
	store, emb := newSizeLimitedStore(t, embedRetryRunes)
	ctx := context.Background()

	m := &Memory{
		Title:   "Session close / огромная сводка",
		Content: strings.Repeat("длинный разбор инцидента. ", 2000), // ≫ embedRetryRunes
		Type:    TypeEpisodic,
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if len(emb.calls) != 2 {
		t.Fatalf("encoder attempts = %v, want two: the whole body then its opening", emb.calls)
	}
	if emb.calls[1] != embedRetryRunes {
		t.Errorf("retry encoded %d runes, want %d", emb.calls[1], embedRetryRunes)
	}

	stored, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Embedding) == 0 {
		t.Fatal("record stored without a vector — it cannot be reached semantically")
	}
	if stored.Metadata[MetadataEmbeddingTruncated] != "true" {
		t.Errorf("metadata %s = %q, want \"true\" — a partial vector must be visible as partial",
			MetadataEmbeddingTruncated, stored.Metadata[MetadataEmbeddingTruncated])
	}
	if got := store.CountTruncatedEmbedding(); got != 1 {
		t.Errorf("CountTruncatedEmbedding() = %d, want 1", got)
	}
	if got := store.CountWithoutEmbedding(); got != 0 {
		t.Errorf("CountWithoutEmbedding() = %d, want 0", got)
	}
}

// When even the opening is refused the record still lands — losing the write
// would be worse — but it must be countable rather than silent.
func TestUnembeddableBodyIsCounted(t *testing.T) {
	store, _ := newSizeLimitedStore(t, 5)
	ctx := context.Background()

	m := &Memory{
		Title:   "Неукладываемая запись",
		Content: strings.Repeat("текст ", 2000),
		Type:    TypeEpisodic,
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if got := store.CountWithoutEmbedding(); got != 1 {
		t.Errorf("CountWithoutEmbedding() = %d, want 1", got)
	}
	if got := store.CountTruncatedEmbedding(); got != 0 {
		t.Errorf("CountTruncatedEmbedding() = %d, want 0 — nothing was embedded at all", got)
	}
}

// A body that fits must not pay for the retry path, and must not be marked.
func TestNormalBodyEmbedsOnceUnmarked(t *testing.T) {
	store, emb := newSizeLimitedStore(t, embedRetryRunes)
	ctx := context.Background()

	m := &Memory{Title: "Обычная запись", Content: "короткое содержательное описание", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if len(emb.calls) != 1 {
		t.Errorf("encoder attempts = %v, want exactly one", emb.calls)
	}
	stored, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, marked := stored.Metadata[MetadataEmbeddingTruncated]; marked {
		t.Error("a fully embedded record was marked as truncated")
	}
}
