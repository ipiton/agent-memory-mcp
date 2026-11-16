package vectorstore

import (
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func vectorIDs(t *testing.T, s *SQLiteStore, query []float32) []string {
	t.Helper()
	results, _, err := s.Search(query, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

// Removing a slot moves the last one into the hole; the moved chunk must stay
// findable and keep its own vector.
func TestVectorIndexRemoveKeepsOthersIntact(t *testing.T) {
	x := newVectorIndex(2, 0)
	x.put(chunkMeta{ID: "a"}, []float32{1, 0})
	x.put(chunkMeta{ID: "b"}, []float32{0, 1})
	x.put(chunkMeta{ID: "c"}, []float32{1, 1})
	x.remove("a")
	x.remove("missing")

	if x.len() != 2 {
		t.Fatalf("len = %d, want 2", x.len())
	}
	for id, want := range map[string][]float32{"b": {0, 1}, "c": {1, 1}} {
		i, ok := x.pos[id]
		if !ok || x.meta[i].ID != id {
			t.Fatalf("%s: pos=%d ok=%v", id, i, ok)
		}
		if got := x.vector(i); got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%s vector = %v, want %v", id, got, want)
		}
	}

	x.put(chunkMeta{ID: "b", Title: "new"}, []float32{0.5, 0.5})
	if i := x.pos["b"]; x.meta[i].Title != "new" || x.vector(i)[0] != 0.5 || x.len() != 2 {
		t.Errorf("replace in place failed: %+v %v", x.meta[i], x.vector(i))
	}
	x.put(chunkMeta{ID: "c"}, []float32{1, 2, 3})
	if _, ok := x.pos["c"]; ok || x.len() != 1 {
		t.Errorf("a vector of the wrong length must take the chunk out of memory")
	}
}

// Upsert and delete after the load change what search sees without a reload.
func TestVectorsFollowUpsertAndDeleteInMemory(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "v.db"), 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Upsert([]Chunk{
		{ID: "x1", DocPath: "x.md", Content: "x", Embedding: []float32{1, 0}},
		{ID: "y1", DocPath: "y.md", Content: "y", Embedding: []float32{0, 1}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if ids := vectorIDs(t, s, []float32{1, 0.2}); len(ids) != 2 || ids[0] != "x1" {
		t.Fatalf("ids = %v", ids)
	}
	if err := s.DeleteByDocPath("x.md"); err != nil {
		t.Fatalf("DeleteByDocPath: %v", err)
	}
	if err := s.Upsert([]Chunk{{ID: "z1", DocPath: "z.md", Content: "z", Embedding: []float32{1, 0.1}}}); err != nil {
		t.Fatalf("Upsert z: %v", err)
	}
	if ids := vectorIDs(t, s, []float32{1, 0.2}); len(ids) != 2 || ids[0] != "z1" || ids[1] != "y1" {
		t.Fatalf("ids after delete+upsert = %v, want [z1 y1]", ids)
	}
}

// T177: StartVectorLoad returns at once and the load lands in the background;
// the idle unloader drops the vectors and the next search brings them back.
func TestBackgroundLoadAndIdleUnload(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v.db")
	writer, err := NewSQLiteStore(dbPath, 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := writer.Upsert([]Chunk{{ID: "a", DocPath: "a.md", Content: "a", Embedding: []float32{1, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_ = writer.Close()

	s, err := NewSQLiteStore(dbPath, 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	waitLoaded := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s.mu.RLock()
			loaded := s.vectorsLoaded
			s.mu.RUnlock()
			if loaded == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("vectorsLoaded never became %v", want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	if s.StartVectorLoad() {
		t.Fatal("StartVectorLoad reported vectors in memory before any load")
	}
	waitLoaded(true)
	if !s.StartVectorLoad() || s.vectorCount() != 1 {
		t.Fatalf("after background load: count=%d", s.vectorCount())
	}

	s.SetIdleUnload(40 * time.Millisecond)
	waitLoaded(false)
	if s.vectorCount() != 0 {
		t.Fatalf("vectors still in memory after idle unload: %d", s.vectorCount())
	}
	if ids := vectorIDs(t, s, []float32{1, 0}); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("search after unload = %v, want [a]", ids)
	}
}

// Every search calls RefreshIfStale first; behind a background load it must
// not wait for the load, or the client is held after all.
func TestRefreshDoesNotWaitForBackgroundLoad(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "v.db"), 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	s.refreshMu.Lock() // the load in progress
	s.mu.Lock()
	s.vectorLoading = true
	s.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := s.RefreshIfStale()
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RefreshIfStale: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RefreshIfStale waited for the background load")
	}
	s.mu.Lock()
	s.vectorLoading = false
	s.mu.Unlock()
	s.refreshMu.Unlock()
}

// Close must not leave a background load running against a closed database.
func TestCloseWaitsForBackgroundLoad(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "v.db"), 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	s.StartVectorLoad()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A call after Close must not start another load against the closed db.
	s.StartVectorLoad()
	s.bg.Wait()
}
