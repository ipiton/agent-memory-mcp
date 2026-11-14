package sessionclose

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func storeDecision(t *testing.T, store *memory.Store, text string) *memory.Memory {
	t.Helper()
	m := &memory.Memory{
		Title:      strings.TrimSuffix(strings.TrimPrefix(text, "Decision: "), "."),
		Content:    text,
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "api",
		},
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	return m
}

func sessionRequest(summary string) AnalyzeRequest {
	return AnalyzeRequest{Summary: memory.SessionSummary{
		Mode:    memory.SessionModeCoding,
		Context: "payments",
		Service: "api",
		Summary: summary,
	}}
}

func review(t *testing.T, svc *Service, summary string) *AnalysisResult {
	t.Helper()
	req := sessionRequest(summary)
	req.DryRun = true
	result, err := svc.Analyze(context.Background(), req)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if result.PlanFingerprint == "" {
		t.Fatal("review returned no plan fingerprint")
	}
	return result
}

func accept(svc *Service, summary, fingerprint string, decisions map[string]Decision) (*AnalysisResult, error) {
	req := sessionRequest(summary)
	req.SaveRaw = true
	req.AutoApplyLowRisk = true
	req.PlanFingerprint = fingerprint
	req.Decisions = decisions
	return svc.Analyze(context.Background(), req)
}

func countMemories(t *testing.T, store *memory.Store) int {
	t.Helper()
	all, err := store.List(context.Background(), memory.Filters{}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return len(all)
}

func updateActionFor(t *testing.T, result *AnalysisResult, targetID string) CandidateAction {
	t.Helper()
	for _, action := range result.Actions {
		if action.TargetMemoryID == targetID {
			return action
		}
	}
	t.Fatalf("no action targets %s: %#v", targetID, result.Actions)
	return CandidateAction{}
}

const hpaDecision = "Decision: Disable HPA for api during rollout accepted."

func TestAcceptRefusesPlanWhoseTargetChangedSinceReview(t *testing.T) {
	svc, store := newTestService(t)
	target := storeDecision(t, store, hpaDecision)

	reviewed := review(t, svc, hpaDecision)
	if action := updateActionFor(t, reviewed, target.ID); action.Kind != ActionUpdate || action.Handling != ActionHandlingAutoApply {
		t.Fatalf("reviewed action = %s/%s, want auto-applied update", action.Kind, action.Handling)
	}

	// A parallel session edits the target between review and accept.
	if err := store.Update(context.Background(), target.ID, memory.Update{Tags: []string{"decision", "service:api", "edited-elsewhere"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	before, err := store.Get(target.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	total := countMemories(t, store)

	_, err = accept(svc, hpaDecision, reviewed.PlanFingerprint, nil)
	if !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("accept error = %v, want ErrPlanChanged", err)
	}

	after, err := store.Get(target.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if memoryVersion(after) != memoryVersion(before) || after.Importance != before.Importance {
		t.Fatalf("target was written despite refusal: before %#v, after %#v", before, after)
	}
	if got := countMemories(t, store); got != total {
		t.Fatalf("memory count = %d, want %d: refused accept must not save the raw summary", got, total)
	}
}

func TestAcceptAppliesUnchangedReviewedPlanWithoutWarning(t *testing.T) {
	svc, store := newTestService(t)
	target := storeDecision(t, store, hpaDecision)

	reviewed := review(t, svc, hpaDecision)
	result, err := accept(svc, hpaDecision, reviewed.PlanFingerprint, nil)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if action := updateActionFor(t, result, target.ID); action.State != ActionStateApplied {
		t.Fatalf("update state = %s (%s), want applied", action.State, action.ExecutionNote)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", result.Warnings)
	}
}

func TestAcceptWithoutFingerprintKeepsOldBehaviourAndWarns(t *testing.T) {
	svc, store := newTestService(t)
	target := storeDecision(t, store, hpaDecision)

	result, err := accept(svc, hpaDecision, "", nil)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if action := updateActionFor(t, result, target.ID); action.State != ActionStateApplied {
		t.Fatalf("update state = %s, want applied", action.State)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "plan_fingerprint") {
		t.Fatalf("warnings = %v, want one plan_fingerprint warning", result.Warnings)
	}
}

func TestAcceptDecisionsRejectOneOfTwoActions(t *testing.T) {
	svc, store := newTestService(t)
	const cacheDecision = "Decision: Keep redis cache for api sessions accepted."
	hpa := storeDecision(t, store, hpaDecision)
	cache := storeDecision(t, store, cacheDecision)
	summary := hpaDecision + "\n" + cacheDecision

	reviewed := review(t, svc, summary)
	hpaAction := updateActionFor(t, reviewed, hpa.ID)
	cacheAction := updateActionFor(t, reviewed, cache.ID)
	if hpaAction.ID == "" || cacheAction.ID == "" || hpaAction.ID == cacheAction.ID {
		t.Fatalf("action ids = %q, %q; want two distinct ids", hpaAction.ID, cacheAction.ID)
	}

	result, err := accept(svc, summary, reviewed.PlanFingerprint, map[string]Decision{
		hpaAction.ID:   DecisionReject,
		cacheAction.ID: DecisionUpdate,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	applied := 0
	for _, action := range result.Actions {
		if action.Kind != ActionRawOnly && action.State == ActionStateApplied {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("applied knowledge actions = %d, want 1: %#v", applied, result.Actions)
	}
	if got := updateActionFor(t, result, hpa.ID); got.State != ActionStateSkipped {
		t.Fatalf("rejected action state = %s, want skipped", got.State)
	}
	if got := updateActionFor(t, result, cache.ID); got.State != ActionStateApplied {
		t.Fatalf("accepted action state = %s (%s), want applied", got.State, got.ExecutionNote)
	}
	untouched, err := store.Get(hpa.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if memoryVersion(untouched) != memoryVersion(hpa) {
		t.Fatal("rejected target was written")
	}
}

func TestAcceptDecisionsAreValidatedBeforeAnyWrite(t *testing.T) {
	svc, store := newTestService(t)
	storeDecision(t, store, hpaDecision)
	reviewed := review(t, svc, hpaDecision)
	total := countMemories(t, store)

	cases := map[string]struct {
		fingerprint string
		decisions   map[string]Decision
	}{
		"unknown id":          {reviewed.PlanFingerprint, map[string]Decision{"act_missing": DecisionReject}},
		"unknown verdict":     {reviewed.PlanFingerprint, map[string]Decision{reviewed.Actions[0].ID: "split"}},
		"without fingerprint": {"", map[string]Decision{reviewed.Actions[0].ID: DecisionReject}},
	}
	for name, tc := range cases {
		if _, err := accept(svc, hpaDecision, tc.fingerprint, tc.decisions); !errors.Is(err, ErrInvalidDecision) {
			t.Errorf("%s: error = %v, want ErrInvalidDecision", name, err)
		}
	}
	if got := countMemories(t, store); got != total {
		t.Fatalf("memory count = %d, want %d", got, total)
	}
}
