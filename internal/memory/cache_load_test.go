package memory

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
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

// openDeferredWithRecords fills a bank through a normal store, closes it and
// reopens the file without loading it.
func openDeferredWithRecords(t *testing.T, ids ...string) *Store {
	t.Helper()
	store := openDeferredUnclosed(t, ids...)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openDeferredUnclosed(t *testing.T, ids ...string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memories.db")
	seed, err := NewStore(path, nil, nil)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	for _, id := range ids {
		storeCacheTestMemory(t, seed, id, "text of "+id)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	store, err := OpenStoreDeferred(path, nil, nil)
	if err != nil {
		t.Fatalf("open deferred store: %v", err)
	}
	return store
}

func (ms *Store) dbPathForTest(t *testing.T) string {
	t.Helper()
	var seq int
	var name, path string
	if err := ms.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatalf("database path: %v", err)
	}
	return path
}

func listedIDs(t *testing.T, store *Store) map[string]bool {
	t.Helper()
	listed, err := store.List(context.Background(), Filters{Context: "cache-load"}, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := make(map[string]bool, len(listed))
	for _, m := range listed {
		ids[m.ID] = true
	}
	return ids
}

// T175: a session hook reads one context and writes a record. Neither may pull
// the whole bank into memory, and both must behave as with a loaded cache.
func TestDeferredStoreServesContextFromFile(t *testing.T) {
	store := openDeferredWithRecords(t, "kept", "doomed")
	ctx := context.Background()

	if got := listedIDs(t, store); !got["kept"] || !got["doomed"] {
		t.Fatalf("records before any write: %v", got)
	}
	if !store.hasMemory("kept") || store.hasMemory("missing") {
		t.Error("hasMemory does not answer from the file")
	}
	storeCacheTestMemory(t, store, "added", "added text")
	if err := store.Delete(ctx, "doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := listedIDs(t, store); !got["added"] || got["doomed"] {
		t.Errorf("records after writes: %v", got)
	}
	if store.CacheReady() {
		t.Fatal("reading one context loaded the bank")
	}
	store.mu.RLock()
	cached := len(store.memories)
	store.mu.RUnlock()
	if cached != 0 {
		t.Errorf("%d records in a cache that is not loaded", cached)
	}

	// Whatever needs the bank loads it, and sees the writes made before.
	if got := store.Count(); got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
	if !store.CacheReady() {
		t.Error("count did not load the bank")
	}
	if got := listedIDs(t, store); !got["kept"] || !got["added"] || got["doomed"] {
		t.Errorf("records after the load: %v", got)
	}
}

func TestDeferredStoreRecallLoadsTheBank(t *testing.T) {
	store := openDeferredWithRecords(t, "found")
	results, err := store.Recall(context.Background(), "text of found", Filters{Context: "cache-load"}, 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(results) != 1 || !store.CacheReady() {
		t.Errorf("recall returned %d results, cache ready = %v", len(results), store.CacheReady())
	}
}

// Writes made while the first load runs reach the cache, and so does a commit
// by another process, which the load cannot have seen and no dirty mark covers.
func TestBackgroundLoadKeepsWritesMadeDuringIt(t *testing.T) {
	store := openDeferredWithRecords(t, "updated", "deleted")
	ctx := context.Background()
	if err := store.WatchForeignWrites(ctx); err != nil {
		t.Fatalf("watch: %v", err)
	}
	other, err := OpenStoreDeferred(store.dbPathForTest(t), nil, nil)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer func() { _ = other.Close() }()

	scanned := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	store.afterCacheScan = func() {
		once.Do(func() {
			close(scanned)
			<-resume
		})
	}
	done := store.StartCacheLoad()
	<-scanned

	if err := store.WaitCacheReady(10 * time.Millisecond); !errors.Is(err, ErrCacheLoading) {
		t.Errorf("wait during the load = %v, want ErrCacheLoading", err)
	}
	if store.StartCacheLoad() != done {
		t.Error("a second start did not join the running load")
	}
	if err := store.Update(ctx, "updated", Update{Content: "new text"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := store.Delete(ctx, "deleted"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	storeCacheTestMemory(t, store, "added", "added text")
	storeCacheTestMemory(t, other, "foreign", "foreign text")
	// One watcher tick has to pass while the load is still running.
	time.Sleep(foreignWriteInterval + 200*time.Millisecond)
	close(resume)
	<-done

	if err := store.WaitCacheReady(time.Second); err != nil {
		t.Fatalf("wait after the load: %v", err)
	}
	if got, _ := cachedContent(store, "updated"); got != "new text" {
		t.Errorf("updated record = %q", got)
	}
	if _, ok := cachedContent(store, "deleted"); ok {
		t.Error("deleted record came back")
	}
	if _, ok := cachedContent(store, "added"); !ok {
		t.Error("added record is missing")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := cachedContent(store, "foreign"); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a commit made during the load never reached the cache")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFailedBackgroundLoadIsRetried(t *testing.T) {
	store := openDeferredWithRecords(t, "kept")
	if _, err := store.db.Exec(`ALTER TABLE memories RENAME TO memories_gone`); err != nil {
		t.Fatalf("rename: %v", err)
	}
	err := store.WaitCacheReady(5 * time.Second)
	if err == nil || errors.Is(err, ErrCacheLoading) {
		t.Fatalf("wait over a missing table = %v, want the load error", err)
	}
	if store.CacheReady() {
		t.Fatal("a failed load marked the cache ready")
	}
	if _, err := store.db.Exec(`ALTER TABLE memories_gone RENAME TO memories`); err != nil {
		t.Fatalf("rename back: %v", err)
	}
	if err := store.WaitCacheReady(5 * time.Second); err != nil {
		t.Fatalf("wait after the repair: %v", err)
	}
	if _, ok := cachedContent(store, "kept"); !ok {
		t.Error("record is missing after the retry")
	}
}

// Close must not wait for a load to read the rest of a large bank.
func TestCloseStopsBackgroundLoad(t *testing.T) {
	store := openDeferredUnclosed(t, "kept")
	scanned := make(chan struct{})
	store.afterCacheScan = func() {
		close(scanned)
		<-store.closeCtx.Done()
	}
	store.StartCacheLoad()
	<-scanned

	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close waited for the load")
	}
}
