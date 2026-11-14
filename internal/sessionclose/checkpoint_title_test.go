package sessionclose

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T186: a checkpoint shared the "Session close" title and tag with the session's
// closing summary, so a long session surfaced in search as a dozen look-alike
// "Session close" records.
func TestCheckpointIsNotTitledAsSessionClose(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	id, err := svc.SaveRawSummaryWithOptions(ctx, memory.SessionSummary{
		Context: "payments",
		Service: "api",
		Summary: "Investigated the rollback order for the payments api migration and noted the caveat.",
	}, RawSaveOptions{
		RecordKind: memory.RecordKindSessionCheckpoint,
		ExtraTags:  []string{"session-checkpoint", "pre_compact"},
	})
	if err != nil {
		t.Fatalf("SaveRawSummaryWithOptions: %v", err)
	}
	mem, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if mem.Title != "Session checkpoint / payments / api" {
		t.Errorf("title = %q, want %q", mem.Title, "Session checkpoint / payments / api")
	}
	if strings.HasPrefix(mem.Title, "Session close") {
		t.Errorf("checkpoint still titled as a session close: %q", mem.Title)
	}
	for _, tag := range []string{"session-close", "session-summary"} {
		if slices.Contains(mem.Tags, tag) {
			t.Errorf("checkpoint carries summary tag %q: %v", tag, mem.Tags)
		}
	}
	if !slices.Contains(mem.Tags, "session-checkpoint") {
		t.Errorf("checkpoint lacks session-checkpoint tag: %v", mem.Tags)
	}
	if !memory.IsSessionCheckpointMemory(mem) || memory.IsTerminalRecord(mem) {
		t.Errorf("record kind = %q, want a non-terminal checkpoint", mem.Metadata[memory.MetadataRecordKind])
	}
}
