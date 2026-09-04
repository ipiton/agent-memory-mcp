package sessionclose

import (
	"context"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// summaryWithSession is the shape the SessionEnd hook writes: a project label
// for the context and the agent session's own id in the metadata.
func summaryWithSession(slug, sessionID, text string) memory.SessionSummary {
	s := memory.SessionSummary{Context: slug, Service: "mcp", Summary: text}
	if sessionID != "" {
		s.Metadata = map[string]string{memory.MetadataAgentSessionID: sessionID}
	}
	return s
}

// TestSaveRawSummary_TwoSessionsOneProject (T130) is the defect: two headless
// sessions closing in the same project within the six-hour window produced one
// record. Consolidation grouped by context, and shouldReplaceContent refuses to
// replace text below 0.95 lexical overlap — which two different sessions never
// reach — so the second session's tags and metadata were merged in while its
// content was dropped on the floor.
func TestSaveRawSummary_TwoSessionsOneProject(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	first := "Fixed the archive sweep threshold and measured it on the live bank."
	second := "Traced the embedding cache reload and rewrote the watcher test."

	id1, err := svc.SaveRawSummary(ctx, summaryWithSession("Moving", "sess-aaa", first))
	if err != nil {
		t.Fatalf("first SaveRawSummary: %v", err)
	}
	id2, err := svc.SaveRawSummary(ctx, summaryWithSession("Moving", "sess-bbb", second))
	if err != nil {
		t.Fatalf("second SaveRawSummary: %v", err)
	}

	if id1 == id2 {
		t.Fatalf("two different sessions folded into one record (%s)", id1)
	}
	if n := countEpisodics(t, store, "Moving"); n != 2 {
		t.Fatalf("expected 2 episodics for two sessions, got %d", n)
	}

	// Both bodies survive — the half the fold used to lose.
	for id, want := range map[string]string{id1: first, id2: second} {
		got, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if !strings.Contains(got.Content, want) {
			t.Fatalf("record %s lost its session's content: %q", id, got.Content)
		}
	}

	// The context is the project, so an exact-match recall finds both.
	items, err := store.List(ctx, memory.Filters{Context: "Moving", Type: memory.TypeEpisodic}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("recall by project context found %d records, want 2", len(items))
	}
}

// TestSaveRawSummary_SameSessionStillConsolidates (T130) is the property the
// fix had to keep: closing the same session twice — a re-run hook, a manual
// close after the automatic one — still folds into one record.
func TestSaveRawSummary_SameSessionStillConsolidates(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	id1, err := svc.SaveRawSummary(ctx, summaryWithSession("Moving", "sess-aaa", "First close summary."))
	if err != nil {
		t.Fatalf("first SaveRawSummary: %v", err)
	}
	id2, err := svc.SaveRawSummary(ctx, summaryWithSession("Moving", "sess-aaa", "First close summary, extended a little."))
	if err != nil {
		t.Fatalf("second SaveRawSummary: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("the same session produced two records: %s vs %s", id1, id2)
	}
	if n := countEpisodics(t, store, "Moving"); n != 1 {
		t.Fatalf("expected 1 episodic for one session, got %d", n)
	}
}

// TestSaveRawSummary_UnidentifiedRecordsKeepOldBehaviour (T130) covers the
// records that carry no session id at all — a manual close_session, and every
// record written before the id existed. They consolidate with each other as
// before, and never with an identified session.
func TestSaveRawSummary_UnidentifiedRecordsKeepOldBehaviour(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	anon1, err := svc.SaveRawSummary(ctx, summaryWithSession("Moving", "", "Manual close, first."))
	if err != nil {
		t.Fatalf("first SaveRawSummary: %v", err)
	}
	anon2, err := svc.SaveRawSummary(ctx, summaryWithSession("Moving", "", "Manual close, second."))
	if err != nil {
		t.Fatalf("second SaveRawSummary: %v", err)
	}
	if anon1 != anon2 {
		t.Fatalf("unidentified records stopped consolidating: %s vs %s", anon1, anon2)
	}

	identified, err := svc.SaveRawSummary(ctx, summaryWithSession("Moving", "sess-aaa", "Hook close of an actual session."))
	if err != nil {
		t.Fatalf("identified SaveRawSummary: %v", err)
	}
	if identified == anon1 {
		t.Fatal("an identified session folded into a record whose session is unknown")
	}
	if n := countEpisodics(t, store, "Moving"); n != 2 {
		t.Fatalf("expected 2 episodics (one anonymous, one identified), got %d", n)
	}
}
