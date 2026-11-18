package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newCacheTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "memories.db"), nil, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func storeCacheTestMemory(t *testing.T, store *Store, id, content string) {
	t.Helper()
	err := store.Store(context.Background(), &Memory{ID: id, Content: content, Type: TypeSemantic, Context: "cache-load"})
	if err != nil {
		t.Fatalf("store %s: %v", id, err)
	}
}

func cachedContent(store *Store, id string) (string, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	m, ok := store.memories[id]
	if !ok {
		return "", false
	}
	return m.Content, true
}

// T175: a rebuild is slow on a starved machine, so reads and writes must get
// through while it runs, and what was written meanwhile must survive it.
func TestCacheRebuildDoesNotBlockAndKeepsConcurrentWrites(t *testing.T) {
	store := newCacheTestStore(t)
	ctx := context.Background()
	storeCacheTestMemory(t, store, "updated", "old text")
	storeCacheTestMemory(t, store, "deleted", "doomed text")

	scanned := make(chan struct{})
	resume := make(chan struct{})
	store.afterCacheScan = func() {
		close(scanned)
		<-resume
	}
	rebuilt := make(chan error, 1)
	go func() { rebuilt <- store.ReloadCache() }()
	<-scanned

	operations := make(chan error, 1)
	go func() {
		if got := store.Count(); got != 2 {
			t.Errorf("count during rebuild = %d, want the previous 2", got)
		}
		if err := store.Update(ctx, "updated", Update{Content: "new text"}); err != nil {
			operations <- err
			return
		}
		if err := store.Delete(ctx, "deleted"); err != nil {
			operations <- err
			return
		}
		operations <- store.Store(ctx, &Memory{ID: "added", Content: "added text", Type: TypeSemantic, Context: "cache-load"})
	}()
	select {
	case err := <-operations:
		if err != nil {
			t.Fatalf("write during rebuild: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reads or writes waited for the rebuild")
	}

	close(resume)
	if err := <-rebuilt; err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got, _ := cachedContent(store, "updated"); got != "new text" {
		t.Errorf("updated record = %q, the rebuild restored its older row", got)
	}
	if _, ok := cachedContent(store, "deleted"); ok {
		t.Error("deleted record came back with the rebuild")
	}
	if got, _ := cachedContent(store, "added"); got != "added text" {
		t.Errorf("added record = %q, the rebuild lost it", got)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.loadDirty != nil {
		t.Error("the rebuild left its dirty set behind")
	}
	if _, ok := store.contextIndex["cache-load"]["deleted"]; ok {
		t.Error("context index still holds the deleted record")
	}
}

// A rebuild that fails must leave the working cache in place.
func TestFailedCacheRebuildKeepsCache(t *testing.T) {
	store := newCacheTestStore(t)
	storeCacheTestMemory(t, store, "kept", "kept text")
	if _, err := store.db.Exec(`ALTER TABLE memories RENAME TO memories_gone`); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := store.ReloadCache(); err == nil {
		t.Fatal("rebuild over a missing table succeeded")
	}
	if got, ok := cachedContent(store, "kept"); !ok || got != "kept text" {
		t.Errorf("cache after failed rebuild: %q, %v", got, ok)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.loadDirty != nil {
		t.Error("a failed rebuild left its dirty set behind")
	}
}
