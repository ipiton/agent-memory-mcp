package memory

import (
	"context"
	"strconv"
	"testing"
)

// seedMemory stores a memory with the given metadata and returns the
// generated ID. Fails the test on any error.
func seedMemory(t *testing.T, store *Store, content string, metadata map[string]string) string {
	t.Helper()
	m := &Memory{
		Content:  content,
		Type:     TypeSemantic,
		Metadata: metadata,
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store(%q): %v", content, err)
	}
	return m.ID
}

// getCount reads the referenced_by_count metadata value for id, returning
// 0 when the key is missing (matches the policy used by Decide).
func getCount(t *testing.T, store *Store, id string) int {
	t.Helper()
	mem, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return referencedByCountFromMetadata(mem.Metadata)
}

func TestIncrementReferencedByCount_NoExistingMetadata(t *testing.T) {
	store := newTestStore(t)
	id := seedMemory(t, store, "fresh memory", nil)

	if err := store.IncrementReferencedByCount(context.Background(), id); err != nil {
		t.Fatalf("IncrementReferencedByCount: %v", err)
	}

	if got := getCount(t, store, id); got != 1 {
		t.Fatalf("count after first increment = %d, want 1", got)
	}
}

func TestIncrementReferencedByCount_ExistingMetadata_BumpsCounter(t *testing.T) {
	store := newTestStore(t)
	id := seedMemory(t, store, "seeded", map[string]string{
		MetadataReferencedByCount: "5",
		"unrelated":               "keep",
	})

	if err := store.IncrementReferencedByCount(context.Background(), id); err != nil {
		t.Fatalf("IncrementReferencedByCount: %v", err)
	}

	mem, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := referencedByCountFromMetadata(mem.Metadata); got != 6 {
		t.Fatalf("count = %d, want 6", got)
	}
	if mem.Metadata["unrelated"] != "keep" {
		t.Fatalf("unrelated metadata dropped; got %q", mem.Metadata["unrelated"])
	}
}

func TestIncrementReferencedByCount_NonexistentID(t *testing.T) {
	store := newTestStore(t)

	// Nonexistent ID is a no-op; returns nil without error.
	if err := store.IncrementReferencedByCount(context.Background(), "does-not-exist"); err != nil {
		t.Fatalf("IncrementReferencedByCount(missing) = %v, want nil", err)
	}
}

func TestIncrementReferencedByCount_EmptyID(t *testing.T) {
	store := newTestStore(t)

	if err := store.IncrementReferencedByCount(context.Background(), ""); err != nil {
		t.Fatalf("IncrementReferencedByCount(\"\") = %v, want nil", err)
	}
	if err := store.IncrementReferencedByCount(context.Background(), "   "); err != nil {
		t.Fatalf("IncrementReferencedByCount(whitespace) = %v, want nil", err)
	}
}

func TestIncrementReferencedByCount_PreservesOtherMetadata(t *testing.T) {
	store := newTestStore(t)
	id := seedMemory(t, store, "with metadata", map[string]string{
		"owner":   "platform",
		"service": "redis",
	})

	if err := store.IncrementReferencedByCount(context.Background(), id); err != nil {
		t.Fatalf("IncrementReferencedByCount: %v", err)
	}

	mem, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mem.Metadata["owner"] != "platform" {
		t.Fatalf("owner dropped; metadata=%v", mem.Metadata)
	}
	if mem.Metadata["service"] != "redis" {
		t.Fatalf("service dropped; metadata=%v", mem.Metadata)
	}
	if mem.Metadata[MetadataReferencedByCount] != "1" {
		t.Fatalf("counter = %q, want 1", mem.Metadata[MetadataReferencedByCount])
	}
}

func TestRecountReferences_BuildsTallyFromAvoidedDeadEnd(t *testing.T) {
	store := newTestStore(t)

	deadEndID := seedMemory(t, store, "dead end A", nil)

	// Three decisions that avoided the same dead-end.
	for i := 0; i < 3; i++ {
		seedMemory(t, store, "decision "+strconv.Itoa(i), map[string]string{
			"avoided_dead_end_id": deadEndID,
		})
	}

	result, err := store.RecountReferences(context.Background(), false)
	if err != nil {
		t.Fatalf("RecountReferences: %v", err)
	}
	if result.Updated != 1 {
		t.Fatalf("Updated = %d, want 1 (only the dead-end counter changes)", result.Updated)
	}
	if got := result.Counts[deadEndID]; got != 3 {
		t.Fatalf("result.Counts[%s] = %d, want 3", deadEndID, got)
	}
	if got := getCount(t, store, deadEndID); got != 3 {
		t.Fatalf("stored count = %d, want 3", got)
	}
}

