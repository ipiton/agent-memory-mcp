package steward

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// A session-close record that grew: the later one holds the earlier verbatim.
const (
	earlierClose = "Assistant: checked the indexer, the main tree has no cancel mark."
	laterClose   = "User: start the task.\n\nAssistant: checked the indexer, the main tree has no cancel mark.\n\nAssistant: moved the decision into DECISIONS.md and re-ran the gates."
)

func containmentGroup() []*memory.Memory {
	// The container is deliberately second: the scanner must move it first.
	return []*memory.Memory{
		{ID: "older", Title: "Session close / task", Context: "task", Content: earlierClose},
		{ID: "newer", Title: "Session close / task", Context: "task", Content: laterClose},
	}
}

// Containment admits a group Jaccard rejects, and the container becomes the
// merge primary, so the merge adds no text to it.
func TestScanDuplicates_ContainmentAutoMergesIntoContainer(t *testing.T) {
	policy := DefaultPolicy()
	policy.AutoMergeDuplicateMinConfidence = 0.75

	res := &ScanResult{}
	scanDuplicates(containmentGroup(), policy, res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingSafeAutoApply {
		t.Fatalf("containment must auto-apply under the opt-in, got %s", a.Handling)
	}
	if a.TargetIDs[0] != "newer" {
		t.Fatalf("primary must be the container, got order %v", a.TargetIDs)
	}
	if !strings.Contains(strings.Join(a.Evidence, " "), "contained_in_primary=all") {
		t.Fatalf("evidence must name the containment proof, got %v", a.Evidence)
	}
}

// The opt-in still governs: under the default policy containment stays in
// review, but the container is already first so a manual merge is safe too.
func TestScanDuplicates_ContainmentRespectsDefaultPolicy(t *testing.T) {
	res := &ScanResult{}
	scanDuplicates(containmentGroup(), DefaultPolicy(), res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingReviewRequired {
		t.Fatalf("default policy must not auto-merge, got %s", a.Handling)
	}
	if a.TargetIDs[0] != "newer" {
		t.Fatalf("primary must be the container, got order %v", a.TargetIDs)
	}
}

// Overlap is not containment: one sentence of the shorter record missing from
// the longer one keeps the group in review.
func TestScanDuplicates_NearContainmentStaysReview(t *testing.T) {
	policy := DefaultPolicy()
	policy.AutoMergeDuplicateMinConfidence = 0.75

	group := containmentGroup()
	group[0].Content = earlierClose + " Then deleted the stale branch."

	res := &ScanResult{}
	scanDuplicates(group, policy, res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingReviewRequired {
		t.Fatalf("a member with text outside the primary must stay in review, got %s", a.Handling)
	}
}

func TestScanStale_SkipsEpisodic(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -90)

	event := &memory.Memory{ID: "event", Title: "Task complete: x", Type: memory.TypeEpisodic, Importance: 0.4, UpdatedAt: old}
	claim := &memory.Memory{ID: "claim", Title: "Pattern: guard in Makefile", Type: memory.TypeProcedural, Importance: 0.4, UpdatedAt: old}

	res := &ScanResult{}
	scanStale([]*memory.Memory{event, claim}, DefaultPolicy(), now, res)

	flagged := map[string]bool{}
	for _, a := range res.Actions {
		flagged[a.TargetIDs[0]] = true
	}
	if flagged["event"] {
		t.Error("an episodic record must not be flagged stale by age")
	}
	if !flagged["claim"] {
		t.Error("a procedural record past the threshold must still be flagged")
	}
}

func TestReconcileInboxComputableReasons(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	put := func(content string, typ memory.Type) *memory.Memory {
		m := &memory.Memory{Content: content, Type: typ, Importance: 0.6}
		if err := store.Store(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	survivor := put("group member that survived", memory.TypeSemantic)
	deleted := put("group member that was deleted", memory.TypeSemantic)
	if err := store.Delete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	event := put("Task complete: shipped the thing", memory.TypeEpisodic)
	claim := put("The guard lives in Makefile", memory.TypeProcedural)

	items := map[string]*InboxItem{
		"lone dup":       {Kind: InboxDuplicateCandidate, Title: "lone dup", TargetIDs: []string{survivor.ID, deleted.ID}},
		"stale episodic": {Kind: InboxStaleEntry, Title: "stale episodic", TargetIDs: []string{event.ID}},
		"stale claim":    {Kind: InboxStaleEntry, Title: "stale claim", TargetIDs: []string{claim.ID}},
	}
	for _, it := range items {
		if err := CreateInboxItem(svc.DB(), it); err != nil {
			t.Fatal(err)
		}
	}

	n, err := svc.reconcileInbox()
	if err != nil {
		t.Fatalf("reconcileInbox: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 reconciled (lone dup, stale episodic), got %d", n)
	}
	pending, err := svc.ListInbox(InboxQuery{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Title != "stale claim" {
		t.Fatalf("only the stale procedural claim must stay pending, got %+v", pending)
	}
}

// Resolving a page shrinks the pending set; the next page must not skip the
// items that slid into the resolved ones' places.
func TestReconcileInboxResolvesAcrossPages(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)

	const total = 1200
	for i := 0; i < total; i++ {
		item := &InboxItem{Kind: InboxDuplicateCandidate, Title: "orphan", TargetIDs: []string{"gone-a", "gone-b"}}
		if err := CreateInboxItem(svc.DB(), item); err != nil {
			t.Fatal(err)
		}
	}

	n, err := svc.reconcileInbox()
	if err != nil {
		t.Fatalf("reconcileInbox: %v", err)
	}
	if n != total {
		t.Fatalf("expected all %d orphans reconciled in one pass, got %d", total, n)
	}
}
