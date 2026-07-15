package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

// TestReviewQueueItemSkipsEmbeddingAndRecall pins the T84 fixes: a review-queue
// pointer record is (C) not embedded on write and (A) never returned from
// semantic recall, even though its content matches the query by text.
func TestReviewQueueItemSkipsEmbeddingAndRecall(t *testing.T) {
	server := newEmbeddingTestServer(t, []float64{1, 0, 0, 0})
	defer server.Close()

	emb, err := embedder.New(embedder.Config{
		OpenAIToken:   "test-token",
		OpenAIBaseURL: server.URL,
		OpenAIModel:   "test-model",
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New embedder: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "review.db")
	store, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()

	reviewItem := &Memory{
		ID:      "review-1",
		Title:   "Review queue / promote rollback runbook",
		Content: "Promotion candidate: memory abc from archived task rollback runbook. Suggested action: promote_to_canonical.",
		Type:    TypeWorking,
		Context: "payments",
		Metadata: map[string]string{
			MetadataRecordKind:     RecordKindReviewQueueItem,
			MetadataReviewRequired: "true",
		},
	}
	if err := store.Store(ctx, reviewItem); err != nil {
		t.Fatalf("Store review item: %v", err)
	}
	// (C) no embedding generated for a pointer record.
	got, err := store.Get("review-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Embedding) != 0 {
		t.Fatalf("review item should not be embedded, got %d dims", len(got.Embedding))
	}

	knowledge := &Memory{
		ID:      "know-1",
		Title:   "Rollback runbook",
		Content: "Promotion candidate rollback runbook: restart api and verify health.",
		Type:    TypeSemantic,
		Context: "payments",
	}
	if err := store.Store(ctx, knowledge); err != nil {
		t.Fatalf("Store knowledge: %v", err)
	}

	// (A) recall must not surface the review pointer even though it matches by text.
	results, err := store.Recall(ctx, "promotion candidate rollback runbook", Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, r := range results {
		if r.Memory.ID == "review-1" {
			t.Fatal("review-queue item leaked into semantic recall")
		}
	}
}
