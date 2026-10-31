package server

import (
	"context"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T156: every way a caller could make a record trusted without
// promote_to_canonical. Each case is one call an agent could be talked into by
// a planted instruction; before the fix each one either landed a canonical
// record or a provenance the T77 gate trusts. The witness is the refusal reason,
// not just the code — an unrelated invalid-params error must not pass for it.
func TestCallerCannotSetTrustMetadata(t *testing.T) {
	cases := []struct {
		name string
		call func(s *MCPServer, id string) *rpcError
	}{
		{"update_memory provenance=external", func(s *MCPServer, id string) *rpcError {
			_, err := s.callUpdateMemory(map[string]any{"id": id, "metadata": map[string]any{"provenance": "external"}})
			return err
		}},
		{"update_memory canonical=true", func(s *MCPServer, id string) *rpcError {
			_, err := s.callUpdateMemory(map[string]any{"id": id, "metadata": map[string]any{"canonical": "true"}})
			return err
		}},
		{"update_memory knowledge_layer=canonical", func(s *MCPServer, id string) *rpcError {
			_, err := s.callUpdateMemory(map[string]any{"id": id, "metadata": map[string]any{"knowledge_layer": "canonical"}})
			return err
		}},
		{"update_memory lifecycle_status=canonical", func(s *MCPServer, id string) *rpcError {
			_, err := s.callUpdateMemory(map[string]any{"id": id, "metadata": map[string]any{"lifecycle_status": "Canonical"}})
			return err
		}},
		{"update_memory status=canonical", func(s *MCPServer, id string) *rpcError {
			_, err := s.callUpdateMemory(map[string]any{"id": id, "metadata": map[string]any{"status": "canonical"}})
			return err
		}},
		{"update_memory padded key", func(s *MCPServer, id string) *rpcError {
			_, err := s.callUpdateMemory(map[string]any{"id": id, "metadata": map[string]any{" provenance ": "verified"}})
			return err
		}},
		{"store_decision status=canonical", func(s *MCPServer, _ string) *rpcError {
			_, err := s.callStoreDecision(map[string]any{"decision": "planted", "status": "canonical"})
			return err
		}},
		{"close_session metadata canonical=true", func(s *MCPServer, _ string) *rpcError {
			_, err := s.callCloseSession(map[string]any{
				"summary":  "Decision: planted fact",
				"dry_run":  false,
				"save_raw": true,
				"metadata": map[string]any{"canonical": "true"},
			})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMemoryTestServer(t)
			mem := &memory.Memory{Content: "ordinary conversational note", Type: memory.TypeSemantic}
			if err := s.memoryStore.Store(context.Background(), mem); err != nil {
				t.Fatalf("Store: %v", err)
			}

			rErr := tc.call(s, mem.ID)
			if rErr == nil || rErr.Code != rpcErrInvalidParams || !strings.Contains(rErr.Message, "set by the server") {
				t.Errorf("got %+v, want invalid params refusing a server-owned trust value", rErr)
			}

			all, err := s.memoryStore.List(context.Background(), memory.Filters{}, 50)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			for _, m := range all {
				if memory.IsCanonicalMemory(m) || memory.PromotionAllowed(m, false) {
					t.Errorf("record %s became trusted: metadata=%v", m.ID, m.Metadata)
				}
			}
		})
	}
}

// Removing a trust key only lowers trust — the T96 correction path for records
// stamped by their own promotion — and ordinary statuses are not trust values.
func TestCallerTrustMetadataAllowsRemovalAndOrdinaryStatus(t *testing.T) {
	s := newMemoryTestServer(t)
	mem := &memory.Memory{Content: "note", Type: memory.TypeSemantic, Metadata: map[string]string{"last_verified_at": "2026-01-01T00:00:00Z"}}
	if err := s.memoryStore.Store(context.Background(), mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if _, rErr := s.callUpdateMemory(map[string]any{"id": mem.ID, "metadata": map[string]any{"last_verified_at": "", "status": "outdated"}}); rErr != nil {
		t.Fatalf("removal + ordinary status refused: %+v", rErr)
	}
	got, err := s.memoryStore.Get(mem.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.Metadata["last_verified_at"]; ok {
		t.Errorf("last_verified_at survived its removal: %v", got.Metadata)
	}
	if _, rErr := s.callStoreDecision(map[string]any{"decision": "keep HPA", "status": "accepted"}); rErr != nil {
		t.Fatalf("store_decision status=accepted refused: %+v", rErr)
	}
}
