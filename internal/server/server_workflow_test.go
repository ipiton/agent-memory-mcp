package server

import (
	"context"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func TestCallResolveReviewItemRemovesItemFromActiveQueue(t *testing.T) {
	s := newMemoryTestServer(t)

	item := &memory.Memory{
		Title:      "Review queue / Replace rollback runbook / api",
		Content:    "Action: merge\nHandling: hard_review\nWhy: replacement is ambiguous",
		Type:       memory.TypeWorking,
		Context:    "payments",
		Importance: 0.45,
		Tags:       []string{"review-queue", "review:required", "status:review_required", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:         string(memory.EngineeringTypeRunbook),
			memory.MetadataService:        "api",
			memory.MetadataRecordKind:     memory.RecordKindReviewQueueItem,
			memory.MetadataReviewRequired: "true",
			memory.MetadataStatus:         "review_required",
		},
	}
	if err := s.memoryStore.Store(context.Background(), item); err != nil {
		t.Fatalf("Store review item: %v", err)
	}

	result, rErr := s.callResolveReviewItem(map[string]any{
		"id":         item.ID,
		"resolution": "dismissed",
		"owner":      "platform",
		"note":       "Handled manually outside the queue",
	})
	if rErr != nil {
		t.Fatalf("callResolveReviewItem returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if !strings.Contains(toolRes.Content[0].Text, "Resolution: dismissed") {
		t.Fatalf("resolve output missing resolution:\n%s", toolRes.Content[0].Text)
	}

	updated, err := s.memoryStore.Get(item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if memory.RequiresReview(updated) {
		t.Fatalf("review item should no longer require review: %#v", updated.Metadata)
	}
	if updated.Metadata["review_resolved_by"] != "platform" {
		t.Fatalf("review_resolved_by = %q, want platform", updated.Metadata["review_resolved_by"])
	}

	view, err := s.memoryStore.ProjectBankView(context.Background(), memory.ProjectBankViewReviewQueue, memory.ProjectBankOptions{
		Filters: memory.Filters{Context: "payments"},
		Service: "api",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ProjectBankView(review_queue): %v", err)
	}
	if view.TotalCount != 0 {
		t.Fatalf("review queue should be empty after resolve, total_count = %d", view.TotalCount)
	}
}

func TestCanonicalKnowledgeTools(t *testing.T) {
	s := newMemoryTestServer(t)

	mem := &memory.Memory{
		Title:      "Canonical API rollback",
		Content:    "rollback api deployment and verify health",
		Type:       memory.TypeProcedural,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"runbook", "service:api"},
		Metadata: map[string]string{
			"entity":  "runbook",
			"service": "api",
		},
	}
	if err := s.memoryStore.Store(context.Background(), mem); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(), mem.ID, "platform"); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	listed, rErr := s.callListCanonicalKnowledge(map[string]any{
		"context": "payments",
		"service": "api",
	})
	if rErr != nil {
		t.Fatalf("callListCanonicalKnowledge returned error: %+v", rErr)
	}
	listedText := listed.(toolResult).Content[0].Text
	for _, expected := range []string{
		"Canonical knowledge",
		"Canonical API rollback",
		"layer=canonical",
	} {
		if !strings.Contains(listedText, expected) {
			t.Fatalf("canonical list missing %q:\n%s", expected, listedText)
		}
	}

	recalled, rErr := s.callRecallCanonicalKnowledge(map[string]any{
		"query":   "rollback api deployment",
		"context": "payments",
		"service": "api",
	})
	if rErr != nil {
		t.Fatalf("callRecallCanonicalKnowledge returned error: %+v", rErr)
	}
	recalledText := recalled.(toolResult).Content[0].Text
	for _, expected := range []string{
		"Canonical knowledge for 'rollback api deployment'",
		"Canonical API rollback",
		"layer=canonical",
	} {
		if !strings.Contains(recalledText, expected) {
			t.Fatalf("canonical recall missing %q:\n%s", expected, recalledText)
		}
	}
}

func TestConsolidationToolsWorkflow(t *testing.T) {
	s := newMemoryTestServer(t)

	primary := &memory.Memory{
		Title:      "Disable HPA",
		Content:    "disable hpa for api during migration",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			"entity":  "decision",
			"service": "api",
			"status":  "accepted",
		},
	}
	duplicate := &memory.Memory{
		Title:      "Disable HPA",
		Content:    "disable hpa for api during migration because rollout was unstable",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.8,
		Tags:       []string{"decision", "service:api", "migration"},
		Metadata: map[string]string{
			"entity":  "decision",
			"service": "api",
			"status":  "accepted",
		},
	}
	if err := s.memoryStore.Store(context.Background(), primary); err != nil {
		t.Fatalf("Store primary: %v", err)
	}
	if err := s.memoryStore.Store(context.Background(), duplicate); err != nil {
		t.Fatalf("Store duplicate: %v", err)
	}

	conflicts, rErr := s.callConflictsReport(map[string]any{
		"context": "payments",
		"service": "api",
		"limit":   10,
	})
	if rErr != nil {
		t.Fatalf("callConflictsReport returned error: %+v", rErr)
	}
	conflictsText := conflicts.(toolResult).Content[0].Text
	if !strings.Contains(conflictsText, "duplicate_candidates") {
		t.Fatalf("expected duplicate_candidates in report:\n%s", conflictsText)
	}

	merged, rErr := s.callMergeDuplicates(map[string]any{
		"primary_id":    primary.ID,
		"duplicate_ids": []any{duplicate.ID},
	})
	if rErr != nil {
		t.Fatalf("callMergeDuplicates returned error: %+v", rErr)
	}
	if !strings.Contains(merged.(toolResult).Content[0].Text, "Duplicates merged:") {
		t.Fatalf("unexpected merge result: %s", merged.(toolResult).Content[0].Text)
	}

	promoted, rErr := s.callPromoteToCanonical(map[string]any{
		"id":    primary.ID,
		"owner": "platform",
	})
	if rErr != nil {
		t.Fatalf("callPromoteToCanonical returned error: %+v", rErr)
	}
	if !strings.Contains(promoted.(toolResult).Content[0].Text, "Memory promoted to canonical:") {
		t.Fatalf("unexpected promote result: %s", promoted.(toolResult).Content[0].Text)
	}

	marked, rErr := s.callMarkOutdated(map[string]any{
		"id":            duplicate.ID,
		"reason":        "duplicate runbook note",
		"superseded_by": primary.ID,
	})
	if rErr != nil {
		t.Fatalf("callMarkOutdated returned error: %+v", rErr)
	}
	if !strings.Contains(marked.(toolResult).Content[0].Text, "Memory marked outdated:") {
		t.Fatalf("unexpected outdated result: %s", marked.(toolResult).Content[0].Text)
	}

	storedPrimary, err := s.memoryStore.Get(primary.ID)
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	if storedPrimary.Metadata["knowledge_layer"] != "canonical" {
		t.Fatalf("knowledge_layer = %q, want canonical", storedPrimary.Metadata["knowledge_layer"])
	}

	finalReport, err := s.memoryStore.ConflictsReport(context.Background(), memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("ConflictsReport: %v", err)
	}
	if len(finalReport) != 0 {
		t.Fatalf("expected no conflicts after merge, got %#v", finalReport)
	}
}
