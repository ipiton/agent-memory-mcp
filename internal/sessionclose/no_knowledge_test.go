package sessionclose

import (
	"context"
	"errors"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T122 root cause: hooks.Check carried the no-knowledge guard, but only the two
// session-checkpoint callers invoked it. This function — the terminal session
// summary write — did not, and the live bank had 75 such records to prove it
// (against 0 on the guarded path), still arriving daily.
func TestSaveRawSummaryRefusesActivityLog(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	journal := memory.SessionSummary{
		Context: "canon-promotion",
		Summary: "- Promoted canonical: 68d40320-1111-4c11-9c11-111111111111\n" +
			"- Promoted canonical: aa0bfc8f-2222-4c22-9c22-222222222222\n" +
			"- Document search: провенанс канона",
	}
	id, err := svc.SaveRawSummary(ctx, journal)
	if !errors.Is(err, ErrNoKnowledge) {
		t.Fatalf("SaveRawSummary(activity log) error = %v, want ErrNoKnowledge", err)
	}
	if id != "" {
		t.Errorf("SaveRawSummary returned id %q for a refused write", id)
	}
	if n := store.Count(); n != 0 {
		t.Errorf("store holds %d records after a refused write, want 0", n)
	}
}

// The guard must not become a way to lose real reports: one line of knowledge
// among the bullets and the summary is knowledge.
func TestSaveRawSummaryKeepsMixedSummary(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	mixed := memory.SessionSummary{
		Context: "canon-promotion",
		Summary: "- Document search: провенанс канона\n" +
			"Промоушен требует доверенного провенанса, иначе запись уходит в review.",
	}
	id, err := svc.SaveRawSummary(ctx, mixed)
	if err != nil {
		t.Fatalf("SaveRawSummary(mixed) = %v, want success", err)
	}
	if id == "" {
		t.Fatal("SaveRawSummary returned an empty id for a real summary")
	}
	if n := store.Count(); n != 1 {
		t.Errorf("store holds %d records, want 1", n)
	}
}
