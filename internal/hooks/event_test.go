package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The defect this file exists for: stdin carries the event, not the session.
// Taking it verbatim is what put 1102 records of `{"session_id":…}` in a live
// bank, so a parser that accepts prose would reintroduce it.
func TestParseEventRejectsProse(t *testing.T) {
	if _, err := ParseEvent([]byte("Fixed the decoder and shipped 0.13.1")); err == nil {
		t.Fatal("prose parsed as a hook event; want an error naming --hook-event")
	}
	if _, err := ParseEvent([]byte("   ")); err == nil {
		t.Fatal("empty stdin parsed as a hook event")
	}
}

func TestParseEventReadsClaudeCodeShape(t *testing.T) {
	raw := `{"session_id":"abc123def456","transcript_path":"/tmp/t.jsonl","cwd":"/Users/vit/Sema","hook_event_name":"SessionEnd","reason":"other"}`
	event, err := ParseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if event.SessionID != "abc123def456" || event.TranscriptPath != "/tmp/t.jsonl" {
		t.Errorf("event = %+v", event)
	}
	if event.Name != "SessionEnd" || event.Reason != "other" {
		t.Errorf("event = %+v", event)
	}
}

// The context label decides whether a second session of the evening survives:
// sessionclose folds same-context records within six hours, and only replaces
// the text at 0.95 lexical overlap, which two sessions never reach.
func TestContextLabelKeepsSessionsApart(t *testing.T) {
	event := Event{SessionID: "abc123def456789", CWD: "/Users/vit/Documents/Moving"}
	if got := event.ContextLabel(""); got != "Moving-abc123de" {
		t.Errorf("ContextLabel = %q, want %q", got, "Moving-abc123de")
	}
	if got := event.ContextLabel("  moving-release  "); got != "moving-release" {
		t.Errorf("explicit context = %q, want it untouched", got)
	}
	if got := (Event{CWD: "/Users/vit/Sema"}).ContextLabel(""); got != "Sema" {
		t.Errorf("no session id: got %q, want %q", got, "Sema")
	}
	if got := (Event{}).ContextLabel(""); got != "session" {
		t.Errorf("empty event: got %q, want %q", got, "session")
	}
}

func writeTranscript(t *testing.T, records []map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var sb strings.Builder
	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func msg(kind, text string) map[string]any {
	return map[string]any{
		"type":    kind,
		"message": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}},
	}
}

func TestSummarizeTranscriptKeepsTheConversation(t *testing.T) {
	path := writeTranscript(t, []map[string]any{
		msg("user", "why is the session hook silent"),
		msg("assistant", "Close waits on the startup re-embed"),
		msg("summary", "ignored record type"),
	})

	got, err := SummarizeTranscript(path)
	if err != nil {
		t.Fatalf("SummarizeTranscript: %v", err)
	}
	want := "User: why is the session hook silent\n\nAssistant: Close waits on the startup re-embed"
	if got != want {
		t.Errorf("summary =\n%q\nwant\n%q", got, want)
	}
}

// Everything a session record must NOT carry: sidechain turns, attachments,
// injected context blocks, and the non-text blocks of a mixed content array —
// tool payloads and thinking signatures are larger than the conversation and
// say nothing about it.
func TestSummarizeTranscriptDropsNonConversation(t *testing.T) {
	sidechain := msg("assistant", "subagent chatter")
	sidechain["isSidechain"] = true
	attachment := msg("user", "pasted file body")
	attachment["attachment"] = map[string]any{"type": "file"}

	mixed := map[string]any{
		"type": "assistant",
		"message": map[string]any{"content": []map[string]any{
			{"type": "thinking", "thinking": "long private reasoning", "signature": "base64…"},
			{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ls"}},
			{"type": "text", "text": "the visible answer"},
		}},
	}

	path := writeTranscript(t, []map[string]any{
		msg("user", "<system-reminder>injected context</system-reminder>"),
		msg("user", "<corpus-search>results</corpus-search>"),
		sidechain,
		attachment,
		mixed,
		msg("user", "   "),
	})

	got, err := SummarizeTranscript(path)
	if err != nil {
		t.Fatalf("SummarizeTranscript: %v", err)
	}
	if got != "Assistant: the visible answer" {
		t.Errorf("summary = %q, want only the visible text block", got)
	}
}

func TestSummarizeTranscriptBoundsSizeAndKeepsTheEnd(t *testing.T) {
	records := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		records = append(records, msg("user", strings.Repeat("x", 2000)+string(rune('a'+i%26))))
	}
	records = append(records, msg("assistant", "the last thing said"))

	got, err := SummarizeTranscript(writeTranscript(t, records))
	if err != nil {
		t.Fatalf("SummarizeTranscript: %v", err)
	}
	if len(got) > maxTranscriptTotal {
		t.Errorf("summary is %d chars, want at most %d", len(got), maxTranscriptTotal)
	}
	if !strings.HasSuffix(got, "the last thing said") {
		t.Error("the tail of the conversation was dropped; the summary must keep the end, not the start")
	}
	if strings.Contains(got, strings.Repeat("x", maxTranscriptPerMessage+1)) {
		t.Error("a single message exceeded the per-message cap")
	}
}

// A half-written last line is normal for a transcript of a session that is
// still closing. It must not cost the rest of the conversation.
func TestSummarizeTranscriptSurvivesATruncatedLine(t *testing.T) {
	path := writeTranscript(t, []map[string]any{msg("user", "first")})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(`{"type":"assistant","message":{"content":[{"type":"te`); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	got, err := SummarizeTranscript(path)
	if err != nil {
		t.Fatalf("SummarizeTranscript: %v", err)
	}
	if got != "User: first" {
		t.Errorf("summary = %q, want the complete records only", got)
	}
}

func TestSummarizeTranscriptMissingFile(t *testing.T) {
	if _, err := SummarizeTranscript(filepath.Join(t.TempDir(), "absent.jsonl")); err == nil {
		t.Fatal("a missing transcript decoded without error")
	}
}
