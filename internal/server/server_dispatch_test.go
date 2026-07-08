package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
)

func TestDispatchTableCompleteness(t *testing.T) {
	s := newTestServer(t, "")
	handlers := s.toolHandlers

	expectedTools := []string{
		"repo_list",
		"repo_read",
		"repo_search",
		"semantic_search",
		"index_documents",
		"store_memory",
		"recall_memory",
		"update_memory",
		"delete_memory",
		"list_memories",
		"memory_stats",
		"merge_duplicates",
		"mark_outdated",
		"promote_to_canonical",
		"conflicts_report",
		"list_canonical_knowledge",
		"recall_canonical_knowledge",
		"close_session",
		"analyze_session",
		"review_session_changes",
		"accept_session_changes",
		"resolve_review_item",
		"resolve_review_queue",
		"end_task",
		"sweep_archive",
		"store_decision",
		"store_incident",
		"store_runbook",
		"store_postmortem",
		"store_dead_end",
		"search_runbooks",
		"recall_similar_incidents",
		"summarize_project_context",
		"project_bank_view",
		"steward_run",
		"steward_report",
		"steward_policy",
		"steward_status",
		"drift_scan",
		"verification_candidates",
		"verify_entry",
		"steward_inbox",
		"steward_inbox_resolve",
		"recall_as_of",
		"knowledge_timeline",
		"promote_sediment",
		"demote_sediment",
		"sediment_cycle",
		"recount_references",
		"recall_multihop",
	}

	for _, tool := range expectedTools {
		if _, ok := handlers[tool]; !ok {
			t.Errorf("missing handler for tool %q", tool)
		}
	}

	if len(handlers) != len(expectedTools) {
		t.Errorf("handler count mismatch: got %d, want %d", len(handlers), len(expectedTools))
	}
}

func TestHandleUnknownTool(t *testing.T) {
	s := newTestServer(t, "")

	params, _ := json.Marshal(map[string]any{
		"name":      "nonexistent_tool",
		"arguments": map[string]any{},
	})

	result, rErr := s.handleToolsCall(json.RawMessage(params))
	if rErr == nil {
		t.Fatal("expected error for unknown tool")
	}
	if rErr.Code != -32601 {
		t.Fatalf("expected -32601, got %d", rErr.Code)
	}
	if result != nil {
		t.Fatal("expected nil result")
	}
}

func TestReadMessageSizeLimit(t *testing.T) {
	// Test that readMessage rejects oversized content-length
	input := "Content-Length: 999999999\r\n\r\n"
	reader := bufio.NewReader(bytes.NewBufferString(input))

	_, _, err := readMessage(reader)
	if err == nil {
		t.Fatal("expected error for oversized content-length")
	}
}
