package sessionclose

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func countEpisodics(t *testing.T, store *memory.Store, slug string) int {
	t.Helper()
	items, err := store.List(context.Background(), memory.Filters{Context: slug, Type: memory.TypeEpisodic}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return len(items)
}

// T71: a second session-close write for the same slug within the window folds
// into the first record instead of creating a duplicate episodic.
func TestSaveRawSummary_Idempotent_SecondCallUpdates(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	id1, err := svc.SaveRawSummary(ctx, memory.SessionSummary{
		Context: "t71-demo",
		Service: "mcp",
		Summary: "First close summary.",
	})
	if err != nil {
		t.Fatalf("first SaveRawSummary: %v", err)
	}

	id2, err := svc.SaveRawSummary(ctx, memory.SessionSummary{
		Context: "t71-demo",
		Service: "mcp",
		Summary: "Second close summary, slightly different.",
	})
	if err != nil {
		t.Fatalf("second SaveRawSummary: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected consolidation into same id, got %s vs %s", id1, id2)
	}
	if n := countEpisodics(t, store, "t71-demo"); n != 1 {
		t.Fatalf("expected 1 episodic after idempotent write, got %d", n)
	}
}

// T71: the auto-hook session close folds into a recent /finalize "Task complete:"
// episodic of the same slug — preserving its richer content and importance —
// instead of writing the second record that steward later flags as a duplicate.
func TestSaveRawSummary_ConsolidatesFinalizeTaskComplete(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	finalize := &memory.Memory{
		Title:      "Task complete: t71-deploy",
		Content:    "Detailed finalize note: shipped deploy, verified health, updated docs.",
		Type:       memory.TypeEpisodic,
		Context:    "t71-deploy",
		Importance: 0.7,
		Tags:       []string{"task-complete"},
	}
	if err := store.Store(ctx, finalize); err != nil {
		t.Fatalf("store finalize record: %v", err)
	}

	id, err := svc.SaveRawSummary(ctx, memory.SessionSummary{
		Context: "t71-deploy",
		Service: "platform",
		Summary: "Auto session close summary.",
	})
	if err != nil {
		t.Fatalf("SaveRawSummary: %v", err)
	}

	if id != finalize.ID {
		t.Fatalf("expected fold into finalize id %s, got %s", finalize.ID, id)
	}
	if n := countEpisodics(t, store, "t71-deploy"); n != 1 {
		t.Fatalf("expected 1 consolidated episodic, got %d", n)
	}

	got, err := store.Get(finalize.ID)
	if err != nil {
		t.Fatalf("Get consolidated: %v", err)
	}
	if got.Importance < 0.7 {
		t.Fatalf("importance lowered to %v, want >= 0.7", got.Importance)
	}
	if !strings.Contains(got.Content, "Detailed finalize note") {
		t.Fatalf("finalize content lost after consolidation: %q", got.Content)
	}
	if got.Metadata[memory.MetadataRecordKind] != memory.RecordKindSessionSummary {
		t.Fatalf("session-close record_kind not merged in: %v", got.Metadata)
	}
}

// T71 guard: a different slug is never folded into an unrelated context.
func TestSaveRawSummary_DistinctContext_NoConsolidation(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	if _, err := svc.SaveRawSummary(ctx, memory.SessionSummary{Context: "t71-a", Summary: "Close A."}); err != nil {
		t.Fatalf("SaveRawSummary A: %v", err)
	}
	if _, err := svc.SaveRawSummary(ctx, memory.SessionSummary{Context: "t71-b", Summary: "Close B."}); err != nil {
		t.Fatalf("SaveRawSummary B: %v", err)
	}

	if n := countEpisodics(t, store, "t71-a"); n != 1 {
		t.Fatalf("context t71-a: expected 1 episodic, got %d", n)
	}
	if n := countEpisodics(t, store, "t71-b"); n != 1 {
		t.Fatalf("context t71-b: expected 1 episodic, got %d", n)
	}
}

// T71 regression: a terminal record older than the consolidation window is NOT
// folded into — cross-session duplicates are left to the steward (T69). The
// store stamps CreatedAt at real time, so we advance svc.now past the window to
// make the just-written finalize record fall outside it.
func TestSaveRawSummary_OutsideWindow_WritesNewRecord(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	finalize := &memory.Memory{
		Title:      "Task complete: t71-old",
		Content:    "Old finalize note from a previous session.",
		Type:       memory.TypeEpisodic,
		Context:    "t71-old",
		Importance: 0.7,
	}
	if err := store.Store(ctx, finalize); err != nil {
		t.Fatalf("store finalize record: %v", err)
	}

	svc.now = func() time.Time { return time.Now().Add(consolidationWindow + time.Hour) }

	id, err := svc.SaveRawSummary(ctx, memory.SessionSummary{
		Context: "t71-old",
		Summary: "New session close after the window elapsed.",
	})
	if err != nil {
		t.Fatalf("SaveRawSummary: %v", err)
	}

	if id == finalize.ID {
		t.Fatalf("expected a new record outside the window, but folded into %s", finalize.ID)
	}
	if n := countEpisodics(t, store, "t71-old"); n != 2 {
		t.Fatalf("expected 2 episodics (no consolidation outside window), got %d", n)
	}
}
