package steward

import (
	"context"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func mergeAction(res *ScanResult) *Action {
	for i := range res.Actions {
		if res.Actions[i].Kind == ActionMergeDuplicates {
			return &res.Actions[i]
		}
	}
	return nil
}

// dupPair returns two same-subject-key memories (same title + context) with the
// given contents, so scanDuplicates groups them and the content-similarity guard
// decides the handling.
func dupPair(contentA, contentB string) []*memory.Memory {
	return []*memory.Memory{
		{ID: "a", Title: "Session close / deploy", Context: "deploy", Content: contentA},
		{ID: "b", Title: "Session close / deploy", Context: "deploy", Content: contentB},
	}
}

const sessionSummary = "Wrapped up the deploy pipeline work for this session and verified health."

// T69: a high-confidence duplicate group whose members are near-identical
// auto-merges when the policy opts in.
func TestScanDuplicates_AutoApply_HighSimHighConf(t *testing.T) {
	policy := DefaultPolicy()
	policy.AutoMergeDuplicateMinConfidence = 0.75
	policy.AutoMergeRequireContentSimilarity = 0.85

	res := &ScanResult{}
	scanDuplicates(dupPair(sessionSummary, sessionSummary), policy, res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingSafeAutoApply {
		t.Fatalf("expected safe_auto_apply, got %s", a.Handling)
	}
}

// T69 data-loss guard: same subject key but divergent content stays in review
// even with auto-merge enabled, because archiving a dissimilar member would lose
// unique content.
func TestScanDuplicates_ReviewRequired_LowContentSim(t *testing.T) {
	policy := DefaultPolicy()
	policy.AutoMergeDuplicateMinConfidence = 0.75
	policy.AutoMergeRequireContentSimilarity = 0.85

	res := &ScanResult{}
	scanDuplicates(dupPair(
		"Fixed the auth bug in the login handler and added regression tests.",
		"Refactored the billing invoice export to a streaming CSV writer.",
	), policy, res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingReviewRequired {
		t.Fatalf("low content similarity must stay review_required, got %s", a.Handling)
	}
}

// T69 regression: the default policy (min-confidence 0.95) never auto-merges,
// even for byte-identical duplicates.
func TestScanDuplicates_DefaultPolicy_NoAutoApply(t *testing.T) {
	res := &ScanResult{}
	scanDuplicates(dupPair(sessionSummary, sessionSummary), DefaultPolicy(), res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingReviewRequired {
		t.Fatalf("default policy must not auto-merge, got %s", a.Handling)
	}
}

// T69 legacy guard: a policy persisted before these fields existed unmarshals
// min-confidence as 0.0, which must mean "disabled", not "threshold 0".
func TestScanDuplicates_LegacyZeroPolicy_NoAutoApply(t *testing.T) {
	policy := DefaultPolicy()
	policy.AutoMergeDuplicateMinConfidence = 0 // legacy/unset

	res := &ScanResult{}
	scanDuplicates(dupPair(sessionSummary, sessionSummary), policy, res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingReviewRequired {
		t.Fatalf("zero min-confidence must disable auto-merge, got %s", a.Handling)
	}
}

// T69: a group containing a canonical member is never auto-merged — canonical
// knowledge must not be silently archived.
func TestScanDuplicates_Canonical_NotAutoMerged(t *testing.T) {
	policy := DefaultPolicy()
	policy.AutoMergeDuplicateMinConfidence = 0.75
	policy.AutoMergeRequireContentSimilarity = 0.85

	mems := dupPair(sessionSummary, sessionSummary)
	mems[1].Metadata = map[string]string{"knowledge_layer": "canonical"}

	res := &ScanResult{}
	scanDuplicates(mems, policy, res)

	a := mergeAction(res)
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.Handling != HandlingReviewRequired {
		t.Fatalf("canonical member must block auto-merge, got %s", a.Handling)
	}
}

// T69 end-to-end: with auto-merge enabled, a real steward run (dry_run=false)
// applies the merge — the duplicate is archived, not just queued for review.
func TestRun_AutoMergeDuplicates_AppliesWhenEnabled(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	for _, id := range []string{"dup-a", "dup-b"} {
		if err := store.Store(ctx, &memory.Memory{
			ID:         id,
			Title:      "Session close / deploy",
			Content:    sessionSummary,
			Type:       memory.TypeWorking,
			Context:    "deploy",
			Importance: 0.3,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	p := svc.Policy()
	p.AutoMergeDuplicateMinConfidence = 0.75
	p.AutoMergeRequireContentSimilarity = 0.85
	if err := svc.SetPolicy(p); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	report, err := svc.Run(ctx, RunParams{Scope: ScopeDuplicates, DryRun: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	a := mergeAction(&ScanResult{Actions: report.Actions})
	if a == nil {
		t.Fatal("expected a merge_duplicates action")
	}
	if a.State != StateApplied {
		t.Fatalf("expected merge applied, got state %s", a.State)
	}
	if report.Stats.ActionsApplied != 1 {
		t.Fatalf("expected 1 action applied, got %d", report.Stats.ActionsApplied)
	}
}
