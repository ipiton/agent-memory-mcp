package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
)

func TestFormatSearchResultsIncludesDebug(t *testing.T) {
	s := newTestServer(t, "")

	formatted := s.formatSearchResults(&rag.SearchResponse{
		Query: "ingress rollback",
		Results: []rag.SearchResult{
			{
				Title:      "Ingress rollback",
				Path:       "runbooks/ingress-rollback.md",
				SourceType: "runbook",
				Score:      0.91,
				Snippet:    "Rollback steps",
				Trust: &trust.Metadata{
					KnowledgeLayer: "document",
					SourceType:     "runbook",
					Confidence:     0.94,
					FreshnessScore: 0.80,
					Owner:          "operations",
					LastVerifiedAt: time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC),
				},
				Debug: &rag.ResultDebug{
					Breakdown: rag.ScoreBreakdown{
						Semantic:          0.62,
						KeywordRaw:        2.10,
						KeywordNormalized: 1.00,
						RecencyBoost:      0.04,
						SourceBoost:       0.08,
						ConfidenceBoost:   0.02,
						FinalScore:        0.91,
					},
					AppliedBoosts: []string{"semantic_similarity", "keyword_match", "source_type:runbook(+0.080)", "trust_confidence(+0.020)"},
				},
			},
		},
		SearchTime: 12,
		Debug: &rag.SearchDebug{
			AppliedFilters:   []string{"source_type=runbook"},
			RankingSignals:   []string{"semantic_similarity", "keyword_bm25_like", "recency_boost", "trust_confidence", "freshness_score"},
			IndexedChunks:    10,
			FilteredOut:      4,
			DiscardedAsNoise: 2,
			CandidateCount:   4,
			ReturnedCount:    1,
		},
	})

	for _, expected := range []string{
		"Applied filters: source_type=runbook",
		"Ranking signals: semantic_similarity, keyword_bm25_like, recency_boost, trust_confidence, freshness_score",
		"Trust: layer=document source=runbook confidence=0.94 freshness=0.80 owner=operations verified=2026-03-01T12:00:00Z",
		"Score breakdown: semantic=0.620",
		"confidence=0.020",
		"Applied boosts: semantic_similarity, keyword_match, source_type:runbook(+0.080), trust_confidence(+0.020)",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("formatted output missing %q:\n%s", expected, formatted)
		}
	}
}

func TestCallStoreDecisionStoresWorkflowMemory(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callStoreDecision(map[string]any{
		"decision":   "Disable HPA for api during migration",
		"rationale":  "Pods were thrashing during rollout",
		"service":    "api",
		"status":     "accepted",
		"context":    "payments",
		"tags":       []any{"migration"},
		"importance": 0.9,
	})
	if rErr != nil {
		t.Fatalf("callStoreDecision returned error: %+v", rErr)
	}

	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if !strings.Contains(toolRes.Content[0].Text, "Decision stored:") {
		t.Fatalf("unexpected result text: %s", toolRes.Content[0].Text)
	}

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}

	mem := memories[0]
	if mem.Type != memory.TypeSemantic {
		t.Fatalf("mem.Type = %s, want %s", mem.Type, memory.TypeSemantic)
	}
	if mem.Metadata["last_verified_at"] == "" {
		t.Fatalf("expected last_verified_at metadata, got %#v", mem.Metadata)
	}
	for _, tag := range []string{"decision", "service:api", "status:accepted", "migration"} {
		if !containsTag(mem.Tags, tag) {
			t.Fatalf("missing tag %q in %v", tag, mem.Tags)
		}
	}
	if mem.Metadata["entity"] != "decision" {
		t.Fatalf("metadata entity = %q, want decision", mem.Metadata["entity"])
	}
	if !strings.Contains(mem.Content, "Rationale: Pods were thrashing during rollout") {
		t.Fatalf("unexpected content: %s", mem.Content)
	}
}

