package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withStdin points os.Stdin at a file holding the given bytes, the way a hook
// runner would.
func withStdin(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write stdin file: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stdin file: %v", err)
	}
	original := os.Stdin
	os.Stdin = file
	t.Cleanup(func() {
		os.Stdin = original
		file.Close()
	})
}

func writeTestTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func hookEventJSON(t *testing.T, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(raw)
}

func TestResolveSessionInputReadsTheTranscriptTheEventPointsAt(t *testing.T) {
	transcript := writeTestTranscript(t,
		`{"type":"user","message":{"content":[{"type":"text","text":"why did the hook go quiet"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Close waited on the re-embed"}]}}`,
	)
	withStdin(t, hookEventJSON(t, map[string]any{
		"session_id":      "abc123def456",
		"transcript_path": transcript,
		"cwd":             "/Users/vit/Documents/Moving",
		"hook_event_name": "SessionEnd",
	}))

	input, err := resolveSessionInput(true, "", false, "", nil)
	if err != nil {
		t.Fatalf("resolveSessionInput: %v", err)
	}
	if !input.Captured {
		t.Fatal("Captured = false for an event with a readable transcript")
	}
	if !strings.Contains(input.Summary, "why did the hook go quiet") {
		t.Errorf("summary lost the conversation: %q", input.Summary)
	}
	if input.Context != "Moving-abc123de" {
		t.Errorf("context = %q, want %q", input.Context, "Moving-abc123de")
	}
	if input.Event.Name != "SessionEnd" {
		t.Errorf("event name = %q", input.Event.Name)
	}
}

// The regression this whole flag is about: the event object must never become
// the record. Under --hook-event prose is refused outright.
func TestResolveSessionInputRefusesProseAsAnEvent(t *testing.T) {
	withStdin(t, "we fixed the decoder today")
	if _, err := resolveSessionInput(true, "", false, "", nil); err == nil {
		t.Fatal("prose accepted as a hook event")
	}
}

// An event with no transcript is a session with nothing to save, not an error
// the hook runner should see.
func TestResolveSessionInputWithoutTranscriptCapturesNothing(t *testing.T) {
	withStdin(t, hookEventJSON(t, map[string]any{"session_id": "x", "cwd": "/tmp", "hook_event_name": "SessionEnd"}))

	input, err := resolveSessionInput(true, "", false, "", nil)
	if err != nil {
		t.Fatalf("resolveSessionInput: %v", err)
	}
	if input.Captured {
		t.Error("Captured = true with no transcript path")
	}
	if input.Context != "tmp-x" {
		t.Errorf("context = %q, want %q", input.Context, "tmp-x")
	}
}

func TestResolveSessionInputMissingTranscriptFileCapturesNothing(t *testing.T) {
	withStdin(t, hookEventJSON(t, map[string]any{
		"transcript_path": filepath.Join(t.TempDir(), "gone.jsonl"),
		"cwd":             "/tmp",
		"hook_event_name": "SessionEnd",
	}))

	input, err := resolveSessionInput(true, "", false, "", nil)
	if err != nil {
		t.Fatalf("a missing transcript must not fail the hook: %v", err)
	}
	if input.Captured {
		t.Error("Captured = true for a transcript that does not exist")
	}
}

// Without the flag nothing changes: stdin is the summary text, as every
// existing invocation expects.
func TestResolveSessionInputKeepsTheTextContract(t *testing.T) {
	withStdin(t, "  released 0.13.1 and fixed the gate  ")

	input, err := resolveSessionInput(false, "", true, "release", nil)
	if err != nil {
		t.Fatalf("resolveSessionInput: %v", err)
	}
	if input.Summary != "released 0.13.1 and fixed the gate" {
		t.Errorf("summary = %q", input.Summary)
	}
	if input.Context != "release" {
		t.Errorf("context = %q, want the flag value", input.Context)
	}
	if !input.Captured {
		t.Error("Captured = false on the text path")
	}
}

// The generated configuration is the contract most users get; it must not hand
// them the shape that stored 1102 event objects.
func TestGeneratedHooksConfigUsesTheEventContract(t *testing.T) {
	cfg := buildHooksConfig("/usr/local/bin/agent-memory-mcp")
	for _, hook := range []string{"SessionEnd", "PreCompact"} {
		entries := cfg[hook]
		if len(entries) != 1 {
			t.Fatalf("%s: %d entries, want 1", hook, len(entries))
		}
		if !strings.Contains(entries[0].Command, "--hook-event") {
			t.Errorf("%s command = %q, want --hook-event", hook, entries[0].Command)
		}
		if strings.Contains(entries[0].Command, "--stdin") {
			t.Errorf("%s command still passes --stdin: %q", hook, entries[0].Command)
		}
	}
}
