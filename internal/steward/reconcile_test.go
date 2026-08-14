package steward

import (
	"context"
	"sync/atomic"
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

// getCountingStore wraps a store and counts Get calls.
type getCountingStore struct {
	storeAPI
	gets atomic.Int32
}

func (g *getCountingStore) Get(id string) (*memory.Memory, error) {
	g.gets.Add(1)
	return g.storeAPI.Get(id)
}

// T90 M5. reconcileInbox used to call store.Get once per target id of every
// pending item — a full SQL row, embedding blob included, to answer a question
// about lifecycle. On the measured store that was ~1770 queries per run. The
// live set now comes from one cache-resident pass.
func TestReconcileInboxDoesNotQueryPerTarget(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var ids []string
	for range 12 {
		m := &memory.Memory{Content: "target of a pending inbox item", Type: memory.TypeSemantic}
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
		ids = append(ids, m.ID)
	}

	counting := &getCountingStore{storeAPI: store}
	svc, err := NewService(counting, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	for i := range ids {
		item := &InboxItem{
			Kind:      InboxStaleEntry,
			Title:     "pending item",
			TargetIDs: []string{ids[i]},
		}
		if err := CreateInboxItem(svc.db, item); err != nil {
			t.Fatalf("CreateInboxItem: %v", err)
		}
	}

	counting.gets.Store(0)
	if _, err := svc.reconcileInbox(); err != nil {
		t.Fatalf("reconcileInbox: %v", err)
	}

	if got := counting.gets.Load(); got != 0 {
		t.Fatalf("store.Get called %d times during reconcile, want 0 — the per-target N+1 is back", got)
	}
}
