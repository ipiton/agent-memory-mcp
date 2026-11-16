package sessionclose

import (
	"context"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// T155: consolidating a session summary into the previous one replaces its
// text when shouldReplaceContent passes — even when one value changed. The
// replaced text must survive as a revision tagged with the consolidation path.
func TestSaveRawSummary_ConsolidationKeepsReplacedText(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	base := "Reviewed the log pipeline for the payments cluster today. The retention window for raw logs " +
		"is VALUE, compaction runs nightly, alerts route to the platform channel, dashboards were updated, " +
		"and the runbook now lists the restore steps with owners for every stage of the pipeline."
	before := strings.Replace(base, "VALUE", "96h", 1)
	after := strings.Replace(base, "VALUE", "30days", 1)

	id1, err := svc.SaveRawSummary(ctx, memory.SessionSummary{Context: "t155-demo", Service: "logs", Summary: before})
	if err != nil {
		t.Fatalf("first SaveRawSummary: %v", err)
	}
	id2, err := svc.SaveRawSummary(ctx, memory.SessionSummary{Context: "t155-demo", Service: "logs", Summary: after})
	if err != nil {
		t.Fatalf("second SaveRawSummary: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected consolidation into one record, got %s and %s", id1, id2)
	}

	current, err := store.Get(id1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(current.Content, "30days") {
		t.Fatalf("consolidation did not replace the text: %q", current.Content)
	}

	revs, err := store.Revisions(ctx, id1)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("revisions = %d, want 1: %#v", len(revs), revs)
	}
	if !strings.Contains(revs[0].Content, "96h") {
		t.Fatalf("revision lost the replaced value: %q", revs[0].Content)
	}
	if revs[0].ReplacedByPath != memory.RevisionSourceSessionConsolidate {
		t.Fatalf("replaced_by_path = %q, want %q", revs[0].ReplacedByPath, memory.RevisionSourceSessionConsolidate)
	}
}
