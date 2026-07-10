package memory

import (
	"context"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func newMergeTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "m.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestMergeDuplicatesSkipsMissingIDs is the T81 idempotent-merge fix: a missing
// (already-archived/deleted) duplicate id is skipped and reported, not fatal, so
// one stale id doesn't abort the whole batch.
func TestMergeDuplicatesSkipsMissingIDs(t *testing.T) {
	store := newMergeTestStore(t)
	ctx := context.Background()

	primary := &Memory{Content: "rollback runbook", Type: TypeProcedural, Importance: 0.8}
	dup := &Memory{Content: "rollback runbook copy", Type: TypeProcedural, Importance: 0.8}
	if err := store.Store(ctx, primary); err != nil {
		t.Fatal(err)
	}
	if err := store.Store(ctx, dup); err != nil {
		t.Fatal(err)
	}

	res, err := store.MergeDuplicates(ctx, primary.ID, []string{dup.ID, "missing-id-123"})
	if err != nil {
		t.Fatalf("MergeDuplicates with one missing id must not fail: %v", err)
	}
	if res.MergedFromCount != 1 {
		t.Fatalf("MergedFromCount = %d, want 1", res.MergedFromCount)
	}
	if len(res.SkippedDuplicateIDs) != 1 || res.SkippedDuplicateIDs[0] != "missing-id-123" {
		t.Fatalf("SkippedDuplicateIDs = %v, want [missing-id-123]", res.SkippedDuplicateIDs)
	}
}

// TestMergeDuplicatesAllMissingIsNoOp: when every duplicate id is already gone
// the call is an idempotent no-op success (not an error) — a re-run of a merge
// that already happened (T81).
func TestMergeDuplicatesAllMissingIsNoOp(t *testing.T) {
	store := newMergeTestStore(t)
	ctx := context.Background()

	primary := &Memory{Content: "the surviving primary", Type: TypeSemantic, Importance: 0.8}
	if err := store.Store(ctx, primary); err != nil {
		t.Fatal(err)
	}

	res, err := store.MergeDuplicates(ctx, primary.ID, []string{"gone-1", "gone-2"})
	if err != nil {
		t.Fatalf("all-missing merge must be a no-op success, got: %v", err)
	}
	if res.MergedFromCount != 0 {
		t.Fatalf("MergedFromCount = %d, want 0", res.MergedFromCount)
	}
	if len(res.SkippedDuplicateIDs) != 2 {
		t.Fatalf("SkippedDuplicateIDs = %v, want 2 entries", res.SkippedDuplicateIDs)
	}
}
