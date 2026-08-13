package lifecycle

import (
	"context"
	"errors"
	"testing"
)

// T89 M6. A sweep over a large archive used to run to completion regardless of
// cancellation: shutdown had to wait it out, and every write it kept issuing
// went out on an already-cancelled context.
func TestSweepArchiveStopsOnCancelledContext(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-A")
	seedWorkingMemory(t, store, "task-A", "note one", 0.3, nil, nil)

	sw := NewSweeper(store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := sw.SweepArchive(ctx, ArchiveSweepConfig{Roots: []string{root}})
	if err == nil {
		t.Fatal("SweepArchive returned nil error on a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
}
