package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// storeTestMemory is a helper that creates a memory via the store, returning
// the assigned ID. Fails the test on error.
func storeTestMemory(t *testing.T, s *MCPServer, m *memory.Memory) string {
	t.Helper()
	if err := s.memoryStore.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	return m.ID
}

func TestCallPromoteSediment_UpdatesLayer(t *testing.T) {
	s := newMemoryTestServer(t)
	id := storeTestMemory(t, s, &memory.Memory{Content: "hello", Type: memory.TypeSemantic})

	result, rErr := s.callPromoteSediment(map[string]any{
		"id":           id,
		"target_layer": "character",
		"format":       "json",
	})
	if rErr != nil {
		t.Fatalf("callPromoteSediment: %+v", rErr)
	}
	text := toolResultTextOf(t, result)
	var payload memory.PromoteSedimentResult
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("unmarshal result: %v; text=%s", err, text)
	}
	if payload.To != memory.LayerCharacter {
		t.Errorf("To=%q, want character", payload.To)
	}
	if !payload.Affected {
		t.Errorf("Affected=false, want true")
	}
}

func TestCallPromoteSediment_InvalidLayer(t *testing.T) {
	s := newMemoryTestServer(t)
	id := storeTestMemory(t, s, &memory.Memory{Content: "hello", Type: memory.TypeSemantic})

	_, rErr := s.callPromoteSediment(map[string]any{
		"id":           id,
		"target_layer": "garbage",
	})
	if rErr == nil {
		t.Fatalf("expected error for invalid target_layer")
	}
	if !strings.Contains(rErr.Message, "invalid target_layer") {
		t.Errorf("error message=%q, want 'invalid target_layer'", rErr.Message)
	}
}

func TestCallDemoteSediment_OneStep(t *testing.T) {
	s := newMemoryTestServer(t)
	id := storeTestMemory(t, s, &memory.Memory{Content: "seed", Type: memory.TypeSemantic})
	if _, err := s.memoryStore.PromoteSediment(context.Background(), id, memory.LayerCharacter); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	result, rErr := s.callDemoteSediment(map[string]any{
		"id":     id,
		"format": "json",
	})
	if rErr != nil {
		t.Fatalf("callDemoteSediment: %+v", rErr)
	}
	text := toolResultTextOf(t, result)
	var payload memory.DemoteSedimentResult
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("unmarshal result: %v; text=%s", err, text)
	}
	if payload.To != memory.LayerSemantic {
		t.Errorf("To=%q, want semantic", payload.To)
	}
}

func TestCallDemoteSediment_NoopAtSurface(t *testing.T) {
	s := newMemoryTestServer(t)
	id := storeTestMemory(t, s, &memory.Memory{Content: "seed", Type: memory.TypeSemantic})
	// default layer is surface

	result, rErr := s.callDemoteSediment(map[string]any{
		"id":     id,
		"format": "text",
	})
	if rErr != nil {
		t.Fatalf("callDemoteSediment: %+v", rErr)
	}
	text := toolResultTextOf(t, result)
	if !strings.Contains(text, "already at surface") {
		t.Errorf("expected 'already at surface' in output, got %q", text)
	}
}

func TestCallSedimentCycle_DryRun(t *testing.T) {
	s := newMemoryTestServer(t)
	// Store an aged surface memory that would transition.
	id := storeTestMemory(t, s, &memory.Memory{Content: "aged", Type: memory.TypeWorking})
	// Backdate to 10 days ago; access_count 5.
	if _, err := s.memoryStore.DB().Exec(
		`UPDATE memories SET created_at = datetime('now', '-10 days'), access_count = 5 WHERE id = ?`, id,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// Reload cache so Decide sees the update.
	if err := reloadStoreCache(s); err != nil {
		t.Fatalf("reload: %v", err)
	}

	result, rErr := s.callSedimentCycle(map[string]any{
		"dry_run": true,
		"format":  "json",
	})
	if rErr != nil {
		t.Fatalf("callSedimentCycle: %+v", rErr)
	}
	text := toolResultTextOf(t, result)
	var payload memory.SedimentCycleResult
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("unmarshal result: %v; text=%s", err, text)
	}
	if !payload.DryRun {
		t.Errorf("DryRun=false, want true")
	}
	// Dry-run reports proposed transitions without mutating.
	got, err := s.memoryStore.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(memory.LayerSurface) {
		t.Errorf("dry-run mutated layer to %q", got.SedimentLayer)
	}
}

// reloadStoreCache forces the in-memory cache to resync with the DB.
// Exposed as a helper because tests occasionally bypass the write path via
// direct SQL (e.g. backdating created_at).
func reloadStoreCache(s *MCPServer) error {
	// The Store has a private reload, but we don't need to reach into it —
	// run a dummy List to trigger nothing. Instead we directly invoke the
	// exported Close/NewStore would be overkill. The simplest path: use
	// the fact that loadMemoriesToCache is unexported but called during
	// NewStore. For the test we can re-open the store path — but the
	// test's MemoryStore is the one in s.memoryStore. Simpler: expose
	// through a public wrapper if absent; for now, the only caller is
	// this test, and the helper is in the same package as the store.
	return s.memoryStore.ReloadCache()
}

// toolResultTextOf extracts the text payload from a toolResult returned by
// any call* handler. Fails the test if the result isn't a toolResult with
// at least one text content block.
func toolResultTextOf(t *testing.T, result any) string {
	t.Helper()
	tr, ok := result.(toolResult)
	if !ok {
		t.Fatalf("expected toolResult, got %T", result)
	}
	if len(tr.Content) == 0 {
		t.Fatalf("toolResult has no content")
	}
	c := tr.Content[0]
	if c.Type != "text" {
		t.Fatalf("content type=%q, want text", c.Type)
	}
	return c.Text
}
