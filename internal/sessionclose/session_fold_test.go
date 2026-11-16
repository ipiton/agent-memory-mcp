package sessionclose

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// transcriptTail renders messages from..to (inclusive) the way the hook does:
// "User:"/"Assistant:" paragraphs separated by a blank line.
func transcriptTail(from, to int) string {
	var parts []string
	for i := from; i <= to; i++ {
		speaker := "User"
		if i%2 == 1 {
			speaker = "Assistant"
		}
		parts = append(parts, fmt.Sprintf("%s: сообщение %d про миграцию индекса и порядок выката", speaker, i))
	}
	return strings.Join(parts, "\n\n")
}

func sessionSummary(slug, sessionID, text string) memory.SessionSummary {
	return memory.SessionSummary{
		Context:  slug,
		Summary:  text,
		Metadata: map[string]string{memory.MetadataAgentSessionID: sessionID},
	}
}

func listEpisodics(t *testing.T, store *memory.Store, slug string) []*memory.Memory {
	t.Helper()
	items, err := store.List(context.Background(), memory.Filters{Context: slug, Type: memory.TypeEpisodic}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return items
}

// T186: a session in the app fires SessionEnd every time it is left, days
// apart. Each one used to start a record once the 6 h window had passed.
func TestSessionEndsHoursApartFoldIntoOneRecord(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	tails := []string{transcriptTail(0, 9), transcriptTail(6, 15), transcriptTail(12, 21)}

	var ids []string
	for i, tail := range tails {
		offset := time.Duration(i*7) * time.Hour
		svc.now = func() time.Time { return time.Now().Add(offset) }
		id, err := svc.SaveRawSummary(ctx, sessionSummary("t186", "sess-a", tail))
		if err != nil {
			t.Fatalf("SaveRawSummary #%d: %v", i, err)
		}
		ids = append(ids, id)
	}

	records := listEpisodics(t, store, "t186")
	if len(records) != 1 {
		t.Fatalf("%d records for one session, want 1", len(records))
	}
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Fatalf("ids = %v, want one id", ids)
	}
	content := records[0].Content
	if want := transcriptTail(0, 21); content != want {
		t.Fatalf("folded content is not the whole conversation once:\n%s", content)
	}
}

func TestDifferentSessionsKeepTheirOwnRecords(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	for _, sid := range []string{"sess-a", "sess-b"} {
		if _, err := svc.SaveRawSummary(ctx, sessionSummary("t186-two", sid, transcriptTail(0, 5)+"\n\nUser: "+sid)); err != nil {
			t.Fatalf("SaveRawSummary %s: %v", sid, err)
		}
	}
	if n := len(listEpisodics(t, store, "t186-two")); n != 2 {
		t.Fatalf("%d records for two sessions, want 2", n)
	}
}

// T186: every PreCompact wrote a checkpoint of its own. They fold the same way,
// into one checkpoint per session, apart from the session summary.
func TestCheckpointsFoldIntoOnePerSession(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	opts := RawSaveOptions{RecordKind: memory.RecordKindSessionCheckpoint}

	for _, tail := range []string{transcriptTail(0, 9), transcriptTail(8, 17)} {
		if _, err := svc.SaveRawSummaryWithOptions(ctx, sessionSummary("t186-cp", "sess-a", tail), opts); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
	}
	if _, err := svc.SaveRawSummary(ctx, sessionSummary("t186-cp", "sess-a", transcriptTail(16, 25))); err != nil {
		t.Fatalf("summary: %v", err)
	}

	var checkpoints, summaries []*memory.Memory
	for _, m := range listEpisodics(t, store, "t186-cp") {
		if memory.IsSessionCheckpointMemory(m) {
			checkpoints = append(checkpoints, m)
		} else {
			summaries = append(summaries, m)
		}
	}
	if len(checkpoints) != 1 || len(summaries) != 1 {
		t.Fatalf("checkpoints = %d, summaries = %d, want 1 and 1", len(checkpoints), len(summaries))
	}
	if strings.HasPrefix(checkpoints[0].Title, "Session close") {
		t.Errorf("checkpoint title %q reads as a session close", checkpoints[0].Title)
	}
	if want := transcriptTail(0, 17); checkpoints[0].Content != want {
		t.Errorf("checkpoint content is not both snapshots once:\n%s", checkpoints[0].Content)
	}
}

func TestFoldSessionTail(t *testing.T) {
	stored := transcriptTail(0, 9)
	tests := []struct {
		name, tail, want string
	}{
		{"same tail again", stored, stored},
		{"tail inside stored", transcriptTail(4, 9), stored},
		{"overlapping tail", transcriptTail(5, 12), transcriptTail(0, 12)},
		{"tail starts inside the last paragraphs", transcriptTail(8, 12), transcriptTail(0, 12)},
		{"no overlap keeps both", transcriptTail(30, 31), stored + "\n\n" + sessionGapMarker + "\n\n" + transcriptTail(30, 31)},
		{"empty tail", "", stored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := foldSessionTail(stored, tt.tail); got != tt.want {
				t.Errorf("foldSessionTail() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// A short closing line repeats; one matching paragraph must not be taken for
// the overlap, or the messages between the two copies would be dropped.
func TestFoldSessionTailNeedsMoreThanOneMatchingParagraph(t *testing.T) {
	stored := "User: начнём\n\nAssistant: Готово."
	tail := "Assistant: Готово.\n\nUser: дальше про индекс\n\nAssistant: Готово."
	got := foldSessionTail(stored, tail)
	if !strings.Contains(got, "User: дальше про индекс") {
		t.Fatalf("message between repeated lines lost:\n%s", got)
	}
}

func TestCapSessionRecordKeepsOpeningAndLatest(t *testing.T) {
	long := transcriptTail(0, 1200)
	capped := capSessionRecord(long)
	if len(capped) > maxSessionRecordBytes {
		t.Fatalf("capped to %d bytes, limit %d", len(capped), maxSessionRecordBytes)
	}
	if !utf8.ValidString(capped) {
		t.Fatal("cut split a rune")
	}
	if !strings.HasPrefix(capped, "User: сообщение 0 ") || !strings.HasSuffix(capped, "сообщение 1200 про миграцию индекса и порядок выката") {
		t.Fatal("opening or latest message lost")
	}
	if strings.Count(capped, sessionTrimMarker) != 1 {
		t.Fatalf("want one trim marker, got %d", strings.Count(capped, sessionTrimMarker))
	}

	// Folding more into an already trimmed record must not pile up markers.
	again := foldSessionTail(capped, transcriptTail(1199, 1400))
	if strings.Count(again, sessionTrimMarker) != 1 || len(again) > maxSessionRecordBytes {
		t.Fatalf("after a second trim: %d markers, %d bytes", strings.Count(again, sessionTrimMarker), len(again))
	}
	if !strings.HasSuffix(again, "сообщение 1400 про миграцию индекса и порядок выката") {
		t.Fatal("latest message lost after second trim")
	}
}
