package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// hookEventFor builds the SessionEnd payload Claude Code writes, pointing at a
// transcript that carries the given line of conversation.
func hookEventFor(t *testing.T, sessionID, cwd, line string) string {
	t.Helper()
	transcript := writeTestTranscript(t,
		`{"type":"user","message":{"content":[{"type":"text","text":`+quoteJSON(t, line)+`}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Understood."}]}}`,
	)
	return hookEventJSON(t, map[string]any{
		"session_id":      sessionID,
		"transcript_path": transcript,
		"cwd":             cwd,
		"hook_event_name": "SessionEnd",
	})
}

func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestTwoHeadlessSessionsKeepTheirContent (T130) drives the whole hook path
// twice, the way two headless sessions in one project do, and checks the bank
// rather than the report: two records, each holding its own session's
// conversation, both filed under the project label so an exact-match recall
// finds them.
func TestTwoHeadlessSessionsKeepTheirContent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mem.db")
	t.Setenv("MCP_MEMORY_DB_PATH", dbPath)
	t.Setenv("MCP_RAG_ENABLED", "false")
	project := filepath.Join(dir, "Moving")

	// Both lines run past the 100-character dedup floor, so the writes are
	// decided by consolidation rather than by the "content too short" gate.
	const firstLine = "why did the archive sweep call every procedural memory a promotion candidate even at threshold 1.0, and what did the live corpus say"
	const secondLine = "the embedding cache never saw the writes the CLI made, so recall answered from a view that was minutes behind the file on disk"

	withStdin(t, hookEventFor(t, "sess-aaaaaaaa", project, firstLine))
	var err error
	captureStdout(t, func() { err = runAutoCapture([]string{"--hook-event"}) })
	if err != nil {
		t.Fatalf("first session: %v", err)
	}

	withStdin(t, hookEventFor(t, "sess-bbbbbbbb", project, secondLine))
	captureStdout(t, func() { err = runAutoCapture([]string{"--hook-event"}) })
	if err != nil {
		t.Fatalf("second session: %v", err)
	}

	store, err := memory.NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("open bank: %v", err)
	}
	defer func() { _ = store.Close() }()

	items, err := store.List(context.Background(), memory.Filters{Context: "Moving", Type: memory.TypeEpisodic}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var summaries []*memory.Memory
	for _, m := range items {
		if memory.IsTerminalRecord(m) {
			summaries = append(summaries, m)
		}
	}
	if len(summaries) != 2 {
		t.Fatalf("two sessions produced %d session-summary records, want 2", len(summaries))
	}

	joined := summaries[0].Content + "\n" + summaries[1].Content
	for _, line := range []string{firstLine, secondLine} {
		if !strings.Contains(joined, line) {
			t.Errorf("no record carries the line %q — a session's content was dropped", line)
		}
	}
	ids := map[string]bool{}
	for _, m := range summaries {
		ids[m.Metadata[memory.MetadataAgentSessionID]] = true
	}
	if !ids["sess-aaaaaaaa"] || !ids["sess-bbbbbbbb"] {
		t.Errorf("records do not carry their own session ids: %v", ids)
	}
}
