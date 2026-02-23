package memory

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestCopyMemory(t *testing.T) {
	orig := &Memory{
		ID:       "test-id",
		Content:  "hello",
		Tags:     []string{"a", "b"},
		Metadata: map[string]string{"k1": "v1", "k2": "v2"},
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
		Content: "test content",
		Type:    TypeSemantic,
		Tags:    []string{"tag1"},
		Metadata: map[string]string{"key": "val"},
	}
	if err := store.Store(m); err != nil {
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

func TestListReturnsCopies(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(&Memory{Content: "one", Type: TypeSemantic, Tags: []string{"t1"}}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.Store(&Memory{Content: "two", Type: TypeSemantic, Tags: []string{"t2"}}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	list, err := store.List(Filters{}, 0)
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
	list2, _ := store.List(Filters{}, 0)
	for _, m := range list2 {
		if m.Tags[0] == "MUTATED" {
			t.Fatal("List returned shared slice")
		}
	}
}

func TestExportAllReturnsCopies(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(&Memory{Content: "mem", Type: TypeEpisodic, Tags: []string{"x"}}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	all, err := store.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1, got %d", len(all))
	}

	all[0].Tags[0] = "MUTATED"

	all2, _ := store.ExportAll()
	if all2[0].Tags[0] == "MUTATED" {
		t.Fatal("ExportAll returned shared slice")
	}
}

func TestRecallTextSearch(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(&Memory{
		Content:    "golang concurrency patterns with goroutines",
		Type:       TypeProcedural,
		Title:      "Go concurrency",
		Importance: 0.8,
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.Store(&Memory{
		Content:    "python decorators and metaclasses",
		Type:       TypeSemantic,
		Title:      "Python patterns",
		Importance: 0.5,
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	results, err := store.Recall("golang goroutines", Filters{}, 10)
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

func TestRecallScoreThreshold(t *testing.T) {
	store := newTestStore(t)

	if err := store.Store(&Memory{
		Content:    "abcdefg unique content",
		Type:       TypeSemantic,
		Importance: 0.01, // very low importance
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Query with totally unrelated content — text match score should be 0
	results, err := store.Recall("zzzzzzz completely different", Filters{}, 10)
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

	if err := store.Store(&Memory{
		Content:    "test recall copy safety",
		Type:       TypeSemantic,
		Title:      "Recall Copy Test",
		Importance: 0.9,
		Tags:       []string{"copy"},
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	results, err := store.Recall("recall copy", Filters{}, 10)
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

	if err := store.Store(&Memory{
		Content:    "important golang design",
		Type:       TypeProcedural,
		Importance: 0.9,
		Tags:       []string{"go"},
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.Store(&Memory{
		Content:    "important python design",
		Type:       TypeSemantic,
		Importance: 0.9,
		Tags:       []string{"python"},
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Filter by type
	results, err := store.Recall("design", Filters{Type: TypeProcedural}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, r := range results {
		if r.Memory.Type != TypeProcedural {
			t.Fatalf("expected only procedural, got %s", r.Memory.Type)
		}
	}

	// Filter by tag
	results, err = store.Recall("design", Filters{Tags: []string{"python"}}, 10)
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
		if err := store.Store(&Memory{
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
	list, _ := store.List(Filters{}, 0)
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
			results, err := store.Recall("concurrent", Filters{}, 5)
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
	if err := store.Store(m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if store.Count() != 1 {
		t.Fatalf("expected 1, got %d", store.Count())
	}

	if err := store.Delete(m.ID); err != nil {
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
	if err := store.Store(m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	imp := 0.9
	if err := store.Update(m.ID, Update{
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

	store.Store(&Memory{Content: "a", Type: TypeSemantic})
	store.Store(&Memory{Content: "b", Type: TypeSemantic})
	store.Store(&Memory{Content: "c", Type: TypeEpisodic})

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

	store.Store(&Memory{Content: "old", Type: TypeSemantic})
	time.Sleep(10 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(10 * time.Millisecond)
	store.Store(&Memory{Content: "new", Type: TypeSemantic})

	list, err := store.List(Filters{Since: cutoff}, 0)
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
