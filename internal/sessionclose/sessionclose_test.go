package sessionclose

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

func newTestService(t *testing.T) (*Service, *memory.Store) {
	t.Helper()

	store, err := memory.NewStore(filepath.Join(t.TempDir(), "memory.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := New(store)
	svc.now = func() time.Time {
		return time.Date(2026, time.March, 6, 12, 0, 0, 0, time.UTC)
	}
	return svc, store
}

func TestAnalyzeReturnsRawOnlyForWeakSignal(t *testing.T) {
	svc, _ := newTestService(t)

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "payments",
			Summary: "Wrapped up the task and left a short note for tomorrow.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if len(result.Delta.ExtractedEntities) != 0 {
		t.Fatalf("expected no extracted entities, got %v", result.Delta.ExtractedEntities)
	}
	if len(result.Actions) != 1 {
		t.Fatalf("len(actions) = %d, want 1", len(result.Actions))
	}
	if result.Actions[0].Kind != ActionRawOnly {
		t.Fatalf("actions[0].Kind = %s, want %s", result.Actions[0].Kind, ActionRawOnly)
	}
	if result.Actions[0].Handling != ActionHandlingAutoApply {
		t.Fatalf("actions[0].Handling = %s, want %s", result.Actions[0].Handling, ActionHandlingAutoApply)
	}
}

func TestAnalyzeBuildsDeltaAndLinksExistingKnowledge(t *testing.T) {
	svc, store := newTestService(t)

	existing := &memory.Memory{
		Title:      "Rollback api deployment",
		Content:    "Runbook: Rollback api deployment with helm rollback and verify health",
		Type:       memory.TypeProcedural,
		Context:    "payments",
		Importance: 0.85,
		Tags:       []string{"runbook", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeRunbook),
			memory.MetadataService: "api",
		},
	}
	if err := store.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Mode:    memory.SessionModeMigration,
			Context: "payments",
			Service: "api",
			Summary: "Runbook: Rollback api deployment with helm rollback and verify health in deploy/rollout.yaml.\nDecision: Disable HPA for api during migration accepted.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if len(result.Delta.ExtractedEntities) == 0 {
		t.Fatal("expected extracted entities")
	}
	if len(result.Delta.LinkedExistingItems) != 1 || result.Delta.LinkedExistingItems[0] != existing.ID {
		t.Fatalf("linked existing items = %v, want [%s]", result.Delta.LinkedExistingItems, existing.ID)
	}
	if len(result.Delta.TouchedPaths) != 1 || result.Delta.TouchedPaths[0] != "deploy/rollout.yaml" {
		t.Fatalf("touched paths = %v, want [deploy/rollout.yaml]", result.Delta.TouchedPaths)
	}

	foundMerge := false
	foundRawOnly := false
	for _, action := range result.Actions {
		switch action.Kind {
		case ActionMerge, ActionUpdate:
			foundMerge = true
			if action.TargetMemoryID != existing.ID {
				t.Fatalf("target memory id = %q, want %q", action.TargetMemoryID, existing.ID)
			}
		case ActionRawOnly:
			foundRawOnly = true
		}
	}
	if !foundMerge {
		t.Fatalf("expected merge/update action, got %#v", result.Actions)
	}
	if !foundRawOnly {
		t.Fatalf("expected raw_only action, got %#v", result.Actions)
	}
	if result.Stats.LinkedCount != 1 {
		t.Fatalf("linked count = %d, want 1", result.Stats.LinkedCount)
	}
	if result.Review.PendingCount == 0 {
		t.Fatalf("expected pending review count > 0, got %#v", result.Review)
	}
	if len(result.AvailableActions) != 3 {
		t.Fatalf("available actions len = %d, want 3", len(result.AvailableActions))
	}
	if result.AvailableActions[0].Key != "accept_all" {
		t.Fatalf("first available action = %q, want accept_all", result.AvailableActions[0].Key)
	}
}

