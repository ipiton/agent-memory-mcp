package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

// newSupersessionTestStore builds a store over a stub embedder that returns the
// same vector for every input — recall then ranks purely on the metadata
// weights, so the assertions below are about the filter, not about similarity.
func newSupersessionTestStore(t *testing.T) *Store {
	t.Helper()

	server := newEmbeddingTestServer(t, []float64{1, 0, 0, 0})
	t.Cleanup(server.Close)

	emb, err := embedder.New(embedder.Config{
		OpenAIToken:   "test-token",
		OpenAIBaseURL: server.URL,
		OpenAIModel:   "test-model",
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New embedder: %v", err)
	}

	store, err := NewStore(filepath.Join(t.TempDir(), "superseded.db"), emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}

func storeSupersessionPair(t *testing.T, store *Store) {
	t.Helper()

	ctx := context.Background()

	old := &Memory{
		ID:      "old-1",
		Title:   "Deploy runbook",
		Content: "Deploy runbook: scale the api deployment to three replicas.",
		Type:    TypeSemantic,
		Context: "payments",
	}
	successor := &Memory{
		ID:      "new-1",
		Title:   "Deploy runbook",
		Content: "Deploy runbook: scale the api deployment to five replicas.",
		Type:    TypeSemantic,
		Context: "payments",
	}
	for _, m := range []*Memory{old, successor} {
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store %s: %v", m.ID, err)
		}
	}

	if err := store.SetTemporalFields(ctx, old.ID, nil, nil, successor.ID, ""); err != nil {
		t.Fatalf("SetTemporalFields: %v", err)
	}
}

func recalledIDs(t *testing.T, store *Store) map[string]bool {
	t.Helper()

	results, err := store.Recall(context.Background(), "deploy runbook replicas", Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	ids := make(map[string]bool, len(results))
	for _, r := range results {
		ids[r.Memory.ID] = true
	}
	return ids
}

// TestSupersededMemoryExcludedFromRecall pins issue #18: an entry whose
// superseded_by points at a live successor stays out of semantic recall (its
// vector is unchanged, so it kept out-ranking the successor), while remaining
// visible to the List-based maintenance views.
func TestSupersededMemoryExcludedFromRecall(t *testing.T) {
	store := newSupersessionTestStore(t)
	storeSupersessionPair(t, store)

	ids := recalledIDs(t, store)
	if ids["old-1"] {
		t.Error("superseded entry leaked into semantic recall")
	}
	if !ids["new-1"] {
		t.Error("successor missing from recall")
	}

	listed, err := store.List(context.Background(), Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, m := range listed {
		if m.ID == "old-1" {
			found = true
		}
	}
	if !found {
		t.Error("superseded entry must stay visible to List for temporal history")
	}

	lightweight := store.ListLightweight(Filters{Context: "payments"})
	found = false
	for _, m := range lightweight {
		if m.ID == "old-1" {
			found = true
		}
	}
	if !found {
		t.Error("superseded entry must stay visible to ListLightweight")
	}
}

// TestOutdatedWithoutSuccessorStaysRecallable guards the boundary of the new
// filter: MarkOutdated with an empty supersededBy leaves superseded_by unset,
// so the entry keeps the pre-existing downrank treatment (lower importance,
// archived metadata) and stays in recall. Nothing replaced it — hiding it would
// simply lose the knowledge.
func TestOutdatedWithoutSuccessorStaysRecallable(t *testing.T) {
	store := newSupersessionTestStore(t)
	storeSupersessionPair(t, store)

	if _, err := store.MarkOutdated(context.Background(), "new-1", "just stale", ""); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}

	if !recalledIDs(t, store)["new-1"] {
		t.Error("outdated entry without a successor must stay in recall")
	}
}

// TestSupersededByDanglingPointerStaysRecallable covers the other side of the
// filter: Delete does not clear superseded_by on predecessors, so once the
// successor is gone the pointer dangles. An archived entry beats no entry at
// all — the old memory must come back into recall rather than stay buried.
func TestSupersededByDanglingPointerStaysRecallable(t *testing.T) {
	store := newSupersessionTestStore(t)
	storeSupersessionPair(t, store)

	if err := store.Delete(context.Background(), "new-1"); err != nil {
		t.Fatalf("Delete successor: %v", err)
	}

	ids := recalledIDs(t, store)
	if !ids["old-1"] {
		t.Error("entry with a dangling superseded_by pointer stayed buried in recall")
	}
}
