package server

import (
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/hooks"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// trackedSessionForDedupTest builds a synthetic tracked session with enough
// activity to satisfy hasEnoughTrackedMaterial.
func trackedSessionForDedupTest(now time.Time, ctx string, line string) *trackedSession {
	return &trackedSession{
		startedAt:        now.Add(-10 * time.Minute),
		lastActivityAt:   now,
		lastCheckpointAt: now.Add(-10 * time.Minute),
		context:          ctx,
		mode:             memory.SessionModeCoding,
		activities: []trackedActivity{
			{Tool: "store_decision", Line: line, At: now},
		},
	}
}

func countSessionCheckpointMemories(t *testing.T, store *memory.Store, ctx string) int {
	t.Helper()
	results, err := store.List(t.Context(), memory.Filters{Context: ctx}, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, m := range results {
		for _, tag := range m.Tags {
			if tag == "session-checkpoint" {
				count++
				break
			}
		}
	}
	return count
}

// TestServerSideCheckpoint_DedupSuppressesRegeneration is the regression test
// for T45's server-side gap: the in-process auto-session pipeline used to
// bypass hooks.Check, so a second boundary persist with identical content
// would re-create a near-duplicate record. The fix mirrors the CLI gate at
// saveCheckpointWithBoundary.
func TestServerSideCheckpoint_DedupSuppressesRegeneration(t *testing.T) {
	srv := newAutoSessionTestServer(t, time.Hour, time.Hour, 1)
	tracker := srv.sessionTracker
	tracker.dedupCfg = hooks.NewDedupConfig(false, 0.9, 5, 10*time.Minute)

	ctx := "platform-service-contract-s2"
	line := "completed phase 1 deploy verification on 7 services with zero restarts"
	now := time.Now()

	// First boundary persist — must write exactly one session-checkpoint.
	tracker.saveCheckpointWithBoundary(trackedSessionForDedupTest(now, ctx, line), "checkpoint")
	if got := countSessionCheckpointMemories(t, srv.memoryStore, ctx); got != 1 {
		t.Fatalf("after first persist: expected 1 session-checkpoint, got %d", got)
	}
	if skips := srv.memoryStore.DedupSkippedByReason(); skips[hooks.ReasonSimilar] != 0 {
		t.Fatalf("first persist should not skip; got skips=%+v", skips)
	}

	// Second boundary persist with identical content — must be skipped.
	tracker.saveCheckpointWithBoundary(trackedSessionForDedupTest(now.Add(time.Minute), ctx, line), "checkpoint")
	if got := countSessionCheckpointMemories(t, srv.memoryStore, ctx); got != 1 {
		t.Fatalf("after duplicate persist: expected still 1 session-checkpoint, got %d", got)
	}
	if skips := srv.memoryStore.DedupSkippedByReason(); skips[hooks.ReasonSimilar] != 1 {
		t.Fatalf("duplicate persist should bump similar-skip counter; got skips=%+v", skips)
	}
}

// TestServerSideCheckpoint_DedupAllowsDistinctContent ensures the gate
// does not over-skip: a genuinely different summary still gets persisted.
func TestServerSideCheckpoint_DedupAllowsDistinctContent(t *testing.T) {
	srv := newAutoSessionTestServer(t, time.Hour, time.Hour, 1)
	tracker := srv.sessionTracker
	tracker.dedupCfg = hooks.NewDedupConfig(false, 0.9, 5, 10*time.Minute)

	ctx := "platform-service-contract-s2"
	now := time.Now()

	tracker.saveCheckpointWithBoundary(
		trackedSessionForDedupTest(now, ctx, "phase 1 complete: pkg/locale extracted to root module"),
		"checkpoint",
	)
	tracker.saveCheckpointWithBoundary(
		trackedSessionForDedupTest(now.Add(time.Minute), ctx, "phase 2 deploy verified: auth service running clean"),
		"checkpoint",
	)

	if got := countSessionCheckpointMemories(t, srv.memoryStore, ctx); got != 2 {
		t.Fatalf("distinct content: expected 2 session-checkpoint records, got %d", got)
	}
	if skips := srv.memoryStore.DedupSkippedByReason(); skips[hooks.ReasonSimilar] != 0 {
		t.Fatalf("distinct content: expected zero similar-skips; got %+v", skips)
	}
}

// TestServerSideCheckpoint_DedupDisabledEscapeHatch verifies the
// MCP_CHECKPOINT_DEDUP_DISABLED=true path: identical persists pass through
// (mirroring CLI behaviour), so the escape hatch survives the new gate.
func TestServerSideCheckpoint_DedupDisabledEscapeHatch(t *testing.T) {
	srv := newAutoSessionTestServer(t, time.Hour, time.Hour, 1)
	tracker := srv.sessionTracker
	tracker.dedupCfg = hooks.NewDedupConfig(true, 0.9, 5, 10*time.Minute)

	ctx := "platform-service-contract-s2"
	line := "session boundary fired with the same content twice"
	now := time.Now()

	tracker.saveCheckpointWithBoundary(trackedSessionForDedupTest(now, ctx, line), "checkpoint")
	tracker.saveCheckpointWithBoundary(trackedSessionForDedupTest(now.Add(time.Minute), ctx, line), "checkpoint")

	if got := countSessionCheckpointMemories(t, srv.memoryStore, ctx); got != 2 {
		t.Fatalf("dedup disabled: expected 2 session-checkpoint records, got %d", got)
	}
}
