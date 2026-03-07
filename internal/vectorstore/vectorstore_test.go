package vectorstore

import (
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestStore(tb testing.TB) *SQLiteStore {
	tb.Helper()
	dbPath := filepath.Join(tb.TempDir(), "test-vec.db")
	store, err := NewSQLiteStore(dbPath, 3, zap.NewNop())
	if err != nil {
		tb.Fatalf("NewSQLiteStore: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
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

func TestAllChunksReturnsSnapshotCopy(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc1.md", Content: "hello", Embedding: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	chunks, err := store.AllChunks()
	if err != nil {
		t.Fatalf("AllChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}

	chunks[0].Content = "mutated"
	chunks[0].Embedding[0] = 99

	snapshot, err := store.AllChunks()
	if err != nil {
		t.Fatalf("AllChunks second call: %v", err)
	}
	if snapshot[0].Content != "hello" {
		t.Fatalf("store content was mutated through snapshot: %q", snapshot[0].Content)
	}
	if snapshot[0].Embedding[0] != 1 {
		t.Fatalf("store embedding was mutated through snapshot: %v", snapshot[0].Embedding)
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

func TestKeywordSearchRanksKeywordMatch(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{
			ID:        "runbook",
			DocPath:   "runbooks/ingress-rollback.md",
			Title:     "Ingress rollback",
			Content:   "Rollback steps for ingress controller recovery",
			Embedding: []float32{0, 1, 0},
		},
		{
			ID:        "generic",
			DocPath:   "docs/networking.md",
			Title:     "Networking notes",
			Content:   "General networking background",
			Embedding: []float32{1, 0, 0},
		},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.KeywordSearch("rollback ingress", 10)
	if err != nil {
		t.Fatalf("KeywordSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected keyword results")
	}
	if results[0].ID != "runbook" {
		t.Fatalf("results[0].ID = %q, want runbook", results[0].ID)
	}
}

func TestKeywordSearchReflectsDeleteByDocPath(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{
			ID:        "runbook",
			DocPath:   "runbooks/ingress-rollback.md",
			Title:     "Ingress rollback",
			Content:   "Rollback steps for ingress controller recovery",
			Embedding: []float32{0, 1, 0},
		},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := store.DeleteByDocPath("runbooks/ingress-rollback.md"); err != nil {
		t.Fatalf("DeleteByDocPath: %v", err)
	}

	results, err := store.KeywordSearch("rollback ingress", 10)
	if err != nil {
		t.Fatalf("KeywordSearch: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 keyword results after delete, got %d", len(results))
	}
}

func BenchmarkKeywordSearch(b *testing.B) {
	store := newTestStore(b)

	chunks := make([]Chunk, 0, 3000)
	for i := 0; i < 3000; i++ {
		content := "General platform notes for release pipelines and deployments"
		title := "Platform note"
		path := "docs/note-" + strconv.Itoa(i) + ".md"
		if i%120 == 0 {
			content = "Rollback steps for ingress controller recovery and troubleshooting"
			title = "Ingress rollback"
			path = "runbooks/ingress-rollback-" + strconv.Itoa(i) + ".md"
		}
		chunks = append(chunks, Chunk{
			ID:        "chunk-" + strconv.Itoa(i),
			DocPath:   path,
			Title:     title,
			Content:   content,
			Embedding: []float32{1, 0, 0},
		})
	}

	if err := store.Upsert(chunks); err != nil {
		b.Fatalf("Upsert: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := store.KeywordSearch("rollback ingress", 20)
		if err != nil {
			b.Fatalf("KeywordSearch: %v", err)
		}
		if len(results) == 0 {
			b.Fatal("expected results")
		}
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
	if !errors.Is(err, ErrMetadataNotFound) {
		t.Fatalf("GetMetadata missing key: expected ErrMetadataNotFound, got %v", err)
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

func TestCommitIndexState(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	if err := store.CommitIndexState(IndexStateUpdate{
		Metadata: map[string]string{
			"index_state":  "ready",
			"last_indexed": now.Format(time.RFC3339),
		},
		UpsertFiles: []*IndexedFileInfo{
			{
				FilePath:   "docs/runbook.md",
				Hash:       "hash-1",
				ModTime:    now,
				Size:       512,
				ChunkCount: 2,
			},
		},
	}); err != nil {
		t.Fatalf("CommitIndexState: %v", err)
	}

	state, err := store.GetMetadata("index_state")
	if err != nil {
		t.Fatalf("GetMetadata(index_state): %v", err)
	}
	if state != "ready" {
		t.Fatalf("index_state = %q, want ready", state)
	}

	info, err := store.GetIndexedFile("docs/runbook.md")
	if err != nil {
		t.Fatalf("GetIndexedFile: %v", err)
	}
	if info == nil || info.Hash != "hash-1" || info.ChunkCount != 2 {
		t.Fatalf("unexpected indexed file info: %+v", info)
	}
}

func TestCommitIndexStateRollsBackOnError(t *testing.T) {
	store := newTestStore(t)

	if err := store.SetMetadata("index_state", "ready"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	if err := store.SetIndexedFile(&IndexedFileInfo{
		FilePath:   "docs/original.md",
		Hash:       "stable-hash",
		ModTime:    time.Now().UTC().Truncate(time.Second),
		Size:       128,
		ChunkCount: 1,
	}); err != nil {
		t.Fatalf("SetIndexedFile: %v", err)
	}

	err := store.CommitIndexState(IndexStateUpdate{
		Metadata: map[string]string{
			"index_state": "dirty",
		},
		UpsertFiles: []*IndexedFileInfo{nil},
		DeleteFilePaths: []string{
			"docs/original.md",
		},
	})
	if err == nil {
		t.Fatal("CommitIndexState error = nil, want rollback")
	}

	state, err := store.GetMetadata("index_state")
	if err != nil {
		t.Fatalf("GetMetadata(index_state): %v", err)
	}
	if state != "ready" {
		t.Fatalf("index_state after rollback = %q, want ready", state)
	}

	info, err := store.GetIndexedFile("docs/original.md")
	if err != nil {
		t.Fatalf("GetIndexedFile after rollback: %v", err)
	}
	if info == nil || info.Hash != "stable-hash" {
		t.Fatalf("indexed file after rollback = %+v, want original entry", info)
	}
}

func TestStoreInterfaceCompliance(t *testing.T) {
	// Verify that SQLiteStore implements the Store interface
	var _ Store = (*SQLiteStore)(nil)
}