func TestSaveRawSummaryProtectsReservedMetadata(t *testing.T) {
	svc, store := newTestService(t)

	rawID, err := svc.SaveRawSummary(context.Background(), memory.SessionSummary{
		Context: "payments",
		Service: "api",
		Mode:    memory.SessionModeIncident,
		Summary: "Incident: api experienced degraded latency during rollout.",
		Metadata: map[string]string{
			memory.MetadataRecordKind: "override",
			memory.MetadataService:    "worker",
			"owner":                   "platform",
		},
	})
	if err != nil {
		t.Fatalf("SaveRawSummary: %v", err)
	}

	mem, err := store.Get(rawID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !memory.IsSessionSummaryMemory(mem) {
		t.Fatalf("expected session summary memory, got %#v", mem.Metadata)
	}
	if mem.Metadata[memory.MetadataRecordKind] != memory.RecordKindSessionSummary {
		t.Fatalf("record_kind = %q, want %q", mem.Metadata[memory.MetadataRecordKind], memory.RecordKindSessionSummary)
	}
	if mem.Metadata[memory.MetadataService] != "api" {
		t.Fatalf("service = %q, want api", mem.Metadata[memory.MetadataService])
	}
	if mem.Metadata["owner"] != "platform" {
		t.Fatalf("owner = %q, want platform", mem.Metadata["owner"])
	}
}

func TestAnalyzeAutoAppliesNearExactUpdate(t *testing.T) {
	svc, store := newTestService(t)

	existing := &memory.Memory{
		Title:      "Disable HPA for api during rollout accepted",
		Content:    "Decision: Disable HPA for api during rollout accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "api",
		},
	}
	if err := store.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Mode:    memory.SessionModeCoding,
			Context: "payments",
			Service: "api",
			Summary: "Decision: Disable HPA for api during rollout accepted.",
		},
		DryRun:           false,
		SaveRaw:          true,
		AutoApplyLowRisk: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if result.StateCounts[ActionStateApplied] != 2 {
		t.Fatalf("applied count = %d, want 2", result.StateCounts[ActionStateApplied])
	}
	if result.RawSummarySaved == "" {
		t.Fatal("expected raw summary to be saved")
	}

	updated, err := store.Get(existing.ID)
	if err != nil {
		t.Fatalf("Get updated: %v", err)
	}
	if updated.Metadata[memory.MetadataLastVerifiedAt] == "" {
		t.Fatalf("expected last_verified_at on updated memory, got %#v", updated.Metadata)
	}
	if updated.Importance <= existing.Importance {
		t.Fatalf("importance = %.2f, want > %.2f", updated.Importance, existing.Importance)
	}

	foundAutoUpdate := false
	for _, action := range result.Actions {
		if action.Kind == ActionUpdate {
			foundAutoUpdate = true
			if action.State != ActionStateApplied {
				t.Fatalf("update state = %s, want %s", action.State, ActionStateApplied)
			}
			if action.AppliedMemoryID != existing.ID {
				t.Fatalf("applied memory id = %q, want %q", action.AppliedMemoryID, existing.ID)
			}
		}
	}
	if !foundAutoUpdate {
		t.Fatalf("expected update action, got %#v", result.Actions)
	}
}

func TestAnalyzeQueuesAutoApplyCandidateWhenPolicyDisabled(t *testing.T) {
	svc, store := newTestService(t)

	existing := &memory.Memory{
		Title:      "Disable HPA for api during rollout accepted",
		Content:    "Decision: Disable HPA for api during rollout accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "api",
		},
	}
	if err := store.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "payments",
			Service: "api",
			Summary: "Decision: Disable HPA for api during rollout accepted.",
		},
		DryRun:  false,
		SaveRaw: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if result.StateCounts[ActionStateApplied] != 1 {
		t.Fatalf("applied count = %d, want 1", result.StateCounts[ActionStateApplied])
	}
	if result.StateCounts[ActionStateReviewRequired] != 1 {
		t.Fatalf("review_required count = %d, want 1", result.StateCounts[ActionStateReviewRequired])
	}

	foundReview := false
	for _, action := range result.Actions {
		if action.Kind == ActionUpdate {
			foundReview = true
			if action.State != ActionStateReviewRequired {
				t.Fatalf("update state = %s, want %s", action.State, ActionStateReviewRequired)
			}
			if action.ExecutionNote != "auto-apply disabled by request" {
				t.Fatalf("execution note = %q, want auto-apply disabled by request", action.ExecutionNote)
			}
		}
	}
	if !foundReview {
		t.Fatalf("expected update action, got %#v", result.Actions)
	}
}

