package vectorstore

import (
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// The defect these tests pin down: search answers from maps built when the
// store was opened, while the database is written by a separate process (the
// indexing run started by the post-commit hook). A reader that never reloads
// keeps answering from its startup snapshot — the commit is in vectors.db and
// missing from the results, with nothing to say so.

// openSecondReader opens another store over the same file, standing in for the
// long-lived process that did not do the indexing.
func openSecondReader(tb testing.TB, dbPath string) *SQLiteStore {
	tb.Helper()
	store, err := NewSQLiteStore(dbPath, 3, zap.NewNop())
	if err != nil {
		tb.Fatalf("NewSQLiteStore (second reader): %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRefreshIfStalePicksUpAnotherProcessIndexing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")

	writer, err := NewSQLiteStore(dbPath, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore (writer): %v", err)
	}
	defer func() { _ = writer.Close() }()

	if err := writer.Upsert([]Chunk{
		{ID: "c1", DocPath: "old.md", Content: "morning walk", Embedding: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert initial: %v", err)
	}
	if err := writer.SetMetadata(indexVersionKey, "2026-09-10T07:00:00Z"); err != nil {
		t.Fatalf("SetMetadata initial: %v", err)
	}

	reader := openSecondReader(t, dbPath)
	if got := reader.Count(); got != 1 {
		t.Fatalf("reader should open on 1 chunk, got %d", got)
	}

	// The other process indexes a fresh commit and stamps the version.
	if err := writer.Upsert([]Chunk{
		{ID: "c2", DocPath: "new.md", Content: "dbt-test root cause", Embedding: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatalf("Upsert after commit: %v", err)
	}
	if err := writer.SetMetadata(indexVersionKey, "2026-09-10T09:30:00Z"); err != nil {
		t.Fatalf("SetMetadata after commit: %v", err)
	}

	reloaded, err := reader.RefreshIfStale()
	if err != nil {
		t.Fatalf("RefreshIfStale: %v", err)
	}
	if !reloaded {
		t.Fatal("expected a reload after the version moved")
	}
	if got := reader.Count(); got != 2 {
		t.Fatalf("expected the reader to see 2 chunks after reload, got %d", got)
	}

	results, _, err := reader.Search([]float32{0, 1, 0}, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var found bool
	for _, r := range results {
		if r.ID == "c2" {
			found = true
		}
	}
	if !found {
		t.Fatal("the chunk indexed by the other process is missing from search results")
	}
}

func TestRefreshIfStaleIsNoOpWhenVersionUnchanged(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")

	writer, err := NewSQLiteStore(dbPath, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore (writer): %v", err)
	}
	defer func() { _ = writer.Close() }()

	if err := writer.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc.md", Content: "unchanged", Embedding: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := writer.SetMetadata(indexVersionKey, "2026-09-10T07:00:00Z"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	reader := openSecondReader(t, dbPath)

	reloaded, err := reader.RefreshIfStale()
	if err != nil {
		t.Fatalf("RefreshIfStale: %v", err)
	}
	if reloaded {
		t.Fatal("expected no reload while the index version is unchanged")
	}
}

// An index that was never stamped must not read as "changed" on every call:
// that would reload the whole corpus per query.
func TestRefreshIfStaleWithoutVersionStampDoesNotReloadRepeatedly(t *testing.T) {
	store := newTestStore(t)

	if err := store.Upsert([]Chunk{
		{ID: "c1", DocPath: "doc.md", Content: "no stamp", Embedding: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	for i := range 3 {
		reloaded, err := store.RefreshIfStale()
		if err != nil {
			t.Fatalf("RefreshIfStale #%d: %v", i, err)
		}
		if reloaded {
			t.Fatalf("call #%d reloaded an index that was never re-stamped", i)
		}
	}
}

// Concurrent searches must not each rebuild the same maps, and must all end up
// on the new copy.
func TestRefreshIfStaleUnderConcurrentReaders(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")

	writer, err := NewSQLiteStore(dbPath, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore (writer): %v", err)
	}
	defer func() { _ = writer.Close() }()

	if err := writer.Upsert([]Chunk{
		{ID: "c1", DocPath: "old.md", Content: "before", Embedding: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatalf("Upsert initial: %v", err)
	}
	if err := writer.SetMetadata(indexVersionKey, "2026-09-10T07:00:00Z"); err != nil {
		t.Fatalf("SetMetadata initial: %v", err)
	}

	reader := openSecondReader(t, dbPath)

	if err := writer.Upsert([]Chunk{
		{ID: "c2", DocPath: "new.md", Content: "after", Embedding: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatalf("Upsert after commit: %v", err)
	}
	if err := writer.SetMetadata(indexVersionKey, "2026-09-10T09:30:00Z"); err != nil {
		t.Fatalf("SetMetadata after commit: %v", err)
	}

	const readers = 8
	errs := make(chan error, readers)
	for range readers {
		go func() {
			_, err := reader.RefreshIfStale()
			errs <- err
		}()
	}
	for range readers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent RefreshIfStale: %v", err)
		}
	}

	if got := reader.Count(); got != 2 {
		t.Fatalf("expected 2 chunks after concurrent refresh, got %d", got)
	}
}

// publish commits a finished index the way the indexer does: last_indexed in
// the same transaction as the indexed-files state.
func publish(tb testing.TB, store *SQLiteStore, stamp string, files ...*IndexedFileInfo) {
	tb.Helper()
	if err := store.CommitIndexState(IndexStateUpdate{
		Metadata:    map[string]string{indexVersionKey: stamp},
		UpsertFiles: files,
	}); err != nil {
		tb.Fatalf("CommitIndexState: %v", err)
	}
}

func mustRefresh(tb testing.TB, store *SQLiteStore) bool {
	tb.Helper()
	reloaded, err := store.RefreshIfStale()
	if err != nil {
		tb.Fatalf("RefreshIfStale: %v", err)
	}
	return reloaded
}

// last_indexed has one-second resolution: two runs finishing within the same
// second used to leave the version unchanged, and a reader that reloaded
// between them never saw the second.
func TestRefreshIfStaleSeesTwoPublishesWithinOneSecond(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")
	writer := openSecondReader(t, dbPath)
	const sameSecond = "2026-09-25T10:00:00Z"

	if err := writer.Upsert([]Chunk{{ID: "a.md-0", DocPath: "a.md", Content: "first", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	publish(t, writer, sameSecond)

	reader := openSecondReader(t, dbPath)

	if err := writer.Upsert([]Chunk{{ID: "b.md-0", DocPath: "b.md", Content: "second", Embedding: []float32{0, 1, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	publish(t, writer, sameSecond)

	if !mustRefresh(t, reader) {
		t.Fatal("a second publish within the same second went unnoticed")
	}
	if got := reader.Count(); got != 2 {
		t.Fatalf("expected 2 chunks after reload, got %d", got)
	}
}

// The orphan sweep runs after the index commit. A reader that reloaded in
// between must learn about the deletion as well.
func TestRefreshIfStaleSeesOrphanSweepAfterPublish(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")
	writer := openSecondReader(t, dbPath)
	// Opened before the publish: a store sweeps orphans itself when it opens.
	reader := openSecondReader(t, dbPath)

	if err := writer.Upsert([]Chunk{
		{ID: "doc.md-0", DocPath: "doc.md", Content: "kept", Embedding: []float32{1, 0, 0}},
		{ID: "doc.md-1", DocPath: "doc.md", Content: "orphan", Embedding: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	publish(t, writer, "2026-09-25T10:00:00Z", &IndexedFileInfo{FilePath: "doc.md", ChunkCount: 1})

	// The reader reloads between the publish and the sweep.
	if !mustRefresh(t, reader) || reader.Count() != 2 {
		t.Fatalf("reader should reload onto 2 chunks after the publish, got %d", reader.Count())
	}

	removed, err := writer.CleanOrphans()
	if err != nil || removed != 1 {
		t.Fatalf("CleanOrphans = %d, %v; want 1, nil", removed, err)
	}

	if !mustRefresh(t, reader) {
		t.Fatal("the orphan sweep went unnoticed by a reader that loaded before it")
	}
	if got := reader.Count(); got != 1 {
		t.Fatalf("expected the orphan gone after reload, got %d chunks", got)
	}
}

// The process that indexed has already updated its maps in place; it must not
// reload the whole corpus on its next search.
func TestRefreshIfStaleSkipsReloadAfterOwnPublish(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")
	writer := openSecondReader(t, dbPath)

	if err := writer.Upsert([]Chunk{
		{ID: "doc.md-0", DocPath: "doc.md", Content: "kept", Embedding: []float32{1, 0, 0}},
		{ID: "doc.md-1", DocPath: "doc.md", Content: "orphan", Embedding: []float32{0, 1, 0}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	publish(t, writer, "2026-09-25T10:00:00Z", &IndexedFileInfo{FilePath: "doc.md", ChunkCount: 1})
	if mustRefresh(t, writer) {
		t.Fatal("the writer reloaded after its own publish")
	}

	if _, err := writer.CleanOrphans(); err != nil {
		t.Fatalf("CleanOrphans: %v", err)
	}
	if mustRefresh(t, writer) {
		t.Fatal("the writer reloaded after its own orphan sweep")
	}
}

// Adopting its own version must not hide a publish another process made
// before it: the writer then still reloads.
func TestRefreshIfStaleAfterOwnPublishStillSeesOtherProcess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")
	writer := openSecondReader(t, dbPath)
	other := openSecondReader(t, dbPath)

	if err := other.Upsert([]Chunk{{ID: "other.md-0", DocPath: "other.md", Content: "theirs", Embedding: []float32{0, 0, 1}}}); err != nil {
		t.Fatalf("Upsert (other): %v", err)
	}
	publish(t, other, "2026-09-25T10:00:00Z")

	if err := writer.Upsert([]Chunk{{ID: "mine.md-0", DocPath: "mine.md", Content: "mine", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert (writer): %v", err)
	}
	publish(t, writer, "2026-09-25T10:00:01Z")

	if !mustRefresh(t, writer) {
		t.Fatal("the writer adopted its version over another process's publish")
	}
	if got := writer.Count(); got != 2 {
		t.Fatalf("expected both processes' chunks, got %d", got)
	}
}

// Marking the index dirty at the start of a run must not send readers to
// reload a half-written index.
func TestDirtyMarkDoesNotAdvanceIndexVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared-vec.db")
	writer := openSecondReader(t, dbPath)
	publish(t, writer, "2026-09-25T10:00:00Z")

	reader := openSecondReader(t, dbPath)

	if err := writer.CommitIndexState(IndexStateUpdate{
		Metadata: map[string]string{"index_state": "dirty"},
	}); err != nil {
		t.Fatalf("CommitIndexState (dirty): %v", err)
	}
	if mustRefresh(t, reader) {
		t.Fatal("a dirty mark made the reader reload a half-written index")
	}
}
