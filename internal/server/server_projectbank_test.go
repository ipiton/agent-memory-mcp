package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func TestSummarizeProjectContextIncludesWorkflowSections(t *testing.T) {
	s := newMemoryTestServer(t)

	for _, mem := range []*memory.Memory{
		{
			Title:      "Disable HPA",
			Content:    "Decision: Disable HPA\nRationale: rollout stability",
			Type:       memory.TypeSemantic,
			Context:    "payments",
			Importance: 0.8,
			Tags:       []string{"decision", "service:api"},
		},
		{
			Title:      "API rollback",
			Content:    "Procedure: Roll back the deployment",
			Type:       memory.TypeProcedural,
			Context:    "payments",
			Importance: 0.8,
			Tags:       []string{"runbook", "service:api"},
		},
		{
			Title:      "Ingress outage",
			Content:    "Incident: ingress outage\nResolution: restart controller",
			Type:       memory.TypeEpisodic,
			Context:    "payments",
			Importance: 0.9,
			Tags:       []string{"incident", "service:api"},
		},
	} {
		if err := s.memoryStore.Store(context.Background(), mem); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	memories, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "payments"}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	canonicalID := ""
	for _, mem := range memories {
		if mem.Title == "Disable HPA" {
			canonicalID = mem.ID
			break
		}
	}
	if canonicalID == "" {
		t.Fatal("failed to find decision memory to promote")
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(), canonicalID, "platform", true); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	result, rErr := s.callSummarizeProjectContext(map[string]any{
		"context": "payments",
		"service": "api",
		"limit":   5,
	})
	if rErr != nil {
		t.Fatalf("callSummarizeProjectContext returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Project context summary",
		"Canonical knowledge",
		"Recent decisions",
		"Runbooks",
		"Incidents and postmortems",
		"Disable HPA",
		"API rollback",
		"Ingress outage",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, text)
		}
	}
}

func TestCallProjectBankViewOverviewShowsCanonicalSessionAndAttentionSections(t *testing.T) {
	s := newMemoryTestServer(t)

	decision := &memory.Memory{
		Title:      "Disable HPA",
		Content:    "Decision: Disable HPA for api during rollout accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.85,
		Tags:       []string{"decision", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "api",
			memory.MetadataStatus:  "accepted",
		},
	}
	if err := s.memoryStore.Store(context.Background(), decision); err != nil {
		t.Fatalf("Store decision: %v", err)
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(), decision.ID, "platform", true); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	session := &memory.Memory{
		Title:      "Session close / payments / api",
		Content:    "Migration summary for payments api rollout.",
		Type:       memory.TypeEpisodic,
		Context:    "payments",
		Importance: 0.20,
		Tags:       []string{"session-summary", "service:api"},
		Metadata: map[string]string{
			memory.MetadataRecordKind:  memory.RecordKindSessionSummary,
			memory.MetadataService:     "api",
			memory.MetadataSessionMode: string(memory.SessionModeMigration),
		},
	}
	if err := s.memoryStore.Store(context.Background(), session); err != nil {
		t.Fatalf("Store session summary: %v", err)
	}

	stale := &memory.Memory{
		Title:      "Known rollout caveat",
		Content:    "Caveat: verification is still manual after rollout.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.60,
		Tags:       []string{"caveat", "service:api"},
		Metadata: map[string]string{
			memory.MetadataEntity:          string(memory.EngineeringTypeCaveat),
			memory.MetadataService:         "api",
			memory.MetadataLifecycleStatus: string(memory.LifecycleOutdated),
			memory.MetadataReviewRequired:  "true",
		},
	}
	if err := s.memoryStore.Store(context.Background(), stale); err != nil {
		t.Fatalf("Store stale caveat: %v", err)
	}

	result, rErr := s.callProjectBankView(map[string]any{
		"view":    "overview",
		"context": "payments",
		"service": "api",
	})
	if rErr != nil {
		t.Fatalf("callProjectBankView returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	text := toolRes.Content[0].Text
	for _, expected := range []string{
		"Project bank view: Canonical overview",
		"Canonical knowledge (1):",
		"Recent session deltas (1):",
		"Needs review or refresh (1):",
		"Session mode: migration",
		"Layer: canonical",
		"Review: required",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("project bank overview missing %q:\n%s", expected, text)
		}
	}
}

func TestCallProjectBankViewJSONAppliesStatusOwnerAndServiceFilters(t *testing.T) {
	s := newMemoryTestServer(t)

	canonicalDecision := &memory.Memory{
		Title:      "Use maintenance window",
		Content:    "Decision: Use a maintenance window for payments-db cutover accepted.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.80,
		Tags:       []string{"decision", "service:payments-db"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "payments-db",
			memory.MetadataStatus:  "accepted",
		},
	}
	if err := s.memoryStore.Store(context.Background(), canonicalDecision); err != nil {
		t.Fatalf("Store canonical decision: %v", err)
	}
	if _, err := s.memoryStore.PromoteToCanonical(context.Background(), canonicalDecision.ID, "platform", true); err != nil {
		t.Fatalf("PromoteToCanonical: %v", err)
	}

	rawDecision := &memory.Memory{
		Title:      "Temporary rollout note",
		Content:    "Decision: Disable background workers during cutover.",
		Type:       memory.TypeSemantic,
		Context:    "payments",
		Importance: 0.70,
		Tags:       []string{"decision", "service:payments-db"},
		Metadata: map[string]string{
			memory.MetadataEntity:  string(memory.EngineeringTypeDecision),
			memory.MetadataService: "payments-db",
			memory.MetadataStatus:  "draft",
		},
	}
	if err := s.memoryStore.Store(context.Background(), rawDecision); err != nil {
		t.Fatalf("Store raw decision: %v", err)
	}

	result, rErr := s.callProjectBankView(map[string]any{
		"view":    "decisions",
		"context": "payments",
		"service": "payments-db",
		"status":  "canonical",
		"owner":   "platform",
		"format":  "json",
	})
	if rErr != nil {
		t.Fatalf("callProjectBankView returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}

	var payload memory.ProjectBankViewResult
	if err := json.Unmarshal([]byte(toolRes.Content[0].Text), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload.View != memory.ProjectBankViewDecisions {
		t.Fatalf("view = %q, want %q", payload.View, memory.ProjectBankViewDecisions)
	}
	if len(payload.Sections) != 1 || len(payload.Sections[0].Items) != 1 {
		t.Fatalf("unexpected sections payload: %#v", payload.Sections)
	}
	item := payload.Sections[0].Items[0]
	if item.Title != canonicalDecision.Title {
		t.Fatalf("title = %q, want %q", item.Title, canonicalDecision.Title)
	}
	if item.Owner != "platform" {
		t.Fatalf("owner = %q, want platform", item.Owner)
	}
	if item.Lifecycle != memory.LifecycleCanonical {
		t.Fatalf("lifecycle = %q, want %q", item.Lifecycle, memory.LifecycleCanonical)
	}
}
