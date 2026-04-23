package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// newSchedulerTestStore returns a fresh memory.Store on a temp DB.
func newSchedulerTestStore(t *testing.T) *memory.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memories.db")
	store, err := memory.NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedAgedSurfaceMemory inserts a memory that Decide will promote
// surface → episodic on the next cycle: age >= 7d, access_count >= 1.
// Returns the stored memory ID.
func seedAgedSurfaceMemory(t *testing.T, store *memory.Store) string {
	t.Helper()
	ctx := context.Background()
	m := &memory.Memory{
		Content:     "aged surface memory for scheduler test",
		Type:        memory.TypeWorking,
		Context:     "scheduler-test",
		AccessCount: 3,
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Backdate so Decide sees age >= SurfaceToEpisodicAge (7d).
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	if err := store.BackdateForTest(m.ID, oldTime, 3); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	return m.ID
}

func TestSedimentScheduler_DoesNotStartWhenIntervalZero(t *testing.T) {
	store := newSchedulerTestStore(t)
	id := seedAgedSurfaceMemory(t, store)

	sched := newSedimentScheduler(store, nil, true /*enabled*/, 0 /*interval*/)
	if sched != nil {
		t.Fatalf("newSedimentScheduler returned non-nil for interval=0")
	}
	// Calling Start/Close on nil is a no-op; confirm no panic.
	sched.Start()
	sched.Close()

	// Give any hypothetical goroutine time to run.
	time.Sleep(100 * time.Millisecond)

	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(memory.LayerSurface) {
		t.Errorf("SedimentLayer=%q, want surface (no cycle should have run)", got.SedimentLayer)
	}
}

func TestSedimentScheduler_DoesNotStartWhenSedimentDisabled(t *testing.T) {
	store := newSchedulerTestStore(t)
	id := seedAgedSurfaceMemory(t, store)

	sched := newSedimentScheduler(store, nil, false /*enabled*/, 50*time.Millisecond)
	if sched != nil {
		t.Fatalf("newSedimentScheduler returned non-nil for enabled=false")
	}
	sched.Start()
	defer sched.Close()

	time.Sleep(200 * time.Millisecond)

	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(memory.LayerSurface) {
		t.Errorf("SedimentLayer=%q, want surface (disabled flag must skip scheduling)", got.SedimentLayer)
	}
}

func TestSedimentScheduler_RunsOnTick(t *testing.T) {
	store := newSchedulerTestStore(t)
	id := seedAgedSurfaceMemory(t, store)

	sched := newSedimentScheduler(store, nil, true, 30*time.Millisecond)
	if sched == nil {
		t.Fatalf("newSedimentScheduler returned nil despite enabled=true + interval>0")
	}
	sched.Start()
	defer sched.Close()

	// Poll for up to 1s for the promotion to land. The first tick fires
	// after ~30ms; allow generous slack on loaded CI runners.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		got, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.SedimentLayer == string(memory.LayerEpisodic) {
			return // promotion happened — scheduler ran
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Errorf("SedimentLayer=%q after scheduler window, want episodic", got.SedimentLayer)
}

func TestSedimentScheduler_ShutsDownOnClose(t *testing.T) {
	store := newSchedulerTestStore(t)
	// Long interval — the goroutine will be parked waiting on the ticker.
	sched := newSedimentScheduler(store, nil, true, 1*time.Hour)
	if sched == nil {
		t.Fatal("newSedimentScheduler returned nil")
	}
	sched.Start()

	doneCh := sched.done // captured for observation
	closeDone := make(chan struct{})

	start := time.Now()
	go func() {
		sched.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not return within 500ms")
	}

	select {
	case <-doneCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("scheduler goroutine did not exit within 500ms of Close")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Close took %s, want < 1s", elapsed)
	}
}