func TestIncidentModePreventsAutoApplyForOperationalUpdate(t *testing.T) {
	svc, store := newTestService(t)

	existing := &memory.Memory{
		Title:      "Rolled back api deployment to mitigate latency spike after alert",
		Content:    "Rolled back api deployment to mitigate latency spike after alert.",
		Type:       memory.TypeEpisodic,
		Context:    "payments",
		Importance: 0.90,
		Tags:       []string{"incident", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeIncident),
			memory.MetadataService: "api",
		},
	}
	if err := store.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Mode:    memory.SessionModeIncident,
			Context: "payments",
			Service: "api",
			Summary: "Rolled back api deployment to mitigate latency spike after alert.",
		},
		DryRun:           false,
		SaveRaw:          true,
		AutoApplyLowRisk: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if result.Summary.Mode != memory.SessionModeIncident {
		t.Fatalf("mode = %s, want %s", result.Summary.Mode, memory.SessionModeIncident)
	}

	foundIncidentReview := false
	for _, action := range result.Actions {
		if action.Kind == ActionUpdate {
			foundIncidentReview = true
			if action.EngineeringType != memory.EngineeringTypeIncident {
				t.Fatalf("engineering type = %s, want %s", action.EngineeringType, memory.EngineeringTypeIncident)
			}
			if action.Handling != ActionHandlingHardReview {
				t.Fatalf("handling = %s, want %s", action.Handling, ActionHandlingHardReview)
			}
			if action.State != ActionStateReviewRequired {
				t.Fatalf("state = %s, want %s", action.State, ActionStateReviewRequired)
			}
		}
	}
	if !foundIncidentReview {
		t.Fatalf("expected incident update action, got %#v", result.Actions)
	}
}

func TestAnalyzeInfersMigrationModeAndMigrationTypeFromSummary(t *testing.T) {
	svc, _ := newTestService(t)

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "payments",
			Service: "payments-db",
			Summary: "Completed schema backfill and cutover for payments-db after dual-write verification.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if result.Summary.Mode != memory.SessionModeMigration {
		t.Fatalf("mode = %s, want %s", result.Summary.Mode, memory.SessionModeMigration)
	}

	foundMigration := false
	for _, action := range result.Actions {
		if action.Kind == ActionRawOnly {
			continue
		}
		foundMigration = true
		if action.EngineeringType != memory.EngineeringTypeMigrationNote {
			t.Fatalf("engineering type = %s, want %s", action.EngineeringType, memory.EngineeringTypeMigrationNote)
		}
		if !contains(action.DecisionTrace, "migration_mode_priority") {
			t.Fatalf("decision trace = %v, want migration_mode_priority", action.DecisionTrace)
		}
	}
	if !foundMigration {
		t.Fatalf("expected migration candidate action, got %#v", result.Actions)
	}
}

func TestFormatAnalysisIncludesSummaryReviewAndNextActions(t *testing.T) {
	svc, _ := newTestService(t)

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "payments",
			Service: "api",
			Summary: "Decision: Disable HPA for api during migration accepted.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	formatted := FormatAnalysis(result)
	for _, expected := range []string{
		"Summary:",
		"Review summary:",
		"Next actions:",
		"accept_all via accept_session_changes",
		"review_changes via review_session_changes",
		"save_raw_only via analyze_session",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("formatted analysis missing %q:\n%s", expected, formatted)
		}
	}
}

func TestAnalyzeSupersedeDetection(t *testing.T) {
	svc, store := newTestService(t)

	existing := &memory.Memory{
		Title:      "Use Redis for session cache",
		Content:    "Decision: Use Redis for session cache in auth service.",
		Type:       memory.TypeSemantic,
		Context:    "auth",
		Importance: 0.80,
		Tags:       []string{"decision", "service:auth"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "auth",
		},
	}
	if err := store.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "auth",
			Service: "auth",
			Summary: "Decision: Use Redis for session cache replaced by Valkey after license change.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	foundSupersede := false
	for _, action := range result.Actions {
		if action.Kind == ActionSupersede {
			foundSupersede = true
			if action.TargetMemoryID != existing.ID {
				t.Fatalf("target = %q, want %q", action.TargetMemoryID, existing.ID)
			}
			if action.Handling != ActionHandlingHardReview {
				t.Fatalf("handling = %s, want %s", action.Handling, ActionHandlingHardReview)
			}
			if !contains(action.DecisionTrace, "supersede_keyword_detected") {
				t.Fatalf("trace = %v, want supersede_keyword_detected", action.DecisionTrace)
			}
		}
	}
	if !foundSupersede {
		t.Fatalf("expected supersede action, got %#v", result.Actions)
	}
}