func TestRecountReferences_BuildsTallyFromSupersededBy(t *testing.T) {
	store := newTestStore(t)

	newer := seedMemory(t, store, "newer truth", nil)
	older1 := seedMemory(t, store, "older truth 1", nil)
	older2 := seedMemory(t, store, "older truth 2", nil)

	// Mark both older memories as superseded by the newer one. MarkOutdated
	// itself increments the counter too, so after this the stored count on
	// `newer` is already 2. The recount should confirm that — Updated=0.
	if _, err := store.MarkOutdated(context.Background(), older1, "replaced", newer); err != nil {
		t.Fatalf("MarkOutdated(older1): %v", err)
	}
	if _, err := store.MarkOutdated(context.Background(), older2, "replaced", newer); err != nil {
		t.Fatalf("MarkOutdated(older2): %v", err)
	}

	// Sanity: live-path increment already did the work.
	if got := getCount(t, store, newer); got != 2 {
		t.Fatalf("pre-recount count = %d, want 2 (live-path increment)", got)
	}

	result, err := store.RecountReferences(context.Background(), false)
	if err != nil {
		t.Fatalf("RecountReferences: %v", err)
	}
	if result.Updated != 0 {
		t.Fatalf("Updated = %d, want 0 on already-synced corpus", result.Updated)
	}

	// And the stored count is still 2 after the idempotent recount.
	if got := getCount(t, store, newer); got != 2 {
		t.Fatalf("post-recount count = %d, want 2", got)
	}
}

func TestRecountReferences_DryRun_NoMutation(t *testing.T) {
	store := newTestStore(t)

	// Simulate a pre-patch corpus where the live-path increment never ran:
	// inject the edges directly and explicitly zero-out the counter so the
	// recount sees a real divergence.
	deadEnd := seedMemory(t, store, "dead end", map[string]string{
		MetadataReferencedByCount: "0",
	})
	seedMemory(t, store, "dec 1", map[string]string{"avoided_dead_end_id": deadEnd})
	seedMemory(t, store, "dec 2", map[string]string{"avoided_dead_end_id": deadEnd})

	result, err := store.RecountReferences(context.Background(), true)
	if err != nil {
		t.Fatalf("RecountReferences dryRun: %v", err)
	}
	if !result.DryRun {
		t.Fatalf("result.DryRun = false, want true")
	}
	if result.Updated != 1 {
		t.Fatalf("Updated = %d, want 1", result.Updated)
	}
	if got := result.Counts[deadEnd]; got != 2 {
		t.Fatalf("result.Counts[%s] = %d, want 2", deadEnd, got)
	}

	// But nothing should have been written.
	if got := getCount(t, store, deadEnd); got != 0 {
		t.Fatalf("stored count after dry-run = %d, want 0 (no mutation)", got)
	}
}

func TestRecountReferences_Idempotent(t *testing.T) {
	store := newTestStore(t)

	deadEnd := seedMemory(t, store, "dead end", map[string]string{
		MetadataReferencedByCount: "0",
	})
	seedMemory(t, store, "dec 1", map[string]string{"avoided_dead_end_id": deadEnd})
	seedMemory(t, store, "dec 2", map[string]string{"avoided_dead_end_id": deadEnd})

	// First run: writes the tally.
	first, err := store.RecountReferences(context.Background(), false)
	if err != nil {
		t.Fatalf("first RecountReferences: %v", err)
	}
	if first.Updated == 0 {
		t.Fatalf("first.Updated = 0, want > 0 on divergent corpus")
	}

	// Second run: counter already matches derived tally → nothing to do.
	second, err := store.RecountReferences(context.Background(), false)
	if err != nil {
		t.Fatalf("second RecountReferences: %v", err)
	}
	if second.Updated != 0 {
		t.Fatalf("second.Updated = %d, want 0 on already-synced corpus", second.Updated)
	}
	if got := getCount(t, store, deadEnd); got != 2 {
		t.Fatalf("stored count = %d, want 2 (stable)", got)
	}
}
