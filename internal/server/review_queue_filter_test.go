package server

import (
	"context"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func seedReviewQueueItem(t *testing.T, s *MCPServer, title string, at time.Time) *memory.Memory {
	t.Helper()
	s.memoryStore.SetClock(func() time.Time { return at })
	item := &memory.Memory{
		Title:      title,
		Content:    "Action: merge\nHandling: hard_review",
		Type:       memory.TypeWorking,
		Importance: 0.45,
		Metadata: map[string]string{
			memory.MetadataRecordKind:     memory.RecordKindReviewQueueItem,
			memory.MetadataReviewRequired: "true",
			memory.MetadataStatus:         "review_required",
		},
	}
	if err := s.memoryStore.Store(context.Background(), item); err != nil {
		t.Fatalf("Store review item: %v", err)
	}
	return item
}

// TestResolveReviewQueueTargetIDs_CreatedBefore is the T81 date-filter fix: bulk
// selection can be narrowed to items created before a cutoff, so cleanup of an
// aged backlog runs through the tool instead of hand-written SQL.
func TestResolveReviewQueueTargetIDs_CreatedBefore(t *testing.T) {
	s := newMemoryTestServer(t)

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	oldItem := seedReviewQueueItem(t, s, "old review item", old)
	_ = seedReviewQueueItem(t, s, "recent review item", recent)

	ids, err := resolveReviewQueueTargetIDs(s.memoryStore, nil, memory.ProjectBankOptions{Limit: 50}, cutoff, "")
	if err != nil {
		t.Fatalf("resolveReviewQueueTargetIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != oldItem.ID {
		t.Fatalf("created_before filter: got %v, want only the old item %s", ids, oldItem.ID)
	}

	// Without the cutoff both items are selected.
	all, err := resolveReviewQueueTargetIDs(s.memoryStore, nil, memory.ProjectBankOptions{Limit: 50}, time.Time{}, "")
	if err != nil {
		t.Fatalf("resolveReviewQueueTargetIDs (no cutoff): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("no cutoff: got %d ids, want 2", len(all))
	}

	// Explicit ids bypass the date filter entirely.
	explicit, err := resolveReviewQueueTargetIDs(s.memoryStore, []string{"x", "y"}, memory.ProjectBankOptions{}, cutoff, "")
	if err != nil {
		t.Fatalf("explicit ids: %v", err)
	}
	if len(explicit) != 2 {
		t.Fatalf("explicit ids must bypass filters, got %v", explicit)
	}
}

// TestResolveReviewQueueTargetIDs_LimitAppliesAfterFilters pins T110.
//
// The filters used to run against two different populations: context/service/
// tags inside the view, which then truncated to Limit, and kind/created_before
// over that truncated page. The live symptom was a date cutoff that behaved
// like a lottery — 885 pending items, `tags` alone matched 100, the cutoff
// alone matched 5, and the two together matched 0.
//
// Seeded here at small scale with the same shape: the old items are the ones
// the view orders last (it sorts by recency), so a limit smaller than the total
// used to hide every one of them behind the recent ones.
func TestResolveReviewQueueTargetIDs_LimitAppliesAfterFilters(t *testing.T) {
	s := newMemoryTestServer(t)

	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	var oldIDs []string
	for i := range 3 {
		item := seedReviewQueueItem(t, s, "old item", time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC))
		oldIDs = append(oldIDs, item.ID)
	}
	for i := range 5 {
		seedReviewQueueItem(t, s, "recent item", time.Date(2026, 6, 1+i, 0, 0, 0, 0, time.UTC))
	}

	// A limit below the total: the cutoff must still see every candidate.
	ids, err := resolveReviewQueueTargetIDs(s.memoryStore, nil, memory.ProjectBankOptions{Limit: 4}, cutoff, "")
	if err != nil {
		t.Fatalf("resolveReviewQueueTargetIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d ids, want 3 — the cutoff must be applied before the limit, not within a page of recent items", len(ids))
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	for _, want := range oldIDs {
		if !found[want] {
			t.Errorf("old item %s missing from the selection", want)
		}
	}

	// The limit still bounds the result when more items match than requested.
	bounded, err := resolveReviewQueueTargetIDs(s.memoryStore, nil, memory.ProjectBankOptions{Limit: 2}, cutoff, "")
	if err != nil {
		t.Fatalf("resolveReviewQueueTargetIDs (bounded): %v", err)
	}
	if len(bounded) != 2 {
		t.Fatalf("got %d ids, want the limit of 2 to bound a larger match set", len(bounded))
	}
}