func TestAnalyzeCanonicalPromotion(t *testing.T) {
	svc, _ := newTestService(t)

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "platform",
			Service: "api",
			Summary: "Decision: Always use structured logging in api service accepted.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	foundPromote := false
	for _, action := range result.Actions {
		if action.Kind == ActionPromoteCanonical {
			foundPromote = true
			if action.Handling != ActionHandlingHardReview {
				t.Fatalf("handling = %s, want %s", action.Handling, ActionHandlingHardReview)
			}
			if !contains(action.DecisionTrace, "high_value_engineering_item") {
				t.Fatalf("trace = %v, want high_value_engineering_item", action.DecisionTrace)
			}
		}
	}
	if !foundPromote {
		t.Fatalf("expected promote_canonical action, got %#v", result.Actions)
	}
}

func TestAnalyzeNewActionWhenNoExistingMatch(t *testing.T) {
	svc, _ := newTestService(t)

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "billing",
			Service: "payments",
			Summary: "Runbook: Rotate Stripe API keys in payments service using vault.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	foundNew := false
	for _, action := range result.Actions {
		if action.Kind == ActionNew {
			foundNew = true
			if action.Confidence != 0.60 {
				t.Fatalf("confidence = %.2f, want 0.60", action.Confidence)
			}
		}
	}
	if !foundNew {
		t.Fatalf("expected new action, got %#v", result.Actions)
	}
}

func TestAnalyzeMergeActionForHighOverlap(t *testing.T) {
	svc, store := newTestService(t)

	existing := &memory.Memory{
		Title:      "Rotate Stripe API keys using vault",
		Content:    "Runbook: Rotate Stripe API keys in payments service using vault and restart workers.",
		Type:       memory.TypeProcedural,
		Context:    "billing",
		Importance: 0.80,
		Tags:       []string{"runbook", "service:payments"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeRunbook),
			memory.MetadataService: "payments",
		},
	}
	if err := store.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "billing",
			Service: "payments",
			Summary: "Runbook: Rotate Stripe API keys in payments service using vault and restart workers after rotation.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	foundMergeOrUpdate := false
	for _, action := range result.Actions {
		if action.Kind == ActionMerge || action.Kind == ActionUpdate {
			foundMergeOrUpdate = true
			if action.TargetMemoryID != existing.ID {
				t.Fatalf("target = %q, want %q", action.TargetMemoryID, existing.ID)
			}
			if action.Confidence < 0.82 {
				t.Fatalf("confidence = %.2f, want >= 0.82", action.Confidence)
			}
			if action.Handling != ActionHandlingSoftReview && action.Handling != ActionHandlingAutoApply {
				t.Fatalf("handling = %s, want soft_review or safe_auto_apply", action.Handling)
			}
		}
	}
	if !foundMergeOrUpdate {
		t.Fatalf("expected merge or update action, got %#v", result.Actions)
	}
}

func TestAnalyzeLowConfidenceGetsHardReview(t *testing.T) {
	svc, _ := newTestService(t)

	result, err := svc.Analyze(context.Background(), AnalyzeRequest{
		Summary: memory.SessionSummary{
			Context: "infra",
			Service: "worker",
			Summary: "Runbook: Rotate Stripe API keys in payments service using vault.",
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	for _, action := range result.Actions {
		if action.Kind == ActionRawOnly {
			continue
		}
		if action.Confidence > 0 && action.Confidence < 0.70 {
			if action.Handling != ActionHandlingHardReview {
				t.Fatalf("low confidence action handling = %s, want %s", action.Handling, ActionHandlingHardReview)
			}
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
