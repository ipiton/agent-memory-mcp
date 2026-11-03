package vectorstore

import (
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func keywordIDs(t *testing.T, s *SQLiteStore, query string) []string {
	t.Helper()
	results, err := s.KeywordSearch(query, 10)
	if err != nil {
		t.Fatalf("KeywordSearch(%q): %v", query, err)
	}
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

func keywordRowCounts(t *testing.T, s *SQLiteStore) (chunks, indexed int) {
	t.Helper()
	if err := s.db.QueryRow(keywordCountsQuery).Scan(&chunks, &indexed); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return chunks, indexed
}

func TestKeywordIndexIsBuiltForExistingChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	first, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Upsert([]Chunk{
		{ID: "a", DocPath: "a.md", Content: "rollback ingress safely", Embedding: []float32{1, 0, 0}, LastModified: time.Now()},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// What an index written by a build without FTS looks like.
	if _, err := first.db.Exec(`DELETE FROM ` + ftsTable); err != nil {
		t.Fatalf("clear keyword index: %v", err)
	}
	if _, err := first.db.Exec(`DELETE FROM index_metadata WHERE key = ?`, keywordIndexVersionKey); err != nil {
		t.Fatalf("clear version: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if ids := keywordIDs(t, reopened, "ingress"); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("after the build got %v, want [a]", ids)
	}
	if version, _ := reopened.GetMetadata(keywordIndexVersionKey); version != keywordIndexVersion {
		t.Fatalf("version = %q, want %q", version, keywordIndexVersion)
	}
}

func TestKeywordIndexIsRebuiltWhenCountsDisagree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	first, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Upsert([]Chunk{{ID: "a", DocPath: "a.md", Content: "known term", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// An old binary still running after the upgrade writes to chunks only.
	if _, err := first.db.Exec(`INSERT INTO chunks (id, doc_path, content, embedding) VALUES ('b', 'b.md', 'missed term', x'')`); err != nil {
		t.Fatalf("insert behind the index: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if ids := keywordIDs(t, reopened, "missed"); len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("got %v, want [b]", ids)
	}
}

func TestKeywordSearchStemsRussianOnly(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert([]Chunk{
		{ID: "ru", DocPath: "ru.md", Content: "откатываем развёртывание документов", Embedding: []float32{1, 0, 0}},
		{ID: "en", DocPath: "en.md", Content: "rollbacks of deployments", Embedding: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if ids := keywordIDs(t, s, "документами"); len(ids) != 1 || ids[0] != "ru" {
		t.Errorf("another form of a Russian word: got %v, want [ru]", ids)
	}
	if ids := keywordIDs(t, s, "rollback"); len(ids) != 0 {
		t.Errorf("English stays as written: got %v, want nothing", ids)
	}
}

func TestUpsertAndDeleteKeepKeywordRowsInStep(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert([]Chunk{{ID: "a", DocPath: "a.md", Content: "oldterm here", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.Upsert([]Chunk{{ID: "a", DocPath: "a.md", Content: "newterm here", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if ids := keywordIDs(t, s, "oldterm"); len(ids) != 0 {
		t.Errorf("replaced text still found: %v", ids)
	}
	if ids := keywordIDs(t, s, "newterm"); len(ids) != 1 {
		t.Errorf("new text not found: %v", ids)
	}
	if chunks, indexed := keywordRowCounts(t, s); chunks != 1 || indexed != 1 {
		t.Errorf("rows after replace: chunks=%d indexed=%d, want 1 and 1", chunks, indexed)
	}

	if err := s.DeleteByDocPath("a.md"); err != nil {
		t.Fatalf("DeleteByDocPath: %v", err)
	}
	if chunks, indexed := keywordRowCounts(t, s); chunks != 0 || indexed != 0 {
		t.Errorf("rows after delete: chunks=%d indexed=%d, want 0 and 0", chunks, indexed)
	}
}

func TestCleanOrphansRemovesKeywordRows(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert([]Chunk{{ID: "orphan", DocPath: "gone.md", Content: "stale term", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// The sweep runs only once something is tracked as indexed.
	if err := s.CommitIndexState(IndexStateUpdate{UpsertFiles: []*IndexedFileInfo{{FilePath: "kept.md", Hash: "h", ChunkCount: 1}}}); err != nil {
		t.Fatalf("CommitIndexState: %v", err)
	}
	removed, err := s.CleanOrphans()
	if err != nil {
		t.Fatalf("CleanOrphans: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if ids := keywordIDs(t, s, "stale"); len(ids) != 0 {
		t.Errorf("orphan still found: %v", ids)
	}
}

func TestVectorsLoadOnlyForVectorSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	first, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Upsert([]Chunk{{ID: "a", DocPath: "a.md", Content: "lazy text", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s.Close() }()

	if ids := keywordIDs(t, s, "lazy"); len(ids) != 1 {
		t.Fatalf("keyword search: %v", ids)
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("Count = %d, want 1", got)
	}
	if s.vectorsLoaded || len(s.chunks) != 0 {
		t.Fatalf("vectors in memory before any vector search: loaded=%v n=%d", s.vectorsLoaded, len(s.chunks))
	}

	results, err := s.Search([]float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Content != "lazy text" {
		t.Fatalf("Search = %+v, want the chunk with its text", results)
	}
	if s.chunks["a"].Content != "" {
		t.Errorf("text kept in memory next to the vector")
	}
}

func TestChunkWithoutVector(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert([]Chunk{{ID: "text", DocPath: "t.md", Content: "plain words"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if ids := keywordIDs(t, s, "plain"); len(ids) != 1 {
		t.Errorf("keyword search: %v", ids)
	}
	results, err := s.Search([]float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("a chunk without a vector reached vector search: %+v", results)
	}
	all, err := s.AllChunks()
	if err != nil {
		t.Fatalf("AllChunks: %v", err)
	}
	if len(all) != 1 || len(all[0].Embedding) != 0 {
		t.Errorf("AllChunks = %+v", all)
	}
}

func TestDimensionBindsOnlyWhenVectorsExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	s, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Upsert([]Chunk{{ID: "text", DocPath: "t.md", Content: "plain words"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err = NewSQLiteStore(path, 5, zap.NewNop())
	if err != nil {
		t.Fatalf("a keyword-only index refused a new dimension: %v", err)
	}
	if err := s.Upsert([]Chunk{{ID: "vec", DocPath: "v.md", Content: "with vector", Embedding: []float32{1, 0, 0, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if s, err := NewSQLiteStore(path, 3, zap.NewNop()); err == nil {
		_ = s.Close()
		t.Fatalf("an index with vectors accepted another dimension")
	}
}

func TestKeywordSearchSeesAnotherProcessWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	reader, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer func() { _ = reader.Close() }()
	writer, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	if err := writer.Upsert([]Chunk{{ID: "a", DocPath: "a.md", Content: "fresh term", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if ids := keywordIDs(t, reader, "fresh"); len(ids) != 1 {
		t.Errorf("reader did not see the write without a refresh: %v", ids)
	}
}
