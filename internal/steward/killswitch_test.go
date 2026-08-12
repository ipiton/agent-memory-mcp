package steward

import (
	"context"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func seedExpiredWorking(t *testing.T, store *memory.Store, importance float64) *memory.Memory {
	t.Helper()
	m := &memory.Memory{
		Content:    "transient task state from a closed session",
		Type:       memory.TypeWorking,
		Importance: importance,
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Age it past any TTL the policy can carry. Steward reads the in-RAM cache,
	// so the direct UPDATE has to be followed by a reload.
	old := time.Now().UTC().AddDate(0, 0, -400)
	if _, err := store.DB().Exec(`UPDATE memories SET updated_at = ? WHERE id = ?`, old, m.ID); err != nil {
		t.Fatalf("age memory: %v", err)
	}
	if err := store.ReloadCache(); err != nil {
		t.Fatalf("ReloadCache: %v", err)
	}
	return m
}

// T104. Disabling auto-delete must revoke the action, not divert it into a
// queue that has no way to carry it out.
func TestDisabledAutoDeleteProducesNoAction(t *testing.T) {
	store := newTestStore(t)
	seedExpiredWorking(t, store, 0.2)

	policy := DefaultPolicy()
	policy.Mode = PolicyModeScheduled
	policy.AutoDeleteExpiredWorking = false

	result := RunScanners(context.Background(), store, policy, ScopeFull, "", "")

	for _, a := range result.Actions {
		if a.Kind == ActionDeleteExpiredWorking {
			t.Fatalf("scan produced %s with auto_delete_expired_working=false (handling=%s); the switch must revoke the action, not queue it", a.Kind, a.Handling)
		}
	}
}

// With the switch on, the action is produced as before — the fix must not
// silence the feature itself.
func TestEnabledAutoDeleteStillProducesAction(t *testing.T) {
	store := newTestStore(t)
	seedExpiredWorking(t, store, 0.2)

	policy := DefaultPolicy()
	policy.Mode = PolicyModeScheduled
	policy.AutoDeleteExpiredWorking = true

	result := RunScanners(context.Background(), store, policy, ScopeFull, "", "")

	found := false
	for _, a := range result.Actions {
		if a.Kind == ActionDeleteExpiredWorking {
			found = true
			if a.Handling != HandlingSafeAutoApply {
				t.Errorf("handling = %s, want %s for a low-importance expired working entry", a.Handling, HandlingSafeAutoApply)
			}
		}
	}
	if !found {
		t.Fatal("no delete_expired_working action produced with the switch on")
	}
}

// Every kind a scan can produce must have a verb able to carry it out. Adding
// an ActionKind without one puts unresolvable items in the inbox — the shape of
// the 409 stranded items measured on 2026-08-12.
func TestEveryActionKindHasAResolution(t *testing.T) {
	for _, kind := range AllActionKinds {
		verb, ok := ResolutionForActionKind[kind]
		if !ok {
			t.Errorf("ActionKind %q has no resolution verb — inbox items of this kind could only be suppressed or deferred", kind)
			continue
		}
		if !isExecutableResolution(verb) {
			t.Errorf("ActionKind %q maps to verb %q, which executeResolution does not implement", kind, verb)
		}
	}
}

// isExecutableResolution mirrors the executeResolution switch. Kept next to the
// test that uses it so a new verb has to be acknowledged in both places.
func isExecutableResolution(v ResolutionAction) bool {
	switch v {
	case ResolveMerge, ResolveMarkOutdated, ResolveMarkSuperseded, ResolvePromote,
		ResolveVerify, ResolveDelete, ResolveSuppress, ResolveDefer:
		return true
	default:
		return false
	}
}

// The delete verb actually removes the targets, so an expired-working item that
// did reach review can be resolved by its own intent.
func TestResolveInboxDeleteRemovesTargets(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	m := seedExpiredWorking(t, store, 0.9)

	item := &InboxItem{
		Kind:      InboxStaleEntry,
		Title:     "Expired working: transient task state",
		TargetIDs: []string{m.ID},
	}
	if err := CreateInboxItem(svc.db, item); err != nil {
		t.Fatalf("CreateInboxItem: %v", err)
	}

	if err := svc.ResolveInbox(item.ID, string(ResolveDelete), "expired", "operator"); err != nil {
		t.Fatalf("ResolveInbox(delete): %v", err)
	}

	if got, err := store.Get(m.ID); err == nil && got != nil {
		t.Fatal("target still present after delete resolution")
	}

	resolved, err := GetInboxItem(svc.db, item.ID)
	if err != nil {
		t.Fatalf("GetInboxItem: %v", err)
	}
	if resolved.State != InboxResolved {
		t.Fatalf("item state = %s, want resolved", resolved.State)
	}
}