func TestCallStoreDeadEndStoresWorkflowMemory(t *testing.T) {
	s := newMemoryTestServer(t)

	result, rErr := s.callStoreDeadEnd(map[string]any{
		"attempted_approach": "use async migration in place without shadow write",
		"why_failed":         "lost messages during cutover because readers lagged",
		"alternative_used":   "dual-write phase with shadow consumer for two weeks",
		"related_task_slug":  "T-12345-async-migration",
		"service":            "catalog",
		"context":            "payments",
		"tags":               []any{"migration"},
		"importance":         0.82,
	})
	if rErr != nil {
		t.Fatalf("callStoreDeadEnd returned error: %+v", rErr)
	}
	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if !strings.Contains(toolRes.Content[0].Text, "Dead End stored:") {
		t.Fatalf("unexpected result text: %s", toolRes.Content[0].Text)
	}

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	mem := memories[0]
	if mem.Type != memory.TypeSemantic {
		t.Fatalf("mem.Type = %s, want %s", mem.Type, memory.TypeSemantic)
	}
	if mem.Metadata[memory.MetadataEntity] != string(memory.EngineeringTypeDeadEnd) {
		t.Fatalf("metadata entity = %q, want dead_end", mem.Metadata[memory.MetadataEntity])
	}
	if !containsTag(mem.Tags, "dead_end") {
		t.Fatalf("missing dead_end tag in %v", mem.Tags)
	}
	if mem.Metadata["alternative_used"] != "dual-write phase with shadow consumer for two weeks" {
		t.Fatalf("missing alternative_used metadata, got %#v", mem.Metadata)
	}
	if mem.Metadata["related_task_slug"] != "T-12345-async-migration" {
		t.Fatalf("missing related_task_slug metadata, got %#v", mem.Metadata)
	}
	if !strings.Contains(mem.Content, "Attempted approach:") || !strings.Contains(mem.Content, "Why failed:") {
		t.Fatalf("unexpected content: %s", mem.Content)
	}
}

func TestCallStoreDeadEndRequiresAttemptedAndWhyFailed(t *testing.T) {
	s := newMemoryTestServer(t)

	_, rErr := s.callStoreDeadEnd(map[string]any{"why_failed": "x"})
	if rErr == nil || rErr.Code != rpcErrInvalidParams {
		t.Fatalf("expected invalid params for missing attempted_approach, got %+v", rErr)
	}
	_, rErr = s.callStoreDeadEnd(map[string]any{"attempted_approach": "try something"})
	if rErr == nil || rErr.Code != rpcErrInvalidParams {
		t.Fatalf("expected invalid params for missing why_failed, got %+v", rErr)
	}
}

func TestCallStoreDecisionRecordsAvoidedDeadEndID(t *testing.T) {
	s := newMemoryTestServer(t)

	_, rErr := s.callStoreDecision(map[string]any{
		"decision":            "use dual-write with shadow consumer",
		"rationale":           "avoids data loss during async migration",
		"service":             "catalog",
		"status":              "accepted",
		"avoided_dead_end_id": "mem-dead-001",
	})
	if rErr != nil {
		t.Fatalf("callStoreDecision returned error: %+v", rErr)
	}

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	if got := memories[0].Metadata["avoided_dead_end_id"]; got != "mem-dead-001" {
		t.Fatalf("avoided_dead_end_id metadata = %q, want mem-dead-001", got)
	}
}

// TestCallStoreDecision_IncrementsReferencedByCountOnAvoidedDeadEnd asserts
// that callStoreDecision with a real avoided_dead_end_id bumps the
// referenced_by_count on the dead-end memory. Activates the T48 semantic→
// character "by refs" rule.
func TestCallStoreDecision_IncrementsReferencedByCountOnAvoidedDeadEnd(t *testing.T) {
	s := newMemoryTestServer(t)

	// Seed a real dead-end first so its ID is addressable by the decision.
	deadEnd := &memory.Memory{
		Title:      "Dead end A",
		Content:    "abandoned because foo",
		Type:       memory.TypeSemantic,
		Importance: 0.5,
	}
	if err := s.memoryStore.Store(context.Background(), deadEnd); err != nil {
		t.Fatalf("Store dead-end: %v", err)
	}

	_, rErr := s.callStoreDecision(map[string]any{
		"decision":            "use approach X",
		"rationale":           "avoids the pitfall documented in dead-end A",
		"service":             "catalog",
		"status":              "accepted",
		"avoided_dead_end_id": deadEnd.ID,
	})
	if rErr != nil {
		t.Fatalf("callStoreDecision: %+v", rErr)
	}

	after, err := s.memoryStore.Get(deadEnd.ID)
	if err != nil {
		t.Fatalf("Get dead-end: %v", err)
	}
	if got := after.Metadata[memory.MetadataReferencedByCount]; got != "1" {
		t.Fatalf("referenced_by_count = %q, want 1", got)
	}
}

