package hooks

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := memory.NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func storeCheckpoint(t *testing.T, store *memory.Store, ctxName, content string) string {
	t.Helper()
	m := &memory.Memory{
		Content: content,
		Type:    memory.TypeEpisodic,
		Context: ctxName,
		Tags:    []string{"session-checkpoint"},
		Metadata: map[string]string{
			memory.MetadataRecordKind:      memory.RecordKindSessionCheckpoint,
			memory.MetadataSessionBoundary: "checkpoint",
			memory.MetadataSessionOrigin:   "hook_checkpoint",
		},
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("store: %v", err)
	}
	return m.ID
}

func newSummary(ctxName, body string) memory.SessionSummary {
	return memory.SessionSummary{
		Context: ctxName,
		Summary: body,
	}
}

func defaultCfg() DedupConfig {
	return DedupConfig{
		Threshold:       0.9,
		MinContentChars: 100,
		Window:          10 * time.Minute,
	}
}

func TestCheck_IdenticalContent_Skip(t *testing.T) {
	store := newTestStore(t)
	content := "Worked on T45 hook dedup filter. Implemented Jaccard similarity across the word token set taken from the content. Added threshold and window configuration."
	id := storeCheckpoint(t, store, "proj-x", content)

	result, err := Check(context.Background(), store, newSummary("proj-x", content), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip {
		t.Fatalf("expected Skip=true for identical content, got %+v", result)
	}
	if result.Reason != ReasonSimilar {
		t.Fatalf("expected reason=similar, got %q", result.Reason)
	}
	if result.SimilarID != id {
		t.Fatalf("expected SimilarID=%s, got %s", id, result.SimilarID)
	}
	if result.Similarity < 0.99 {
		t.Fatalf("expected similarity ~1.0, got %f", result.Similarity)
	}
}

func TestCheck_DifferentContent_NoSkip(t *testing.T) {
	store := newTestStore(t)
	contentA := "Worked on T45 hook dedup filter. Implemented Jaccard similarity across the word token set taken from the content."
	_ = storeCheckpoint(t, store, "proj-x", contentA)

	contentB := "Fixed a performance regression in the RAG indexing pipeline by switching batch sizes and removing redundant embedder probes around startup."
	result, err := Check(context.Background(), store, newSummary("proj-x", contentB), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Skip {
		t.Fatalf("expected Skip=false for different content, got %+v", result)
	}
}

func TestCheck_EmptyContent_SkipEmpty(t *testing.T) {
	store := newTestStore(t)

	result, err := Check(context.Background(), store, newSummary("proj-x", "   \n  "), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonEmpty {
		t.Fatalf("expected Skip=true reason=empty, got %+v", result)
	}
}

func TestCheck_ShortContent_SkipEmpty(t *testing.T) {
	store := newTestStore(t)

	result, err := Check(context.Background(), store, newSummary("proj-x", "tiny summary"), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonEmpty {
		t.Fatalf("expected Skip=true reason=empty for short content, got %+v", result)
	}
}

func TestCheck_OutsideWindow_NoSkip(t *testing.T) {
	store := newTestStore(t)
	content := "Worked on T45 hook dedup filter. Implemented Jaccard similarity across the word token set taken from the content. Added threshold and window configuration."
	_ = storeCheckpoint(t, store, "proj-x", content)

	// Use a tiny window so that even just-created memories fall outside of it.
	cfg := defaultCfg()
	cfg.Window = time.Nanosecond
	time.Sleep(2 * time.Millisecond)

	result, err := Check(context.Background(), store, newSummary("proj-x", content), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Skip {
		t.Fatalf("expected Skip=false when window excludes existing checkpoint, got %+v", result)
	}
}

func TestCheck_ThresholdOne_OnlyExactMatch(t *testing.T) {
	store := newTestStore(t)
	contentA := "Worked on T45 hook dedup filter. Implemented Jaccard similarity across the word token set taken from the content. Added threshold and window configuration."
	id := storeCheckpoint(t, store, "proj-x", contentA)

	cfg := defaultCfg()
	cfg.Threshold = 1.0

	// Slightly different content: should NOT skip at threshold 1.0.
	slightlyDifferent := contentA + " Extra unique tail xyz."
	result, err := Check(context.Background(), store, newSummary("proj-x", slightlyDifferent), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Skip {
		t.Fatalf("expected Skip=false with threshold=1.0 and slightly different content, got %+v", result)
	}

	// Exact same content should still skip.
	result2, err := Check(context.Background(), store, newSummary("proj-x", contentA), cfg)
	if err != nil {
		t.Fatalf("Check exact: %v", err)
	}
	if !result2.Skip || result2.SimilarID != id {
		t.Fatalf("expected exact match to skip, got %+v", result2)
	}
}

func TestCheck_DisabledByZeroThreshold_NoSkip(t *testing.T) {
	store := newTestStore(t)
	content := "Worked on T45 hook dedup filter. Implemented Jaccard similarity across the word token set taken from the content. Added threshold and window configuration."
	_ = storeCheckpoint(t, store, "proj-x", content)

	cfg := defaultCfg()
	cfg.Threshold = 0

	result, err := Check(context.Background(), store, newSummary("proj-x", content), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Skip {
		t.Fatalf("expected Skip=false when cfg disabled (Threshold=0), got %+v", result)
	}
}

func TestCheck_DifferentContext_NoSkip(t *testing.T) {
	store := newTestStore(t)
	content := "Worked on T45 hook dedup filter. Implemented Jaccard similarity across the word token set taken from the content. Added threshold and window configuration."
	_ = storeCheckpoint(t, store, "proj-x", content)

	// Same content, different context — should not be considered a duplicate.
	result, err := Check(context.Background(), store, newSummary("proj-y", content), defaultCfg())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Skip {
		t.Fatalf("expected Skip=false when context differs, got %+v", result)
	}
}

func TestJaccardSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "hello world foo bar", "hello world foo bar", 1.0},
		{"disjoint", "hello world", "red green blue", 0.0},
		{"half overlap", "a b c d", "a b e f", 1.0 / 3.0},
		{"empty both", "", "", 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := JaccardSimilarity(tc.a, tc.b)
			if diff := got - tc.want; diff > 0.001 || diff < -0.001 {
				t.Fatalf("got %f want %f", got, tc.want)
			}
		})
	}
}

// TestJaccardSimilarity_UnicodeRussianPunctuation verifies that the tokenizer
// correctly treats typographic punctuation (em-dash, typographic quotes,
// typographic comma) as separators for non-ASCII (Cyrillic) text. Before the
// unicode-aware tokenizer fix, these separators leaked into tokens and drove
// similarity below 1.0 for otherwise-identical Russian summaries.
func TestJaccardSimilarity_UnicodeRussianPunctuation(t *testing.T) {
	// Glued punctuation (no surrounding whitespace) — under an ASCII-only tokenizer
	// "баг—обновил" would be a single token and the two summaries would have
	// jaccard ≈ 0.14. Under unicode-aware isTokenSeparator they split correctly
	// and jaccard approaches 1.0.
	summary1 := "Исправил баг—обновил конфиг,закоммитил"
	summary2 := "Исправил баг,обновил конфиг—закоммитил"

	got := JaccardSimilarity(summary1, summary2)
	if got < 0.9 {
		t.Fatalf("expected similarity >= 0.9 for Russian text with different punctuation, got %f", got)
	}
}

// TestCheck_DisabledConfig_WhitespaceStillEmpty pins the escape-hatch
// behaviour of dedupConfigFrom when MCP_CHECKPOINT_DEDUP_DISABLED=true:
// Threshold=0 and MinContentChars=0 short-circuit the similarity gate for
// non-whitespace candidates, but whitespace-only summaries are still dropped
// as ReasonEmpty.
func TestCheck_DisabledConfig_WhitespaceStillEmpty(t *testing.T) {
	store := newTestStore(t)

	// Zero-value DedupConfig mirrors what dedupConfigFrom returns when the
	// disable flag is set.
	cfg := DedupConfig{}

	result, err := Check(context.Background(), store, newSummary("proj-x", "   \n\t  "), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip || result.Reason != ReasonEmpty {
		t.Fatalf("expected Skip=true reason=empty for whitespace under disabled cfg, got %+v", result)
	}
}

// TestCheck_MinCharsZero_ShortContentPassesEmptyCheck documents the behaviour
// that a zero-byte (whitespace-only) summary is still skipped with
// reason=empty even when MinContentChars == 0. Rationale: empty content is
// never useful to persist; any tokenless summary would also produce an
// undefined Jaccard score downstream, so we short-circuit here.
func TestCheck_MinCharsZero_ShortContentPassesEmptyCheck(t *testing.T) {
	store := newTestStore(t)

	cfg := DedupConfig{
		Threshold:       0.9,
		MinContentChars: 0,
		Window:          10 * time.Minute,
	}

	result, err := Check(context.Background(), store, newSummary("proj-x", "   \n\t  "), cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Skip {
		t.Fatalf("expected Skip=true for empty content even with MinContentChars=0, got %+v", result)
	}
	if result.Reason != ReasonEmpty {
		t.Fatalf("expected reason=empty, got %q", result.Reason)
	}
}
