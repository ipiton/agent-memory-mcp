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

// Review-queue cards carry no vector by design (T84), so they must not show up
// as records the encoder refused (T189).
func TestReviewQueueCardIsNotCountedAsUnembedded(t *testing.T) {
	store, _ := newSizeLimitedStore(t, 5)
	ctx := context.Background()

	card := &Memory{
		Title:    "Карточка очереди ревью",
		Content:  "new engineering knowledge",
		Type:     TypeEpisodic,
		Metadata: map[string]string{MetadataRecordKind: RecordKindReviewQueueItem},
	}
	refused := &Memory{
		Title:   "Неукладываемая запись",
		Content: strings.Repeat("текст ", 2000),
		Type:    TypeEpisodic,
	}
	for _, m := range []*Memory{card, refused} {
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	stored, err := store.Get(card.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Embedding) != 0 {
		t.Fatal("precondition: review-queue card is expected to be stored without a vector")
	}
	if got := store.CountWithoutEmbedding(); got != 1 {
		t.Errorf("CountWithoutEmbedding() = %d, want 1 — only the refused body counts", got)
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

// ReembedAll must take the same refusal path as a write. It used to call the
// encoder directly: every session summary above the physical batch failed on
// each startup and kept its stale model id, so recall never reached its
// cosine. In the Moving bank that was 142 of 327 records.
func TestReembedAllRetriesOversizeBodyOnItsOpening(t *testing.T) {
	store, emb := newSizeLimitedStore(t, embedRetryRunes)
	ctx := context.Background()

	m := &Memory{
		Title:          "Session close / сводка из другого банка",
		Content:        strings.Repeat("длинный разбор инцидента. ", 2000),
		Type:           TypeEpisodic,
		Embedding:      []float32{1, 0, 0},
		EmbeddingModel: "legacy:model:3",
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	emb.calls = nil

	result, err := store.ReembedAll(ctx)
	if err != nil {
		t.Fatalf("ReembedAll: %v", err)
	}
	if result.Reembedded != 1 || result.Failed != 0 {
		t.Fatalf("Reembedded=%d Failed=%d (%v), want 1/0", result.Reembedded, result.Failed, result.FailedByID)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.EmbeddingModel != "test-model" {
		t.Errorf("EmbeddingModel = %q, want test-model", got.EmbeddingModel)
	}
	if got.Metadata[MetadataEmbeddingTruncated] != "true" {
		t.Errorf("metadata %s = %q, want \"true\"", MetadataEmbeddingTruncated, got.Metadata[MetadataEmbeddingTruncated])
	}
}

// A re-embed that fits whole clears a truncation mark left by an earlier,
// smaller batch: the mark must describe the vector the row carries now.
func TestReembedAllClearsStaleTruncationMark(t *testing.T) {
	store, _ := newSizeLimitedStore(t, embedRetryRunes)
	ctx := context.Background()

	m := &Memory{
		Title:          "Короткая запись",
		Content:        "короткое содержательное описание",
		Type:           TypeSemantic,
		Embedding:      []float32{1, 0, 0},
		EmbeddingModel: "legacy:model:3",
		Metadata:       map[string]string{MetadataEmbeddingTruncated: "true"},
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := store.ReembedAll(ctx); err != nil {
		t.Fatalf("ReembedAll: %v", err)
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.Metadata[MetadataEmbeddingTruncated]; ok {
		t.Errorf("stale %s survived a whole-body re-embed", MetadataEmbeddingTruncated)
	}
}

// A row embedded from its opening already carries the current model id, so the
// startup pass must leave it alone; ReembedTruncated, run after the batch is
// raised, encodes it whole and clears the mark.
func TestReembedTruncatedRevisitsPartialVectors(t *testing.T) {
	store, emb := newSizeLimitedStore(t, embedRetryRunes)
	ctx := context.Background()

	m := &Memory{
		Title:   "Session close / длинная сводка",
		Content: strings.Repeat("длинный разбор инцидента. ", 2000),
		Type:    TypeEpisodic,
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	emb.calls = nil
	result, err := store.ReembedAll(ctx)
	if err != nil {
		t.Fatalf("ReembedAll: %v", err)
	}
	if result.Reembedded != 0 || len(emb.calls) != 1 {
		t.Fatalf("startup pass touched a partial row: Reembedded=%d calls=%v, want 0 and only the probe", result.Reembedded, emb.calls)
	}

	emb.maxRunes = 1 << 20 // the batch was raised
	result, err = store.ReembedTruncated(ctx)
	if err != nil {
		t.Fatalf("ReembedTruncated: %v", err)
	}
	if result.Reembedded != 1 {
		t.Fatalf("Reembedded = %d, want 1", result.Reembedded)
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.Metadata[MetadataEmbeddingTruncated]; ok {
		t.Error("truncation mark survived a whole-body re-embed")
	}
	if got := store.CountTruncatedEmbedding(); got != 0 {
		t.Errorf("CountTruncatedEmbedding() = %d, want 0", got)
	}
}