// TestCallStoreDecision_NoIncrement_WhenAvoidedDeadEndIDEmpty verifies that
// when avoided_dead_end_id is omitted/empty, no counter increment side
// effect touches any existing memory.
func TestCallStoreDecision_NoIncrement_WhenAvoidedDeadEndIDEmpty(t *testing.T) {
	s := newMemoryTestServer(t)

	existing := &memory.Memory{
		Title:      "Unrelated",
		Content:    "does not participate in the graph edge",
		Type:       memory.TypeSemantic,
		Importance: 0.5,
	}
	if err := s.memoryStore.Store(context.Background(), existing); err != nil {
		t.Fatalf("Store existing: %v", err)
	}

	_, rErr := s.callStoreDecision(map[string]any{
		"decision":  "ship it",
		"rationale": "no blockers",
		"service":   "catalog",
		"status":    "accepted",
	})
	if rErr != nil {
		t.Fatalf("callStoreDecision: %+v", rErr)
	}

	after, err := s.memoryStore.Get(existing.ID)
	if err != nil {
		t.Fatalf("Get existing: %v", err)
	}
	// No avoided_dead_end_id → no edge → no counter bump on any
	// pre-existing memory.
	if got := after.Metadata[memory.MetadataReferencedByCount]; got != "" {
		t.Fatalf("referenced_by_count = %q, want empty", got)
	}
}

func TestCallRecallMemoryBlendsDeadEndOnPitfallKeyword(t *testing.T) {
	s := newMemoryTestServer(t)

	// Seed a dead_end record.
	_, rErr := s.callStoreDeadEnd(map[string]any{
		"attempted_approach": "sharding without capacity planning",
		"why_failed":         "hot partitions saturated a single node and migration had to be rolled back",
		"service":            "ledger",
		"context":            "payments",
	})
	if rErr != nil {
		t.Fatalf("seed dead_end: %+v", rErr)
	}
	// Seed an unrelated top-ranking memory that matches the query so the
	// dead_end is unlikely to appear in the main top-K.
	if err := s.memoryStore.Store(context.Background(), &memory.Memory{
		Title:      "catalog migration approach guide",
		Content:    "how to approach catalog migration with staged rollout and canary verification",
		Type:       memory.TypeSemantic,
		Importance: 0.5,
		Context:    "payments",
	}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Use limit=1 so that if the generic memory out-ranks, the dead_end
	// won't be in the main results — blending should kick in.
	result, rErr := s.callRecallMemory(map[string]any{
		"query": "how to approach catalog migration",
		"limit": 1,
	})
	if rErr != nil {
		t.Fatalf("callRecallMemory returned error: %+v", rErr)
	}
	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	text := toolRes.Content[0].Text

	// Two acceptable outcomes:
	//  (a) the dead_end is already in results (suggestion omitted);
	//  (b) the dead_end is surfaced as a suggestion marker.
	deadEndInResults := strings.Contains(text, "Tags: [dead_end")
	suggested := strings.Contains(text, "suggestion:dead_end")
	if !deadEndInResults && !suggested {
		t.Fatalf("expected dead_end either in results or as suggestion, got:\n%s", text)
	}

	// Regression: the blend must not mutate stored tags. The
	// `suggestion:dead_end` marker may appear in the returned output but must
	// never be persisted on the stored memory itself.
	all, err := s.memoryStore.List(context.Background(), memory.Filters{}, 100)
	if err != nil {
		t.Fatalf("List after blend: %v", err)
	}
	for _, m := range all {
		for _, tag := range m.Tags {
			if tag == "suggestion:dead_end" {
				t.Fatalf("stored memory %q tags mutated with suggestion marker: %v", m.ID, m.Tags)
			}
		}
	}
}

func TestCallRecallMemoryNoBlendWhenQueryHasNoPitfallKeyword(t *testing.T) {
	s := newMemoryTestServer(t)

	_, rErr := s.callStoreDeadEnd(map[string]any{
		"attempted_approach": "use async migration in place without shadow write",
		"why_failed":         "lost messages during cutover",
		"service":            "catalog",
	})
	if rErr != nil {
		t.Fatalf("seed dead_end: %+v", rErr)
	}
	// Seed an unrelated semantic memory so the recall has something to return.
	if err := s.memoryStore.Store(context.Background(), &memory.Memory{
		Title:      "unrelated fact",
		Content:    "catalog service uses protobuf for its public API",
		Type:       memory.TypeSemantic,
		Importance: 0.5,
	}); err != nil {
		t.Fatalf("Store unrelated: %v", err)
	}

	result, rErr := s.callRecallMemory(map[string]any{
		"query": "catalog service protobuf schema",
		"limit": 5,
	})
	if rErr != nil {
		t.Fatalf("callRecallMemory returned error: %+v", rErr)
	}
	toolRes, ok := result.(toolResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	text := toolRes.Content[0].Text
	if strings.Contains(text, "suggestion:dead_end") {
		t.Fatalf("did not expect dead_end suggestion for neutral query, got:\n%s", text)
	}
}

func TestIsDeadEndKeywordQueryTable(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"how to approach async migration", true},
		{"what is the lesson learned here?", true},
		{"pitfall when sharding without plan", true},
		{"should I avoid this pattern?", true},
		{"why not use global locks", true},
		{"", false},
		{"catalog service public API schema", false},
		{"how much memory does ingester use", false}, // near-miss: "how " without "how to"
		// Regression: word-boundary matching must suppress substring hits.
		{"retry storm diagnosis", false},    // "retry" must not fire "try"
		{"country code validator", false},   // "country" must not fire "try"
		{"entry point config", false},       // "entry" must not fire "try"
		{"unavoidable dependency", false},   // "unavoidable" must not fire "avoid"
		{"devoid of tests", false},          // "devoid" must not fire "avoid"
		{"poultry counting service", false}, // "poultry" must not fire "try"
	}
	for _, c := range cases {
		got := isDeadEndKeywordQuery(c.q)
		if got != c.want {
			t.Fatalf("isDeadEndKeywordQuery(%q) = %v, want %v", c.q, got, c.want)
		}
	}
}

