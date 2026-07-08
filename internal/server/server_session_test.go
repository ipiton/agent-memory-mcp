package server

import (
	"context"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func TestCallAnalyzeSessionDryRunReturnsReviewAwareReport(t *testing.T) {
	s := newMemoryTestServer(t)

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
	if err := s.memoryStore.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, rErr := s.callCloseSession(map[string]any{
		"summary": "Runbook: Rollback api deployment with helm rollback and verify health.\nDecision: Disable HPA for api during migration accepted.",
		"context": "payments",
		"service": "api",
		"mode":    "migration",
		"dry_run": true,
	})
	if rErr != nil {
		t.Fatalf("callCloseSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (dry-run)",
		"Planned actions:",
		"Handling:",
		"safe_auto_apply",
		"hard_review",
		"RAW_ONLY",
		"MERGE",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("analysis output missing %q:\n%s", expected, text)
		}
	}
	if s.memoryStore.Count() != 1 {
		t.Fatalf("dry-run should not persist raw summary, count = %d", s.memoryStore.Count())
	}
}

func TestCallAnalyzeSessionSaveRawPersistsSessionSummary(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callCloseSession(map[string]any{
		"summary":    "Decision: Disable HPA for api during migration accepted.",
		"context":    "payments",
		"service":    "api",
		"mode":       "migration",
		"save_raw":   true,
		"format":     "json",
		"started_at": "2026-03-06T10:00:00Z",
		"ended_at":   "2026-03-06T10:30:00Z",
		"metadata": map[string]any{
			memory.MetadataRecordKind: "override-me",
			"owner":                   "platform",
		},
	})
	if rErr != nil {
		t.Fatalf("callCloseSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if !strings.Contains(toolRes.Content[0].Text, "\"raw_summary_saved\"") {
		t.Fatalf("expected json output with raw_summary_saved, got:\n%s", toolRes.Content[0].Text)
	}

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	mem := memories[0]
	if !memory.IsSessionSummaryMemory(mem) {
		t.Fatalf("expected session summary memory, got metadata %#v", mem.Metadata)
	}
	if mem.Metadata[memory.MetadataRecordKind] != memory.RecordKindSessionSummary {
		t.Fatalf("record_kind = %q, want %q", mem.Metadata[memory.MetadataRecordKind], memory.RecordKindSessionSummary)
	}
	if mem.Metadata["owner"] != "platform" {
		t.Fatalf("owner metadata = %q, want platform", mem.Metadata["owner"])
	}
	if mem.Metadata[memory.MetadataSessionMode] != string(memory.SessionModeMigration) {
		t.Fatalf("session_mode = %q, want %q", mem.Metadata[memory.MetadataSessionMode], memory.SessionModeMigration)
	}
}

func TestCallAnalyzeSessionAutoApplyLowRiskReturnsAppliedState(t *testing.T) {
	s := newMemoryTestServer(t)

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
	if err := s.memoryStore.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, rErr := s.callCloseSession(map[string]any{
		"summary":             "Decision: Disable HPA for api during rollout accepted.",
		"context":             "payments",
		"service":             "api",
		"mode":                "coding",
		"save_raw":            true,
		"auto_apply_low_risk": true,
	})
	if rErr != nil {
		t.Fatalf("callCloseSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (write-enabled)",
		"Execution state:",
		"applied: 2",
		"UPDATE",
		"State: applied",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("analysis output missing %q:\n%s", expected, text)
		}
	}
}

func TestCallReviewSessionChangesForcesDryRunAndHighlightsNextActions(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callReviewSessionChanges(map[string]any{
		"summary":  "Decision: Disable HPA for api during migration accepted.",
		"context":  "payments",
		"service":  "api",
		"save_raw": true,
	})
	if rErr != nil {
		t.Fatalf("callReviewSessionChanges returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (dry-run)",
		"Next actions:",
		"review_changes via review_session_changes",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("review output missing %q:\n%s", expected, text)
		}
	}
	if s.memoryStore.Count() != 0 {
		t.Fatalf("review should not persist memories, count = %d", s.memoryStore.Count())
	}
}

func TestCallAcceptSessionChangesUsesWriteEnabledDefaults(t *testing.T) {
	s := newMemoryTestServer(t)

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
	if err := s.memoryStore.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	result, rErr := s.callAcceptSessionChanges(map[string]any{
		"summary": "Decision: Disable HPA for api during rollout accepted.",
		"context": "payments",
		"service": "api",
		"mode":    "coding",
	})
	if rErr != nil {
		t.Fatalf("callAcceptSessionChanges returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Session analysis (write-enabled)",
		"Raw summary saved as memory",
		"State: applied",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("accept output missing %q:\n%s", expected, text)
		}
	}
	if s.memoryStore.Count() != 2 {
		t.Fatalf("expected updated memory + raw summary, count = %d", s.memoryStore.Count())
	}
}

func TestCallAnalyzeSessionInfersMigrationModeFromSummary(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callCloseSession(map[string]any{
		"summary": "Completed schema backfill and cutover for payments-db after dual-write verification.",
		"context": "payments",
		"service": "payments-db",
	})
	if rErr != nil {
		t.Fatalf("callCloseSession returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Mode: migration",
		"migration_mode_priority",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("analysis output missing %q:\n%s", expected, text)
		}
	}
}
