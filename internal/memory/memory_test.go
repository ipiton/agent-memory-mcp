package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCopyMemory(t *testing.T) {
	orig := &Memory{
		ID:        "test-id",
		Content:   "hello",
		Tags:      []string{"a", "b"},
		Metadata:  map[string]string{"k1": "v1", "k2": "v2"},
		Embedding: []float32{0.1, 0.2, 0.3},
	}

	c := copyMemory(orig)

	// Must be a different pointer
	if c == orig {
		t.Fatal("copyMemory returned same pointer")
	}

	// Values must be equal
	if c.ID != orig.ID || c.Content != orig.Content {
		t.Fatal("scalar fields differ")
	}

	// Mutate the copy — original must not change
	c.Tags[0] = "CHANGED"
	if orig.Tags[0] == "CHANGED" {
		t.Fatal("Tags slice is shared — not a deep copy")
	}

	c.Metadata["k1"] = "CHANGED"
	if orig.Metadata["k1"] == "CHANGED" {
		t.Fatal("Metadata map is shared — not a deep copy")
	}

	c.Embedding[0] = 99.9
	if orig.Embedding[0] == 99.9 {
		t.Fatal("Embedding slice is shared — not a deep copy")
	}
}

func TestCopyMemoryNilSlices(t *testing.T) {
	orig := &Memory{
		ID:      "empty",
		Content: "no tags or metadata",
	}
	c := copyMemory(orig)
	if c == orig {
		t.Fatal("same pointer")
	}
	if c.Tags != nil || c.Metadata != nil || c.Embedding != nil {
		t.Fatal("nil slices/maps should remain nil")
	}
}