func TestCallStoreMemoryRejectsInvalidType(t *testing.T) {
	s := newMemoryTestServer(t)

	_, rErr := s.callStoreMemory(map[string]any{
		"content": "remember this",
		"type":    "broken",
	})
	if rErr == nil {
		t.Fatal("expected invalid type error")
	}
	if rErr.Code != -32602 {
		t.Fatalf("rErr.Code = %d, want -32602", rErr.Code)
	}
	if !strings.Contains(rErr.Message, "invalid memory type") {
		t.Fatalf("rErr.Message = %q, want invalid memory type", rErr.Message)
	}
}

func TestCallStoreMemoryNormalizesTagsFromString(t *testing.T) {
	s := newMemoryTestServer(t)

	_, rErr := s.callStoreMemory(map[string]any{
		"content": "remember this",
		"tags":    " api,incident,api , rollback ",
	})
	if rErr != nil {
		t.Fatalf("callStoreMemory error = %v", rErr)
	}

	memories, err := s.memoryStore.List(context.Background(), memory.Filters{}, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	got := memories[0].Tags
	want := []string{"api", "incident", "rollback"}
	if len(got) != len(want) {
		t.Fatalf("len(tags) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tags[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

func TestFormatSearchResultsOmitsZeroVerifiedTimestamp(t *testing.T) {
	s := newTestServer(t, "")

	formatted := s.formatSearchResults(&rag.SearchResponse{
		Query: "runbook",
		Results: []rag.SearchResult{
			{
				Title:   "Runbook",
				Path:    "runbooks/api.md",
				Score:   0.8,
				Snippet: "Steps",
				Trust: &trust.Metadata{
					KnowledgeLayer: "document",
					SourceType:     "runbook",
					Confidence:     0.8,
					FreshnessScore: 0.7,
				},
			},
		},
	})

	if strings.Contains(formatted, "verified=0001-01-01T00:00:00Z") {
		t.Fatalf("formatted output unexpectedly contains zero verified timestamp:\n%s", formatted)
	}
}
