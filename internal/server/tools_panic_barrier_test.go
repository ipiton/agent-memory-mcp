package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T88 C1. superseded_by is arbitrary caller input — MarkOutdated only trims it —
// so the timeline formatter must not assume it is at least 8 chars long.
func TestFormatKnowledgeTimelineShortSupersededBy(t *testing.T) {
	entries := []memory.TimelineEntry{{
		MemoryID:     "abcdef0123456789",
		Title:        "routing decision",
		Status:       "outdated",
		SupersededBy: "x",
		Replaces:     "y",
	}}

	out := formatKnowledgeTimeline(entries, "routing")

	if !strings.Contains(out, "outdated -> x") {
		t.Fatalf("formatted timeline = %q, want it to render the short superseded_by verbatim", out)
	}
	if !strings.Contains(out, "Replaces: y") {
		t.Fatalf("formatted timeline = %q, want it to render the short replaces verbatim", out)
	}
}

// T88 C1 end-to-end: the two calls a caller actually makes. Before the guard
// this panicked inside the formatter and, with no recover() in the dispatcher,
// took the stdio server down with it.
func TestKnowledgeTimelineToolSurvivesShortSupersededBy(t *testing.T) {
	s := newMemoryTestServer(t)

	stored := &memory.Memory{
		Content: "ingress routing switched to traefik",
		Type:    "semantic",
	}
	if err := s.memoryStore.Store(context.Background(), stored); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if _, rErr := s.callMarkOutdated(map[string]any{
		"id":            stored.ID,
		"superseded_by": "x",
	}); rErr != nil {
		t.Fatalf("callMarkOutdated: %v", rErr)
	}

	params, _ := json.Marshal(map[string]any{
		"name":      "knowledge_timeline",
		"arguments": map[string]any{"query": "ingress routing"},
	})
	result, rErr := s.handleToolsCall(json.RawMessage(params))
	if rErr != nil {
		t.Fatalf("knowledge_timeline returned error: %+v", rErr)
	}
	if result == nil {
		t.Fatal("knowledge_timeline returned nil result")
	}
}

// T88 C1: the barrier itself. A handler that panics must degrade to one failed
// call, not to a dead process.
func TestInvokeToolConvertsPanicToInternalError(t *testing.T) {
	s := newTestServer(t, "")

	result, rErr := s.invokeTool("exploding_tool", func(map[string]any) (any, *rpcError) {
		panic("boom")
	}, nil)

	if rErr == nil {
		t.Fatal("expected an rpcError from a panicking handler")
	}
	if rErr.Code != rpcErrInternalError {
		t.Fatalf("rErr.Code = %d, want %d", rErr.Code, rpcErrInternalError)
	}
	if !strings.Contains(rErr.Message, "exploding_tool") {
		t.Fatalf("rErr.Message = %q, want it to name the tool", rErr.Message)
	}
	data, _ := rErr.Data.(string)
	if !strings.Contains(data, "boom") {
		t.Fatalf("rErr.Data = %v, want the panic value", rErr.Data)
	}
	if result != nil {
		t.Fatalf("result = %v, want nil when the handler panicked", result)
	}
}