func TestGetReturnsCopy(t *testing.T) {
	store := newTestStore(t)

	m := &Memory{
		Content:  "test content",
		Type:     TypeSemantic,
		Tags:     []string{"tag1"},
		Metadata: map[string]string{"key": "val"},
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Mutate returned value
	got.Tags[0] = "MUTATED"
	got.Content = "MUTATED"

	// Original in cache must be unchanged
	got2, _ := store.Get(m.ID)
	if got2.Tags[0] == "MUTATED" {
		t.Fatal("Get returned shared Tags slice")
	}
	if got2.Content == "MUTATED" {
		t.Fatal("Get returned shared Memory struct")
	}
}

func TestStoreCopiesCallerOwnedFields(t *testing.T) {
	store := newTestStore(t)

	input := &Memory{
		Content:   "caller owned fields",
		Type:      TypeSemantic,
		Tags:      []string{"tag1"},
		Metadata:  map[string]string{"owner": "platform"},
		Embedding: []float32{0.1, 0.2},
	}
	if err := store.Store(context.Background(), input); err != nil {
		t.Fatalf("Store: %v", err)
	}

	input.Tags[0] = "mutated"
	input.Metadata["owner"] = "mutated"
	input.Embedding[0] = 9.9

	got, err := store.Get(input.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Tags[0] != "tag1" {
		t.Fatalf("stored tag = %q, want tag1", got.Tags[0])
	}
	if got.Metadata["owner"] != "platform" {
		t.Fatalf("stored metadata owner = %q, want platform", got.Metadata["owner"])
	}
	if got.Embedding[0] != 0.1 {
		t.Fatalf("stored embedding[0] = %f, want 0.1", got.Embedding[0])
	}
}

func TestListReturnsCopies(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{Content: "one", Type: TypeSemantic, Tags: []string{"t1"}}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.Store(context.Background(), &Memory{Content: "two", Type: TypeSemantic, Tags: []string{"t2"}}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	list, err := store.List(context.Background(), Filters{}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(list))
	}

	// Mutate returned list items
	for _, m := range list {
		m.Tags[0] = "MUTATED"
	}

	// Verify cache is untouched
	list2, _ := store.List(context.Background(), Filters{}, 0)
	for _, m := range list2 {
		if m.Tags[0] == "MUTATED" {
			t.Fatal("List returned shared slice")
		}
	}
}

func TestExportAllReturnsCopies(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{Content: "mem", Type: TypeEpisodic, Tags: []string{"x"}}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	all, err := store.ExportAll(context.Background())
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1, got %d", len(all))
	}

	all[0].Tags[0] = "MUTATED"

	all2, _ := store.ExportAll(context.Background())
	if all2[0].Tags[0] == "MUTATED" {
		t.Fatal("ExportAll returned shared slice")
	}
}

func TestRecallTextSearch(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{
		Content:    "golang concurrency patterns with goroutines",
		Type:       TypeProcedural,
		Title:      "Go concurrency",
		Importance: 0.8,
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.Store(context.Background(), &Memory{
		Content:    "python decorators and metaclasses",
		Type:       TypeSemantic,
		Title:      "Python patterns",
		Importance: 0.5,
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	results, err := store.Recall(context.Background(), "golang goroutines", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].Memory.Title != "Go concurrency" {
		t.Fatalf("expected Go concurrency first, got %s", results[0].Memory.Title)
	}
}

func TestStorePreservesExplicitZeroImportance(t *testing.T) {
	store := newTestStore(t)

	m := &Memory{
		Content:    "zero importance remains explicit",
		Type:       TypeSemantic,
		Importance: 0,
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Importance != 0 {
		t.Fatalf("Importance = %f, want 0", got.Importance)
	}
}

func TestRecallScoreThreshold(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{
		Content:    "abcdefg unique content",
		Type:       TypeSemantic,
		Importance: 0.01, // very low importance
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Query with totally unrelated content — text match score should be 0
	results, err := store.Recall(context.Background(), "zzzzzzz completely different", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	// With minScore threshold, irrelevant results should be filtered out
	if len(results) != 0 {
		t.Fatalf("expected 0 results (below threshold), got %d with score %f", len(results), results[0].Score)
	}
}

func TestRecallReturnsCopies(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{
		Content:    "test recall copy safety",
		Type:       TypeSemantic,
		Title:      "Recall Copy Test",
		Importance: 0.9,
		Tags:       []string{"copy"},
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	results, err := store.Recall(context.Background(), "recall copy", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}

	// Mutate returned result
	results[0].Memory.Tags[0] = "MUTATED"

	// Verify cache is not affected
	got, _ := store.Get(results[0].Memory.ID)
	if got.Tags[0] == "MUTATED" {
		t.Fatal("Recall returned shared Memory pointer")
	}
}

func TestRecallWithFilters(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{
		Content:    "important golang design",
		Type:       TypeProcedural,
		Importance: 0.9,
		Tags:       []string{"go"},
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.Store(context.Background(), &Memory{
		Content:    "important python design",
		Type:       TypeSemantic,
		Importance: 0.9,
		Tags:       []string{"python"},
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Filter by type
	results, err := store.Recall(context.Background(), "design", Filters{Type: TypeProcedural}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, r := range results {
		if r.Memory.Type != TypeProcedural {
			t.Fatalf("expected only procedural, got %s", r.Memory.Type)
		}
	}

	// Filter by tag
	results, err = store.Recall(context.Background(), "design", Filters{Tags: []string{"python"}}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, r := range results {
		found := false
		for _, tag := range r.Memory.Tags {
			if tag == "python" {
				found = true
			}
		}
		if !found {
			t.Fatal("expected only results with python tag")
		}
	}
}

func TestConcurrentGetRecall(t *testing.T) {
	store := newTestStore(t)

	for i := 0; i < 10; i++ {
		if err := store.Store(context.Background(), &Memory{
			Content:    "concurrent memory test item",
			Type:       TypeSemantic,
			Importance: 0.5,
			Tags:       []string{"concurrent"},
		}); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)

	// Concurrent Get
	list, _ := store.List(context.Background(), Filters{}, 0)
	for _, m := range list {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			got, err := store.Get(id)
			if err != nil {
				errs <- err
				return
			}
			// Mutate the copy — should be safe
			got.Tags = append(got.Tags, "mutated")
		}(m.ID)
	}

	// Concurrent Recall
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := store.Recall(context.Background(), "concurrent", Filters{}, 5)
			if err != nil {
				errs <- err
				return
			}
			for _, r := range results {
				r.Memory.Content = "mutated"
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent error: %v", err)
	}
}

func TestStoreAndDelete(t *testing.T) {
	store := newTestStore(t)

	m := &Memory{Content: "to delete", Type: TypeWorking}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if store.Count() != 1 {
		t.Fatalf("expected 1, got %d", store.Count())
	}

	if err := store.Delete(context.Background(), m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if store.Count() != 0 {
		t.Fatalf("expected 0, got %d", store.Count())
	}

	_, err := store.Get(m.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestUpdateMemory(t *testing.T) {
	store := newTestStore(t)

	m := &Memory{Content: "original", Type: TypeSemantic, Tags: []string{"v1"}}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	imp := 0.9
	if err := store.Update(context.Background(), m.ID, Update{
		Content:    "updated",
		Title:      "New Title",
		Tags:       []string{"v2"},
		Importance: &imp,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.Get(m.ID)
	if got.Content != "updated" {
		t.Fatalf("expected updated content, got %s", got.Content)
	}
	if got.Title != "New Title" {
		t.Fatalf("expected New Title, got %s", got.Title)
	}
	if got.Importance != 0.9 {
		t.Fatalf("expected importance 0.9, got %f", got.Importance)
	}
}

func TestCountByType(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{Content: "a", Type: TypeSemantic}); err != nil {
		t.Fatalf("Store a: %v", err)
	}
	if err := store.Store(context.Background(), &Memory{Content: "b", Type: TypeSemantic}); err != nil {
		t.Fatalf("Store b: %v", err)
	}
	if err := store.Store(context.Background(), &Memory{Content: "c", Type: TypeEpisodic}); err != nil {
		t.Fatalf("Store c: %v", err)
	}

	counts := store.CountByType()
	if counts[TypeSemantic] != 2 {
		t.Fatalf("expected 2 semantic, got %d", counts[TypeSemantic])
	}
	if counts[TypeEpisodic] != 1 {
		t.Fatalf("expected 1 episodic, got %d", counts[TypeEpisodic])
	}
}

func TestListWithSinceFilter(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{Content: "old", Type: TypeSemantic}); err != nil {
		t.Fatalf("Store old: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(10 * time.Millisecond)
	if err := store.Store(context.Background(), &Memory{Content: "new", Type: TypeSemantic}); err != nil {
		t.Fatalf("Store new: %v", err)
	}

	list, err := store.List(context.Background(), Filters{Since: cutoff}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}
	if list[0].Content != "new" {
		t.Fatalf("expected 'new', got %s", list[0].Content)
	}
}

func TestRecallFallsBackToTextForMismatchedEmbeddingModel(t *testing.T) {
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

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Store(context.Background(), &Memory{
		Content:        "completely unrelated content",
		Type:           TypeSemantic,
		Importance:     1.0,
		Embedding:      []float32{1, 0, 0, 0},
		EmbeddingModel: "other-provider:model:4",
	}); err != nil {
		t.Fatalf("Store mismatched: %v", err)
	}
	if err := store.Store(context.Background(), &Memory{
		Content:    "deploy rollback guide and checklist",
		Type:       TypeProcedural,
		Importance: 0.8,
	}); err != nil {
		t.Fatalf("Store text match: %v", err)
	}

	results, err := store.Recall(context.Background(), "deploy rollback", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].Memory.Content != "deploy rollback guide and checklist" {
		t.Fatalf("expected text-matching memory first, got %q", results[0].Memory.Content)
	}
}

func TestRecallDoesNotBlockStoreDuringQueryEmbedding(t *testing.T) {
	var started atomic.Bool
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		started.Store(true)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{1, 0, 0, 0}},
			},
		})
	}))
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

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Store(context.Background(), &Memory{
		Content:        "existing memory",
		Type:           TypeSemantic,
		Embedding:      []float32{1, 0, 0, 0},
		EmbeddingModel: "legacy:model:4",
	}); err != nil {
		t.Fatalf("Store seed: %v", err)
	}

	recallDone := make(chan error, 1)
	go func() {
		_, err := store.Recall(context.Background(), "existing", Filters{}, 10)
		recallDone <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for recall embedding request to start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	storeDone := make(chan error, 1)
	go func() {
		storeDone <- store.Store(context.Background(), &Memory{
			Content:        "new memory while recall is embedding",
			Type:           TypeSemantic,
			Embedding:      []float32{0, 1, 0, 0},
			EmbeddingModel: "manual:test:4",
		})
	}()

	select {
	case err := <-storeDone:
		if err != nil {
			t.Fatalf("Store during recall: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Store blocked while recall query embedding was in progress")
	}

	close(release)

	select {
	case err := <-recallDone:
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recall did not finish after releasing embedding response")
	}
}

func TestRecallUsesTrustMetadataForRanking(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(context.Background(), &Memory{
		Title:      "Accepted scaling decision",
		Content:    "disable hpa for api during migration",
		Type:       TypeSemantic,
		Importance: 0.7,
		Metadata: map[string]string{
			"entity":           "decision",
			"status":           "accepted",
			"owner":            "platform",
			"last_verified_at": time.Now().UTC().Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("Store accepted: %v", err)
	}
	if err := store.Store(context.Background(), &Memory{
		Title:      "Draft working note",
		Content:    "disable hpa for api during migration",
		Type:       TypeWorking,
		Importance: 0.7,
		Metadata: map[string]string{
			"status":           "draft",
			"last_verified_at": time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("Store draft: %v", err)
	}

	results, err := store.Recall(context.Background(), "disable hpa migration", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	if results[0].Memory.Title != "Accepted scaling decision" {
		t.Fatalf("top result = %q, want accepted decision", results[0].Memory.Title)
	}
	if results[0].Trust == nil {
		t.Fatal("expected trust metadata on top result")
	}
	if results[0].Trust.SourceType != "decision" {
		t.Fatalf("SourceType = %q, want decision", results[0].Trust.SourceType)
	}
	if results[0].Trust.Owner != "platform" {
		t.Fatalf("Owner = %q, want platform", results[0].Trust.Owner)
	}
	if results[0].Trust.Confidence <= results[1].Trust.Confidence {
		t.Fatalf("expected top result confidence %.2f to exceed second %.2f", results[0].Trust.Confidence, results[1].Trust.Confidence)
	}
}

func TestPromoteToCanonicalBoostsTrustRanking(t *testing.T) {
	store := newTestStore(t)

	canonical := &Memory{
		Title:      "Ingress rollback canonical",
		Content:    "rollback ingress controller deployment",
		Type:       TypeProcedural,
		Importance: 0.7,
		Metadata:   map[string]string{"entity": "runbook"},
	}
	raw := &Memory{
		Title:      "Ingress rollback raw",
		Content:    "rollback ingress controller deployment",
		Type:       TypeProcedural,
		Importance: 0.7,
		Metadata:   map[string]string{"entity": "runbook", "status": "draft"},
	}
	if err := store.Store(context.Background(), canonical); err != nil {
		t.Fatalf("Store canonical candidate: %v", err)
	}
	if err := store.Store(context.Background(), raw); err != nil {
		t.Fatalf("Store raw: %v", err)
	}
	if _, err := store.PromoteToCanonical(context.Background(), canonical.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	results, err := store.Recall(context.Background(), "rollback ingress controller", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	if results[0].Memory.ID != canonical.ID {
		t.Fatalf("top result = %s, want %s", results[0].Memory.ID, canonical.ID)
	}
	if results[0].Trust == nil || results[0].Trust.Confidence <= results[1].Trust.Confidence {
		t.Fatalf("expected canonical entry to have higher confidence: %#v vs %#v", results[0].Trust, results[1].Trust)
	}
	if results[0].Trust.KnowledgeLayer != "canonical" {
		t.Fatalf("KnowledgeLayer = %q, want canonical", results[0].Trust.KnowledgeLayer)
	}
}

func TestListAndRecallCanonicalKnowledge(t *testing.T) {
	store := newTestStore(t)

	canonical := &Memory{
		Title:      "Canonical ingress rollback",
		Content:    "rollback ingress deployment and verify endpoints",
		Type:       TypeProcedural,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"runbook", "service:api"},
		Metadata: map[string]string{
			"entity":  "runbook",
			"service": "api",
		},
	}
	raw := &Memory{
		Title:      "Raw ingress note",
		Content:    "rollback ingress deployment and verify endpoints",
		Type:       TypeProcedural,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"runbook", "service:api"},
		Metadata: map[string]string{
			"entity":  "runbook",
			"service": "api",
		},
	}
	if err := store.Store(context.Background(), canonical); err != nil {
		t.Fatalf("Store canonical: %v", err)
	}
	if err := store.Store(context.Background(), raw); err != nil {
		t.Fatalf("Store raw: %v", err)
	}
	if _, err := store.PromoteToCanonical(context.Background(), canonical.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	listed, err := store.ListCanonical(context.Background(), Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("ListCanonical: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("len(listed) = %d, want 1", len(listed))
	}
	if listed[0].SourceMemoryID != canonical.ID {
		t.Fatalf("SourceMemoryID = %q, want %q", listed[0].SourceMemoryID, canonical.ID)
	}
	if listed[0].Trust == nil || listed[0].Trust.KnowledgeLayer != "canonical" {
		t.Fatalf("unexpected trust on canonical entry: %#v", listed[0].Trust)
	}

	recalled, err := store.RecallCanonical(context.Background(), "rollback ingress deployment", Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("RecallCanonical: %v", err)
	}
	if len(recalled) != 1 {
		t.Fatalf("len(recalled) = %d, want 1", len(recalled))
	}
	if recalled[0].Entry.ID != canonical.ID {
		t.Fatalf("Entry.ID = %q, want %q", recalled[0].Entry.ID, canonical.ID)
	}
}

func TestMergeDuplicatesAndConflictsReport(t *testing.T) {
	store := newTestStore(t)

	primary := &Memory{
		Title:      "Disable HPA",
		Content:    "disable hpa for api during migration",
		Type:       TypeSemantic,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			"entity":  "decision",
			"service": "api",
			"status":  "accepted",
		},
	}
	duplicate := &Memory{
		Title:      "Disable HPA",
		Content:    "disable hpa for api during migration because rollout was unstable",
		Type:       TypeSemantic,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"decision", "service:api", "migration"},
		Metadata: map[string]string{
			"entity":  "decision",
			"service": "api",
			"status":  "accepted",
		},
	}
	if err := store.Store(context.Background(), primary); err != nil {
		t.Fatalf("Store primary: %v", err)
	}
	if err := store.Store(context.Background(), duplicate); err != nil {
		t.Fatalf("Store duplicate: %v", err)
	}

	report, err := store.ConflictsReport(context.Background(), Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("ConflictsReport: %v", err)
	}
	if len(report) != 1 || report[0].Reason != "duplicate_candidates" {
		t.Fatalf("unexpected conflicts report: %#v", report)
	}

	result, err := store.MergeDuplicates(context.Background(), primary.ID, []string{duplicate.ID})
	if err != nil {
		t.Fatalf("MergeDuplicates: %v", err)
	}
	if result.MergedFromCount != 1 {
		t.Fatalf("MergedFromCount = %d, want 1", result.MergedFromCount)
	}

	gotDuplicate, err := store.Get(duplicate.ID)
	if err != nil {
		t.Fatalf("Get duplicate: %v", err)
	}
	if gotDuplicate.Metadata["status"] != "merged" {
		t.Fatalf("duplicate status = %q, want merged", gotDuplicate.Metadata["status"])
	}
	if gotDuplicate.Metadata["merged_into"] != primary.ID {
		t.Fatalf("merged_into = %q, want %q", gotDuplicate.Metadata["merged_into"], primary.ID)
	}

	gotPrimary, err := store.Get(primary.ID)
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	if !containsTag(gotPrimary.Tags, "migration") {
		t.Fatalf("expected merged tags on primary, got %v", gotPrimary.Tags)
	}
	if !strings.Contains(gotPrimary.Content, "Merged note") {
		t.Fatalf("expected merged content on primary, got %q", gotPrimary.Content)
	}

	report, err = store.ConflictsReport(context.Background(), Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("ConflictsReport after merge: %v", err)
	}
	if len(report) != 0 {
		t.Fatalf("expected no conflict groups after merge, got %#v", report)
	}
}

func TestMarkOutdatedDownranksMemory(t *testing.T) {
	store := newTestStore(t)

	current := &Memory{
		Title:      "Current rollback",
		Content:    "rollback ingress deployment",
		Type:       TypeProcedural,
		Importance: 0.8,
		Metadata:   map[string]string{"entity": "runbook", "status": "confirmed"},
	}
	old := &Memory{
		Title:      "Old rollback",
		Content:    "rollback ingress deployment",
		Type:       TypeProcedural,
		Importance: 0.8,
		Metadata:   map[string]string{"entity": "runbook", "status": "confirmed"},
	}
	if err := store.Store(context.Background(), current); err != nil {
		t.Fatalf("Store current: %v", err)
	}
	if err := store.Store(context.Background(), old); err != nil {
		t.Fatalf("Store old: %v", err)
	}
	if _, err := store.MarkOutdated(context.Background(), old.ID, "replaced by newer runbook", current.ID); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}

	results, err := store.Recall(context.Background(), "rollback ingress deployment", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Memory.ID != current.ID {
		t.Fatalf("top result = %s, want %s", results[0].Memory.ID, current.ID)
	}
	outdated, err := store.Get(old.ID)
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if outdated.Metadata["status"] != "superseded" {
		t.Fatalf("status = %q, want superseded", outdated.Metadata["status"])
	}
	if outdated.Metadata["archived"] != "true" {
		t.Fatalf("archived = %q, want true", outdated.Metadata["archived"])
	}
}

func TestReembedAllUpdatesEmbeddingModel(t *testing.T) {
	server := newEmbeddingTestServer(t, []float64{0.5, 0.4, 0.3, 0.2})
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

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := &Memory{
		Content:        "runbook for ingress rollback",
		Type:           TypeProcedural,
		Embedding:      []float32{1, 0, 0, 0},
		EmbeddingModel: "legacy:model:4",
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	result, err := store.ReembedAll(context.Background())
	if err != nil {
		t.Fatalf("ReembedAll: %v", err)
	}
	if result.Reembedded != 1 {
		t.Fatalf("Reembedded = %d, want 1", result.Reembedded)
	}
	if result.CurrentModel == "" {
		t.Fatal("expected CurrentModel to be set")
	}
	if result.ChangedFromByModel["legacy:model:4"] != 1 {
		t.Fatalf("ChangedFromByModel = %#v, want legacy:model:4 -> 1", result.ChangedFromByModel)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.EmbeddingModel != result.CurrentModel {
		t.Fatalf("EmbeddingModel = %q, want %q", got.EmbeddingModel, result.CurrentModel)
	}
}

func TestBackgroundReembedOnModelMismatch(t *testing.T) {
	// Phase 1: Create store with memories embedded by "old-model"
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store1, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore (phase 1): %v", err)
	}
	// Insert memories with a legacy embedding model directly
	for i := 0; i < 3; i++ {
		m := &Memory{
			Content:        "memory for reembed test",
			Type:           TypeSemantic,
			Importance:     0.5,
			Embedding:      []float32{1, 0, 0, 0},
			EmbeddingModel: "legacy:old-model:4",
		}
		if err := store1.Store(context.Background(), m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("Close (phase 1): %v", err)
	}

	// Phase 2: Reopen with a new embedder — should detect mismatch and auto-reembed
	server := newEmbeddingTestServer(t, []float64{0.5, 0.5, 0.5, 0.5})
	defer server.Close()

	emb, err := embedder.New(embedder.Config{
		OpenAIToken:   "test-token",
		OpenAIBaseURL: server.URL,
		OpenAIModel:   "new-model",
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New embedder: %v", err)
	}

	store2, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore (phase 2): %v", err)
	}
	defer func() { _ = store2.Close() }()

	// Close waits for background goroutine (via accessWG), so after Close all reembed is done.
	// But we can also poll for completion.
	deadline := time.After(10 * time.Second)
	for {
		allCurrent := true
		store2.mu.RLock()
		for _, m := range store2.memories {
			if m.EmbeddingModel == "legacy:old-model:4" {
				allCurrent = false
				break
			}
		}
		store2.mu.RUnlock()
		if allCurrent {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for background reembed to complete")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Verify all memories now have the new model
	store2.mu.RLock()
	for id, m := range store2.memories {
		if m.EmbeddingModel == "legacy:old-model:4" {
			t.Errorf("memory %s still has legacy model after reembed", id)
		}
		if strings.Contains(m.EmbeddingModel, "new-model") == false {
			t.Errorf("memory %s has unexpected model %q", id, m.EmbeddingModel)
		}
	}
	store2.mu.RUnlock()
}

func TestNoBackgroundReembedWhenModelsMatch(t *testing.T) {
	server := newEmbeddingTestServer(t, []float64{0.5, 0.5, 0.5, 0.5})
	defer server.Close()

	emb, err := embedder.New(embedder.Config{
		OpenAIToken:   "test-token",
		OpenAIBaseURL: server.URL,
		OpenAIModel:   "current-model",
		Dimension:     4,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("New embedder: %v", err)
	}

	// Phase 1: Store memories with current embedder (model will match)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store1, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore (phase 1): %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := store1.Store(context.Background(), &Memory{
			Content:    "memory content",
			Type:       TypeSemantic,
			Importance: 0.5,
		}); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("Close (phase 1): %v", err)
	}

	// Phase 2: Reopen with same embedder — no reembed should happen
	store2, err := NewStore(dbPath, emb, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore (phase 2): %v", err)
	}
	defer func() { _ = store2.Close() }()

	// All models should already be current — just verify nothing broke
	store2.mu.RLock()
	for id, m := range store2.memories {
		if !strings.Contains(m.EmbeddingModel, "current-model") {
			t.Errorf("memory %s has unexpected model %q", id, m.EmbeddingModel)
		}
	}
	store2.mu.RUnlock()
}

func TestNewStoreMigratesEmbeddingModelColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("initial NewStore: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE memories`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := store.db.Exec(`
		CREATE TABLE memories (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT,
			tags TEXT,
			context TEXT,
			importance REAL DEFAULT 0.5,
			metadata TEXT,
			embedding BLOB,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			accessed_at DATETIME NOT NULL,
			access_count INTEGER DEFAULT 0
		)
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	migrated, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("migrated NewStore: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })

	if err := migrated.Store(context.Background(), &Memory{Content: "post-migration", Type: TypeSemantic}); err != nil {
		t.Fatalf("Store after migration: %v", err)
	}
}

func newEmbeddingTestServer(t *testing.T, embedding []float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatalf("missing Authorization header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": embedding},
			},
		})
	}))
}

// TestConcurrentStress exercises Store, Update, Delete, Recall, List, and Get concurrently
// to detect data races and deadlocks. Run with -race flag.
func TestConcurrentStress(t *testing.T) {
	store := newTestStore(t)

	// Seed initial data.
	for i := 0; i < 20; i++ {
		if err := store.Store(context.Background(), &Memory{
			Content:    "stress test memory content for concurrent access",
			Type:       TypeSemantic,
			Importance: 0.5,
			Tags:       []string{"stress"},
		}); err != nil {
			t.Fatalf("seed Store: %v", err)
		}
	}

	var wg sync.WaitGroup
	var errCount atomic.Int32
	const goroutines = 10
	const opsPerGoroutine = 50

	// Concurrent writers: Store + Update + Delete
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				mem := &Memory{
					Content:    "concurrent write content",
					Type:       TypeEpisodic,
					Importance: 0.3,
					Tags:       []string{"stress", "write"},
				}
				if err := store.Store(context.Background(), mem); err != nil {
					errCount.Add(1)
					continue
				}
				_ = store.Update(context.Background(), mem.ID, Update{Content: "updated content"})
				_ = store.Delete(context.Background(), mem.ID)
			}
		}(g)
	}

	// Concurrent readers: Recall + List + Get
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				results, _ := store.Recall(context.Background(), "stress concurrent", Filters{}, 5)
				for _, r := range results {
					r.Memory.Content = "mutated safely"
				}

				list, _ := store.List(context.Background(), Filters{Tags: []string{"stress"}}, 10)
				for _, m := range list {
					_, _ = store.Get(m.ID)
				}
			}
		}()
	}

	wg.Wait()

	if c := errCount.Load(); c > 0 {
		t.Logf("Non-fatal store errors during stress: %d", c)
	}

	// Verify store is still functional after stress.
	if err := store.Store(context.Background(), &Memory{
		Content:    "post-stress check",
		Type:       TypeSemantic,
		Importance: 0.5,
	}); err != nil {
		t.Fatalf("post-stress Store: %v", err)
	}
	if store.Count() == 0 {
		t.Fatal("store empty after stress test")
	}
}

func containsTag(tags []string, wanted string) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}

func TestStoreCloseNoGoroutineLeak(t *testing.T) {
	t.Helper()
	// Allow background goroutines from the runtime/testing framework to settle.
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const iterations = 5
	for i := 0; i < iterations; i++ {
		dbPath := filepath.Join(t.TempDir(), "leak.db")
		store, err := NewStore(dbPath, nil, zap.NewNop())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		_ = store.Store(context.Background(), &Memory{
			Content: "test content",
			Type:    TypeSemantic,
		})
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	// Give goroutines time to exit.
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	current := runtime.NumGoroutine()
	// Allow a margin of 3 goroutines for runtime jitter.
	if current > baseline+3 {
		t.Errorf("goroutine leak: baseline=%d, current=%d (delta=%d)", baseline, current, current-baseline)
	}
}
