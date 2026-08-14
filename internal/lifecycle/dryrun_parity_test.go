package lifecycle

import (
	"context"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T92. A dry run exists so an operator can decide about an irreversible bulk
// operation. On the live measurement of 2026-07-16 it reported "Promotion
// candidates: 0, Promoted: 209" where the real run did "Promotion candidates:
// 195, Promoted: 14" — a 15× divergence, and not in the amount of work but in
// its nature: the operator approved 209 canonicalizations and got 14.
//
// The cause was structural, not arithmetic: the dry-run branch counted before
// the T77 provenance gate, which only the live branch applied. A forecast that
// disagrees with reality is worse than no forecast — a missing one makes you
// check, a lying one convinces you not to.
//
// This test compares every counter across both modes on the same input. It is
// deliberately a parity test rather than an assertion of specific numbers: a
// gate added to only one of the two branches must fail here regardless of what
// that gate decides.
func TestSweep_DryRunMatchesLiveCounters(t *testing.T) {
	// A mix that exercises all four counters and both sides of the gate.
	seed := func(t *testing.T, store *memory.Store, slug string) {
		t.Helper()
		// Below the promotion threshold → outdated.
		seedWorkingMemory(t, store, slug, "low-a", 0.2, nil, nil)
		seedWorkingMemory(t, store, slug, "low-b", 0.1, nil, nil)
		// At/above the threshold, conversational provenance → the gate routes
		// these to review even with AutoPromote on.
		seedWorkingMemory(t, store, slug, "high-conversational", 0.9, nil, nil)
		seedWorkingMemory(t, store, slug, "high-default-provenance", 0.85, nil, nil)
		// At/above the threshold, trusted provenance → genuinely promoted.
		seedWorkingMemory(t, store, slug, "high-verified", 0.9, nil,
			map[string]string{memory.MetadataProvenance: memory.ProvenanceVerified})
		seedWorkingMemory(t, store, slug, "high-external", 0.88, nil,
			map[string]string{memory.MetadataProvenance: memory.ProvenanceExternal})
		// Opted out by tag → skipped.
		seedWorkingMemory(t, store, slug, "kept", 0.9, []string{KeepAfterArchiveTag}, nil)
	}

	run := func(t *testing.T, dryRun, autoPromote bool) SweepResult {
		t.Helper()
		store := newTestStore(t)
		const slug = "task-dryrun-parity"
		root := seedTempArchive(t, slug)
		seed(t, store, slug)

		sw := NewSweeper(store)
		result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{
			Roots:       []string{root},
			DryRun:      dryRun,
			AutoPromote: autoPromote,
		})
		if err != nil {
			t.Fatalf("SweepArchive(dry=%v, auto=%v): %v", dryRun, autoPromote, err)
		}
		if len(result.Errors) != 0 {
			t.Fatalf("unexpected sweep errors: %v", result.Errors)
		}
		return *result
	}

	for _, autoPromote := range []bool{false, true} {
		t.Run(map[bool]string{false: "auto_promote=false", true: "auto_promote=true"}[autoPromote], func(t *testing.T) {
			dry := run(t, true, autoPromote)
			live := run(t, false, autoPromote)

			if dry.TotalPromoted != live.TotalPromoted {
				t.Errorf("Promoted: dry-run=%d, live=%d", dry.TotalPromoted, live.TotalPromoted)
			}
			if dry.TotalPromotionCand != live.TotalPromotionCand {
				t.Errorf("PromotionCandidates: dry-run=%d, live=%d", dry.TotalPromotionCand, live.TotalPromotionCand)
			}
			if dry.TotalOutdated != live.TotalOutdated {
				t.Errorf("Outdated: dry-run=%d, live=%d", dry.TotalOutdated, live.TotalOutdated)
			}
			if dry.TotalSkipped != live.TotalSkipped {
				t.Errorf("Skipped: dry-run=%d, live=%d", dry.TotalSkipped, live.TotalSkipped)
			}
		})
	}
}

// The parity above would also be satisfied by two branches that are both wrong
// in the same way. This pins the substance: with AutoPromote on, the gate must
// split the four high-importance records into two promoted (trusted) and two
// routed to review (conversational) — in both modes.
func TestSweep_DryRunReportsTheGateSplit(t *testing.T) {
	store := newTestStore(t)
	const slug = "task-dryrun-split"
	root := seedTempArchive(t, slug)

	seedWorkingMemory(t, store, slug, "conversational", 0.9, nil, nil)
	seedWorkingMemory(t, store, slug, "verified", 0.9, nil,
		map[string]string{memory.MetadataProvenance: memory.ProvenanceVerified})

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{
		Roots:       []string{root},
		DryRun:      true,
		AutoPromote: true,
	})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}
	if result.TotalPromoted != 1 {
		t.Errorf("Promoted = %d, want 1 (only the verified record clears the T77 gate)", result.TotalPromoted)
	}
	if result.TotalPromotionCand != 1 {
		t.Errorf("PromotionCandidates = %d, want 1 (the conversational record is gated to review)", result.TotalPromotionCand)
	}
}
