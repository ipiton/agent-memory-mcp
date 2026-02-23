package vectorstore

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test-vec.db")
	store, err := NewSQLiteStore(dbPath, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUpsertAndCount(t *testing.T) {
	store := newTestStore(t)

	chunks := []Chunk{
		{ID: "c1", DocPath: "doc1.md", Content: "hello", Embedding: []float32{1, 0, 0}},
		{ID: "c2", DocPath: "doc1.md", Content: "world", Embedding: []float32{0, 1, 0}},
	}

	if err := store.Upsert(chunks); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if got := store.Count(); got != 2 {
		t.Fatalf("expected 2 chunks, got %d", got)
	}
}

func TestUpsertReplace(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc.md", Content: "original", Embedding: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := store.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc.md", Content: "replaced", Embedding: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}

	if got := store.Count(); got != 1 {
		t.Fatalf("expected 1 after replace, got %d", got)
	}

	results, err := store.Search([]float32{0, 1, 0}, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 || results[0].Content != "replaced" {
		t.Fatal("expected replaced content")
	}
}

func TestDeleteByDocPath(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc1.md", Content: "a", Embedding: []float32{1, 0, 0}},
		{ID: "c2", DocPath: "doc1.md", Content: "b", Embedding: []float32{0, 1, 0}},
		{ID: "c3", DocPath: "doc2.md", Content: "c", Embedding: []float32{0, 0, 1}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := store.DeleteByDocPath("doc1.md"); err != nil {
		t.Fatalf("DeleteByDocPath: %v", err)
	}

	if got := store.Count(); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
}

func TestSearchReturnsRanked(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc1.md", Content: "exact match", Embedding: []float32{1, 0, 0}},
		{ID: "c2", DocPath: "doc2.md", Content: "partial", Embedding: []float32{0.7, 0.7, 0}},
		{ID: "c3", DocPath: "doc3.md", Content: "unrelated", Embedding: []float32{0, 0, 1}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Search([]float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].Content != "exact match" {
		t.Fatalf("expected 'exact match' first, got %s", results[0].Content)
	}

	// Results should be sorted descending by score
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Fatal("results not sorted by score descending")
		}
	}
}

func TestSearchScoreThreshold(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc.md", Content: "orthogonal", Embedding: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Orthogonal vector — cosine similarity = 0
	results, err := store.Search([]float32{0, 1, 0}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Score 0 is below minScore threshold (0.1), so nothing should be returned
	for _, r := range results {
		if r.Score < 0.1 {
			t.Fatalf("got result with score %f below threshold", r.Score)
		}
	}
}

func TestSearchLimit(t *testing.T) {
	store := newTestStore(t)

	chunks := make([]Chunk, 20)
	for i := range chunks {
		chunks[i] = Chunk{
			ID:        "c" + string(rune('a'+i)),
			DocPath:   "doc.md",
			Content:   "content",
			Embedding: []float32{1, float32(i) * 0.01, 0},
		}
	}
	if err := store.Upsert(chunks); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Search([]float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) > 5 {
		t.Fatalf("expected at most 5, got %d", len(results))
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []float32
		expected float64
		epsilon  float64
	}{
		{
			name:     "identical",
			a:        []float32{1, 0, 0},
			b:        []float32{1, 0, 0},
			expected: 1.0,
			epsilon:  1e-9,
		},
		{
			name:     "orthogonal",
			a:        []float32{1, 0, 0},
			b:        []float32{0, 1, 0},
			expected: 0.0,
			epsilon:  1e-9,
		},
		{
			name:     "opposite",
			a:        []float32{1, 0, 0},
			b:        []float32{-1, 0, 0},
			expected: -1.0,
			epsilon:  1e-9,
		},
		{
			name:     "diagonal",
			a:        []float32{1, 1, 0},
			b:        []float32{1, 0, 0},
			expected: 1.0 / math.Sqrt(2),
			epsilon:  1e-6,
		},
		{
			name:     "empty",
			a:        []float32{},
			b:        []float32{},
			expected: 0,
			epsilon:  1e-9,
		},
		{
			name:     "different lengths",
			a:        []float32{1, 0},
			b:        []float32{1, 0, 0},
			expected: 0,
			epsilon:  1e-9,
		},
		{
			name:     "zero vector",
			a:        []float32{0, 0, 0},
			b:        []float32{1, 0, 0},
			expected: 0,
			epsilon:  1e-9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.expected) > tt.epsilon {
				t.Fatalf("CosineSimilarity(%v, %v) = %f, want %f", tt.a, tt.b, got, tt.expected)
			}
		})
	}
}

func TestMetadata(t *testing.T) {
	store := newTestStore(t)

	if err := store.SetMetadata("test_key", "test_value"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	val, err := store.GetMetadata("test_key")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if val != "test_value" {
		t.Fatalf("expected 'test_value', got %q", val)
	}

	// Overwrite
	if err := store.SetMetadata("test_key", "new_value"); err != nil {
		t.Fatalf("SetMetadata overwrite: %v", err)
	}
	val, err = store.GetMetadata("test_key")
	if err != nil {
		t.Fatalf("GetMetadata after overwrite: %v", err)
	}
	if val != "new_value" {
		t.Fatalf("expected 'new_value', got %q", val)
	}

	// Missing key
	val, err = store.GetMetadata("nonexistent")
	if err != nil {
		t.Fatalf("GetMetadata missing key should not error: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty for missing key, got %q", val)
	}
}

func TestIndexedFiles(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	info := &IndexedFileInfo{
		FilePath:   "docs/readme.md",
		Hash:       "abc123",
		ModTime:    now,
		Size:       1024,
		ChunkCount: 5,
	}

	if err := store.SetIndexedFile(info); err != nil {
		t.Fatalf("SetIndexedFile: %v", err)
	}

	got, err := store.GetIndexedFile("docs/readme.md")
	if err != nil {
		t.Fatalf("GetIndexedFile: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.Hash != "abc123" || got.Size != 1024 || got.ChunkCount != 5 {
		t.Fatalf("unexpected values: %+v", got)
	}

	// GetAll
	all, err := store.GetAllIndexedFiles()
	if err != nil {
		t.Fatalf("GetAllIndexedFiles: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1, got %d", len(all))
	}

	// Delete
	if err := store.DeleteIndexedFile("docs/readme.md"); err != nil {
		t.Fatalf("DeleteIndexedFile: %v", err)
	}
	got, err = store.GetIndexedFile("docs/readme.md")
	if err != nil {
		t.Fatalf("GetIndexedFile after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestStoreInterfaceCompliance(t *testing.T) {
	// Verify that SQLiteStore implements the Store interface
	var _ Store = (*SQLiteStore)(nil)
}
