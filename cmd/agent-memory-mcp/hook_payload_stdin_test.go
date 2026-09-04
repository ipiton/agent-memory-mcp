package main

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// precompactPayload is the object Claude Code wrote to the PreCompact hook on
// 2026-09-04, copied from a record it produced in the live brew bank. Two of
// its keys — scratchpad_dir and custom_instructions — sit outside the T80
// metadata whitelist, which is why every payload of this shape used to be
// stored as if it were a session summary.
const precompactPayload = `{"session_id":"7e913ee6-8fb6-4305-abd4-0f6d5d7cf396",` +
	`"transcript_path":"/Users/vit/.claude/projects/-Users-vit-Sema/7e913ee6.jsonl",` +
	`"cwd":"/Users/vit/Sema/croqui-frontend",` +
	`"scratchpad_dir":"/private/tmp/claude-501/7e913ee6/scratchpad",` +
	`"prompt_id":"5a801064-6dc0-44c5-8226-a530f537fe31",` +
	`"hook_event_name":"PreCompact","trigger":"auto","custom_instructions":null}`

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = write

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(read)
		done <- string(out)
	}()

	fn()

	os.Stdout = orig
	_ = write.Close()
	out := <-done
	_ = read.Close()
	return out
}

// countMemories reports how many records the CLI actually wrote. A checkpoint
// that reports a skip but stores the row anyway is the failure this pins, so
// the assertion goes to the bank and not to the message.
func countMemories(t *testing.T, dbPath string) int {
	t.Helper()
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return 0
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open bank: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM memories").Scan(&n); err != nil {
		t.Fatalf("count memories: %v", err)
	}
	return n
}

// TestCheckpointStdinRefusesHookEventPayload is T132: `checkpoint --stdin` fed
// a hook event must store nothing and say what to do instead. Before the fix
// it stored the event object as the session's content — 78 such records in the
// live bank, all of them PreCompact.
func TestCheckpointStdinRefusesHookEventPayload(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	t.Setenv("MCP_MEMORY_DB_PATH", dbPath)
	withStdin(t, precompactPayload)

	var err error
	out := captureStdout(t, func() {
		err = runCheckpoint([]string{"--boundary", "pre_compact", "--stdin"})
	})
	if err != nil {
		t.Fatalf("a misconfigured hook must not fail the session: %v", err)
	}
	if n := countMemories(t, dbPath); n != 0 {
		t.Errorf("stored %d records for a hook payload, want 0", n)
	}
	if !strings.Contains(out, "--hook-event") {
		t.Errorf("skip message does not name the fix: %q", out)
	}
}

// TestCheckpointStdinKeepsProseSummary is the regression guard on the other
// side: the refusal above must not touch what --stdin is for.
func TestCheckpointStdinKeepsProseSummary(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	t.Setenv("MCP_MEMORY_DB_PATH", dbPath)
	withStdin(t, "Traced the empty analyses to the ML gateway dropping the persona field on retry; "+
		"added the field to the retry envelope and confirmed against the staging bank that the analysis comes back filled.")

	var err error
	out := captureStdout(t, func() {
		err = runCheckpoint([]string{"--boundary", "pre_compact", "--stdin"})
	})
	if err != nil {
		t.Fatalf("runCheckpoint: %v", err)
	}
	if n := countMemories(t, dbPath); n != 1 {
		t.Errorf("stored %d records for a prose summary, want 1", n)
	}
	if !strings.Contains(out, "Checkpoint saved") {
		t.Errorf("prose checkpoint did not report a save: %q", out)
	}
}
