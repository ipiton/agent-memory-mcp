package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeCyrillicTranscript builds a transcript long enough to hit both byte
// budgets, in a script where a rune is two bytes — the condition under which a
// byte-indexed cut lands mid-codepoint.
func writeCyrillicTranscript(t *testing.T, messages, repeats int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := strings.Repeat("Проверка усечения по байтам ", repeats) + "х"
	lines := make([]string, 0, messages)
	for i := 0; i < messages; i++ {
		raw, err := json.Marshal(map[string]any{
			"type":    "user",
			"message": map[string]any{"content": body},
		})
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		lines = append(lines, string(raw))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// TestSummarizeTranscriptKeepsRunesIntact is T133. Both cuts in
// SummarizeTranscript are byte counts; before the fix the tail cut opened the
// summary mid-rune, and 64 records in the live bank start with U+FFFD because
// of it. The assertion is on UTF-8 validity, not on length: a summary that is
// not valid text is not a summary.
func TestSummarizeTranscriptKeepsRunesIntact(t *testing.T) {
	// 20 messages × ~1.6 KB overflows maxTranscriptPerMessage per message and
	// maxTranscriptTotal overall, so both cuts fire.
	path := writeCyrillicTranscript(t, 20, 60)

	summary, err := SummarizeTranscript(path)
	if err != nil {
		t.Fatalf("SummarizeTranscript: %v", err)
	}
	if !utf8.ValidString(summary) {
		t.Fatalf("summary is not valid UTF-8; first bytes: %q", summary[:min(16, len(summary))])
	}
	if strings.ContainsRune(summary, utf8.RuneError) {
		t.Errorf("summary carries U+FFFD, so a cut landed mid-rune: %q", summary[:min(16, len(summary))])
	}
	if len(summary) > maxTranscriptTotal+utf8.UTFMax {
		t.Errorf("summary is %d bytes, past the %d budget by more than one rune", len(summary), maxTranscriptTotal)
	}
	if !strings.Contains(summary, "Проверка усечения") {
		t.Errorf("summary lost the conversation entirely: %q", summary[:min(64, len(summary))])
	}
}

// TestSummarizeTranscriptShortCyrillicUntouched: the alignment must not trim a
// transcript that fits, which is what the overwhelming majority of them do.
func TestSummarizeTranscriptShortCyrillicUntouched(t *testing.T) {
	path := writeCyrillicTranscript(t, 1, 2)

	summary, err := SummarizeTranscript(path)
	if err != nil {
		t.Fatalf("SummarizeTranscript: %v", err)
	}
	want := "User: " + strings.Repeat("Проверка усечения по байтам ", 2) + "х"
	if summary != want {
		t.Errorf("short transcript was altered:\n got %q\nwant %q", summary, want)
	}
}
