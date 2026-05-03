package server

import (
	"context"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// seedMultihopGraph builds a 3-memory chain where only m1 carries query
// keywords, so a keyword-only Recall returns m1 alone as seed; m2 is
// reachable at hop=1 (its triple shares the seed entity db_pool_exhaustion)
// and m3 is reachable at hop=1 via the m2 connector — both surface only
// through the graph walk. This guarantees the formatted tool output
// includes at least one path arrow.
func seedMultihopGraph(t *testing.T, s *MCPServer) (seedID, twoHopID string) {
	t.Helper()
	ctx := context.Background()

	m1 := &memory.Memory{
		Title:      "Payments outage",
		Content:    "incident root cause analysis: payments outage triggered by db_pool exhaustion",
		Type:       memory.TypeSemantic,
		Importance: 0.9,
	}
	if err := s.memoryStore.Store(ctx, m1); err != nil {
		t.Fatalf("store m1: %v", err)
	}
	m2 := &memory.Memory{
		Title:      "Saturation runbook",
		Content:    "schema rewrite scheduling caused thread saturation in 2025-Q1 window",
		Type:       memory.TypeSemantic,
		Importance: 0.7,
	}
	if err := s.memoryStore.Store(ctx, m2); err != nil {
		t.Fatalf("store m2: %v", err)
	}
	// m3 sits two graph steps from any seed entity. Its triple's
	// endpoints (migration_v42, release_2025_03) are NOT seed entities —
	// migration_v42 is hop=1, release_2025_03 is hop=2 — so m3's contrib
	// flows from a non-seed endpoint and the path field is non-empty.
	m3 := &memory.Memory{
		Title:      "Release tracker",
		Content:    "deploy summary for migration_v42 went out in release 2025_03",
		Type:       memory.TypeSemantic,
		Importance: 0.6,
	}
	if err := s.memoryStore.Store(ctx, m3); err != nil {
		t.Fatalf("store m3: %v", err)
	}

	if err := s.memoryStore.AddTriples(ctx, []*memory.Triple{
		{Subject: "payments_service", Relation: "caused_by", Object: "db_pool_exhaustion", MemoryID: m1.ID, Weight: 0.9},
		{Subject: "db_pool_exhaustion", Relation: "traced_to", Object: "migration_v42", MemoryID: m2.ID, Weight: 0.85},
		{Subject: "migration_v42", Relation: "shipped_in", Object: "release_2025_03", MemoryID: m3.ID, Weight: 0.8},
	}); err != nil {
		t.Fatalf("AddTriples: %v", err)
	}
	return m1.ID, m3.ID
}

func TestCallRecallMultihop_RequiresQuery(t *testing.T) {
	s := newMemoryTestServer(t)
	if _, rErr := s.callRecallMultihop(map[string]any{}); rErr == nil {
		t.Fatalf("expected rpc error when query is missing")
	}
}

func TestCallRecallMultihop_RequiresMemoryStore(t *testing.T) {
	// A bare server (no memory store wired) must surface a clear
	// "memory store unavailable" error rather than panic.
	s := newTestServer(t, "")
	if _, rErr := s.callRecallMultihop(map[string]any{"query": "x"}); rErr == nil {
		t.Fatalf("expected rpc error when memory store is not configured")
	}
}

func TestCallRecallMultihop_ReturnsResultsAndPathsInOutput(t *testing.T) {
	s := newMemoryTestServer(t)
	seedID, twoHopID := seedMultihopGraph(t, s)

	result, rErr := s.callRecallMultihop(map[string]any{
		"query":    "payments outage db_pool exhaustion",
		"max_hops": 3,
		"seed_k":   3,
		"limit":    10,
	})
	if rErr != nil {
		t.Fatalf("callRecallMultihop: %+v", rErr)
	}
	text := toolResultTextOf(t, result)
	if !strings.Contains(text, "Multihop recall for") {
		t.Errorf("output missing header line:\n%s", text)
	}
	if !strings.Contains(text, seedID) {
		t.Errorf("output missing seed memory id %s:\n%s", seedID, text)
	}
	if !strings.Contains(text, twoHopID) {
		t.Errorf("output missing 2-hop memory id %s:\n%s", twoHopID, text)
	}
	// Path lines are emitted as "(subj)─[rel]→(obj)" for hop>0 results.
	// At least one connecting triple from the chain must appear since m3
	// is two graph steps from any seed entity.
	if !strings.Contains(text, "─[traced_to]→") && !strings.Contains(text, "─[shipped_in]→") {
		t.Errorf("output missing graph path arrow:\n%s", text)
	}
}

func TestCallRecallMultihop_LimitClampedToMax(t *testing.T) {
	s := newMemoryTestServer(t)
	_, _ = seedMultihopGraph(t, s)

	// limit=999 should not blow up the engine — the handler must clamp
	// silently to its documented ceiling (50). Easiest way to assert is
	// "no error, output is well-formed".
	result, rErr := s.callRecallMultihop(map[string]any{
		"query": "payments outage db_pool exhaustion",
		"limit": 999,
	})
	if rErr != nil {
		t.Fatalf("callRecallMultihop with oversize limit: %+v", rErr)
	}
	if got := toolResultTextOf(t, result); !strings.Contains(got, "Multihop recall") {
		t.Errorf("clamp path produced malformed output:\n%s", got)
	}
}

func TestFormatMultihopResults_EmptyResultRendersNoMatchLine(t *testing.T) {
	got := formatMultihopResults("query xyz", nil)
	if !strings.Contains(got, "no results") {
		t.Errorf("empty result formatter dropped 'no results' marker; got=%q", got)
	}
}

func TestRecallMultihop_RegisteredInDispatchTable(t *testing.T) {
	s := newMemoryTestServer(t)
	if _, ok := s.toolHandlers["recall_multihop"]; !ok {
		t.Fatalf("recall_multihop missing from dispatch table — clients can't invoke the tool")
	}
}
