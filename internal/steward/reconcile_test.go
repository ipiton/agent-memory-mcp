package steward

import (
	"context"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// TestReconcileInboxResolvesGoneTargets is the T81 orphan fix: a pending inbox
// item whose targets are all gone (deleted here) is auto-resolved as obsolete,
// instead of lingering pending forever.
func TestReconcileInboxResolvesGoneTargets(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	m := &memory.Memory{Content: "since-deleted decision", Type: memory.TypeSemantic, Importance: 0.6}
	if err := store.Store(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, m.ID); err != nil { // target is now gone
		t.Fatal(err)
	}

	item := &InboxItem{Kind: InboxDuplicateCandidate, Title: "orphaned dup", TargetIDs: []string{m.ID}}
	if err := CreateInboxItem(svc.DB(), item); err != nil {
		t.Fatal(err)
	}

	n, err := svc.reconcileInbox()
	if err != nil {
		t.Fatalf("reconcileInbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 item reconciled, got %d", n)
	}
	pending, _ := svc.ListInbox(InboxQuery{Status: "pending"})
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending after reconcile, got %d", len(pending))
	}
}

// TestReconcileInboxKeepsLiveTargets: an item whose target still exists and is
// active must NOT be reconciled away.
func TestReconcileInboxKeepsLiveTargets(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	m := &memory.Memory{Content: "still-live decision", Type: memory.TypeSemantic, Importance: 0.6}
	if err := store.Store(ctx, m); err != nil {
		t.Fatal(err)
	}

	item := &InboxItem{Kind: InboxDuplicateCandidate, Title: "live dup", TargetIDs: []string{m.ID}}
	if err := CreateInboxItem(svc.DB(), item); err != nil {
		t.Fatal(err)
	}

	n, err := svc.reconcileInbox()
	if err != nil {
		t.Fatalf("reconcileInbox: %v", err)
	}
	if n != 0 {
		t.Fatalf("live-target item must not be reconciled, got %d resolved", n)
	}
	pending, _ := svc.ListInbox(InboxQuery{Status: "pending"})
	if len(pending) != 1 {
		t.Fatalf("expected item still pending, got %d", len(pending))
	}
}
