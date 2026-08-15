package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// T116 acceptance, in the form the task asks for: two Stores over one file,
// reproducing the divergence and showing it gone.
//
// On the live service the daemon reported 4562 records against the file's 4564,
// and the two it could not see were session checkpoints written by a hook 40
// minutes earlier. Everything but Get reads the cache, so those records were
// absent from recall, from list, and from every steward and sediment pass.
func TestDaemonSeesRecordsWrittenByAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	ctx := context.Background()

	daemon, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("open daemon store: %v", err)
	}
	defer func() { _ = daemon.Close() }()

	cli, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("open cli store: %v", err)
	}
	defer func() { _ = cli.Close() }()

	before := daemon.Count()

	// The divergence, without the watcher: the CLI writes, the daemon's cache
	// does not move.
	checkpoint := &Memory{
		Title:   "Session close / provenance-gate",
		Content: "Промоушен в канон требует доверенного провенанса.",
		Type:    TypeEpisodic,
		Context: "provenance-gate",
	}
	if err := cli.Store(ctx, checkpoint); err != nil {
		t.Fatalf("cli write: %v", err)
	}
	if got := daemon.Count(); got != before {
		t.Fatalf("daemon count moved to %d without a watcher — the fixture no longer reproduces the bug", got)
	}
	if found := daemon.ListLightweight(Filters{Context: "provenance-gate"}); len(found) != 0 {
		t.Fatalf("record already visible to the daemon (%d) — nothing left to fix", len(found))
	}

	// With the watcher, the same write converges.
	if err := daemon.WatchForeignWrites(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}

	second := &Memory{
		Title:   "Session close / decay-defaults",
		Content: "Возрастное затухание выключено по умолчанию.",
		Type:    TypeEpisodic,
		Context: "decay-defaults",
	}
	if err := cli.Store(ctx, second); err != nil {
		t.Fatalf("second cli write: %v", err)
	}

	deadline := time.Now().Add(10 * foreignWriteInterval)
	for time.Now().Before(deadline) {
		if len(daemon.ListLightweight(Filters{Context: "decay-defaults"})) == 1 {
			break
		}
		time.Sleep(foreignWriteInterval / 4)
	}

	if found := daemon.ListLightweight(Filters{Context: "decay-defaults"}); len(found) != 1 {
		t.Fatalf("record written by another process never became visible (found %d)", len(found))
	}
	// The reload is a full rebuild, so the earlier record arrives with it.
	if found := daemon.ListLightweight(Filters{Context: "provenance-gate"}); len(found) != 1 {
		t.Errorf("the pre-watcher record did not converge either (found %d)", len(found))
	}
	if got, want := daemon.Count(), before+2; got != want {
		t.Errorf("daemon count = %d, want %d", got, want)
	}
}

// Close must release the pinned connection and the goroutine; a second Close
// and a Close without a watcher must both be harmless.
func TestStopWatchingIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.stopWatching() // never started

	if err := store.WatchForeignWrites(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := store.WatchForeignWrites(context.Background()); err != nil {
		t.Fatalf("second start must be a no-op: %v", err)
	}
	store.stopWatching()
	store.stopWatching()

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
