package memory

import (
	"context"
	"testing"
)

// TestMarkOutdated_IncrementsReferencedByCountOnSupersededBy asserts that
// MarkOutdated(id, reason, supersededBy) bumps referenced_by_count on the
// SupersededBy target. This activates the T48 semantic→character "by refs"
// promotion rule which was previously dormant.
func TestMarkOutdated_IncrementsReferencedByCountOnSupersededBy(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	newer := &Memory{
		Title:      "Current truth",
		Content:    "the new authoritative answer",
		Type:       TypeSemantic,
		Importance: 0.8,
		Metadata:   map[string]string{"entity": "decision"},
	}
	older := &Memory{
		Title:      "Old truth",
		Content:    "the old answer",
		Type:       TypeSemantic,
		Importance: 0.8,
		Metadata:   map[string]string{"entity": "decision"},
	}
	if err := store.Store(ctx, newer); err != nil {
		t.Fatalf("Store newer: %v", err)
	}
	if err := store.Store(ctx, older); err != nil {
		t.Fatalf("Store older: %v", err)
	}

	if got := getCount(t, store, newer.ID); got != 0 {
		t.Fatalf("pre-supersede count = %d, want 0", got)
	}

	if _, err := store.MarkOutdated(ctx, older.ID, "replaced", newer.ID); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}

	if got := getCount(t, store, newer.ID); got != 1 {
		t.Fatalf("post-supersede count = %d, want 1 (MarkOutdated should bump)", got)
	}
}

// TestMarkOutdated_NoIncrementWhenSupersededByEmpty ensures MarkOutdated
// without a supersededBy target skips the increment path entirely (and does
// not e.g. call with empty string causing an error or an accidental bump).
func TestMarkOutdated_NoIncrementWhenSupersededByEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	target := &Memory{
		Title:      "Unrelated memory",
		Content:    "unrelated",
		Type:       TypeSemantic,
		Importance: 0.8,
	}
	if err := store.Store(ctx, target); err != nil {
		t.Fatalf("Store: %v", err)
	}

	stale := &Memory{
		Title:   "Stale",
		Content: "stale fact",
		Type:    TypeSemantic,
	}
	if err := store.Store(ctx, stale); err != nil {
		t.Fatalf("Store stale: %v", err)
	}

	if _, err := store.MarkOutdated(ctx, stale.ID, "just stale", ""); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}

	// No supersession edge → no counter bump anywhere.
	if got := getCount(t, store, target.ID); got != 0 {
		t.Fatalf("unrelated target count = %d, want 0", got)
	}
}
