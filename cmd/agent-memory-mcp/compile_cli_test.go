package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// compileBank points the config at a temp bank with one pending summary and a fake
// claude that answers with the given text.
func compileBank(t *testing.T, answer string) (config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("MCP_DATA_PATH", dir)
	t.Setenv("MCP_MEMORY_DB_PATH", filepath.Join(dir, "memories.db"))
	t.Setenv("MCP_RAG_INDEX_PATH", filepath.Join(dir, "rag"))
	t.Setenv("MCP_EMBEDDING_MODE", "local-only")
	t.Setenv(envCompiling, "")

	envelope, err := json.Marshal(map[string]any{"type": "result", "is_error": false, "result": answer})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "answer.json"), envelope, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat >" + dir + "/stdin.txt\nenv >" + dir + "/env.txt\ncat " + dir + "/answer.json\n"
	bin := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_COMPILE_CLAUDE_BIN", bin)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	summary := &memory.Memory{
		Content:  "fixed the rollback script",
		Type:     memory.TypeEpisodic,
		Context:  "proj",
		Metadata: map[string]string{memory.MetadataRecordKind: memory.RecordKindSessionSummary},
	}
	if err := store.Store(context.Background(), summary); err != nil {
		t.Fatal(err)
	}
	return cfg, summary.ID
}

func bankMemories(t *testing.T, cfg config.Config) []*memory.Memory {
	t.Helper()
	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	all, err := store.List(context.Background(), memory.Filters{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func findMemory(all []*memory.Memory, id string) *memory.Memory {
	for _, m := range all {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func TestCompileStoresItemsAndTagsSummary(t *testing.T) {
	cfg, id := compileBank(t, "```json\n"+`{"items":[
		{"kind":"decision","title":"Check kubectl exit code","content":"Rollback checks the exit code.","tags":["k8s","canonical"],"importance":0.7},
		{"kind":"dead_end","title":"Retry loop","content":"Retries hid the failure.","importance":0.5},
		{"kind":"fact","title":"Script lives in deploy","content":"deploy/rollback.sh","importance":0.4},
		{"kind":"procedure","title":"Roll back","content":"Run deploy/rollback.sh.","importance":0.4},
		{"kind":"noise","title":"","content":""}
	]}`+"\n```")

	if err := runCompile(nil); err != nil {
		t.Fatalf("compile: %v", err)
	}

	all := bankMemories(t, cfg)
	if len(all) != 5 {
		t.Fatalf("memories = %d, want the summary and 4 items", len(all))
	}
	if !hasTag(findMemory(all, id).Tags, tagCompiled) {
		t.Errorf("summary tags = %v, want %q", findMemory(all, id).Tags, tagCompiled)
	}
	entities := map[memory.EngineeringType]bool{}
	for _, m := range all {
		if m.ID == id {
			continue
		}
		entities[memory.EngineeringTypeOf(m)] = true
		if m.Context != "proj" || m.Metadata[memory.MetadataDerivedFrom] != id {
			t.Errorf("item %q: context %q, derived_from %q", m.Title, m.Context, m.Metadata[memory.MetadataDerivedFrom])
		}
		if memory.ProvenanceOf(m) != memory.ProvenanceConversational {
			t.Errorf("item %q: provenance %q", m.Title, memory.ProvenanceOf(m))
		}
		if hasTag(m.Tags, "canonical") {
			t.Errorf("item %q kept the model's canonical tag: %v", m.Title, m.Tags)
		}
	}
	if !entities[memory.EngineeringTypeDecision] || !entities[memory.EngineeringTypeDeadEnd] {
		t.Errorf("entities = %v", entities)
	}

	// The second run has nothing to do: the summary is out of the queue.
	if err := os.Remove(cfg.Compile.ClaudeBin); err != nil {
		t.Fatal(err)
	}
	if err := runCompile(nil); err != nil {
		t.Fatalf("second compile: %v", err)
	}
}

func TestCompileChildEnvironment(t *testing.T) {
	cfg, _ := compileBank(t, `{"items":[]}`)
	t.Setenv("CLAUDECODE", "1")
	if err := runCompile(nil); err != nil {
		t.Fatalf("compile: %v", err)
	}
	env, err := os.ReadFile(filepath.Join(cfg.DataPath, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), envCompiling+"=1") || strings.Contains(string(env), "CLAUDECODE=") {
		t.Errorf("child environment:\n%s", env)
	}
	stdin, err := os.ReadFile(filepath.Join(cfg.DataPath, "stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdin), "<session_summary>\nfixed the rollback script") {
		t.Errorf("child stdin: %s", stdin)
	}
}

// A bad answer leaves the summary pending, and after the limit sets it aside so it
// stops costing a call per run.
func TestCompileBadAnswerRetriesThenSetsAside(t *testing.T) {
	cfg, id := compileBank(t, "I could not find anything useful.")
	for attempt := 1; attempt <= maxCompileAttempts; attempt++ {
		if err := runCompile(nil); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		summary := findMemory(bankMemories(t, cfg), id)
		if hasTag(summary.Tags, tagCompiled) {
			t.Fatalf("attempt %d tagged a failed summary compiled", attempt)
		}
		if got, want := hasTag(summary.Tags, tagCompileFailed), attempt == maxCompileAttempts; got != want {
			t.Fatalf("attempt %d: set aside = %v, want %v", attempt, got, want)
		}
	}
}

// A claude that cannot run says nothing about the summary: no attempt is counted.
func TestCompileClaudeFailureKeepsSummaryPending(t *testing.T) {
	cfg, id := compileBank(t, `{"items":[]}`)
	t.Setenv("MCP_COMPILE_CLAUDE_BIN", filepath.Join(cfg.DataPath, "no-such-claude"))
	if err := runCompile(nil); err == nil {
		t.Fatal("compile without claude reported success")
	}
	summary := findMemory(bankMemories(t, cfg), id)
	if hasTag(summary.Tags, tagCompiled) || summary.Metadata[metadataCompileAttempts] != "" {
		t.Errorf("summary after a failed call: tags %v, metadata %v", summary.Tags, summary.Metadata)
	}
}

func TestCompileRespectsLock(t *testing.T) {
	cfg, id := compileBank(t, `{"items":[]}`)
	unlock, locked, err := lockCompile(cfg.DataPath)
	if err != nil || !locked {
		t.Fatalf("lock: %v, %v", locked, err)
	}
	defer unlock()
	if err := runCompile(nil); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if hasTag(findMemory(bankMemories(t, cfg), id).Tags, tagCompiled) {
		t.Error("compile ran past the lock")
	}
}

func TestHooksDoNothingInsideCompile(t *testing.T) {
	cfg, _ := compileBank(t, `{"items":[]}`)
	before := len(bankMemories(t, cfg))
	t.Setenv(envCompiling, "1")

	out := captureStdout(t, func() {
		if err := runContextInject(nil); err != nil {
			t.Errorf("context-inject: %v", err)
		}
		if err := runAutoCapture([]string{"--summary", "a long enough session summary that dedup would normally accept it"}); err != nil {
			t.Errorf("auto-capture: %v", err)
		}
		if err := runCheckpoint([]string{"--summary", "a long enough checkpoint summary that dedup would normally accept it"}); err != nil {
			t.Errorf("checkpoint: %v", err)
		}
		if err := runCompile(nil); err != nil {
			t.Errorf("compile: %v", err)
		}
	})
	if out != "" {
		t.Errorf("hooks printed %q", out)
	}
	if after := len(bankMemories(t, cfg)); after != before {
		t.Errorf("memories %d -> %d", before, after)
	}
}

func TestCompileLeavesOldSummariesAlone(t *testing.T) {
	cfg, id := compileBank(t, `{"items":[{"kind":"noise"}]}`)
	t.Setenv("MCP_COMPILE_BACKGROUND", "true")

	// The form created_at really has in a bank, which SQLite's date functions cannot read.
	db, err := sql.Open("sqlite", cfg.Memory.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour).String()
	if _, err := db.Exec(`UPDATE memories SET created_at = ? WHERE id = ?`, old, id); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if err := runCompile(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataPath, "stdin.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an old summary reached the model: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runContextInject(nil); err != nil {
			t.Error(err)
		}
	})
	if strings.Contains(out, "waiting for background") {
		t.Errorf("an old summary counts as waiting:\n%s", out)
	}

	if err := runCompile([]string{"--max-age", "0"}); err != nil {
		t.Fatal(err)
	}
	if summary := findMemory(bankMemories(t, cfg), id); summary == nil || !slices.Contains(summary.Tags, tagCompiled) {
		t.Errorf("--max-age 0 did not take the old summary: %+v", summary)
	}
}

func TestContextInjectPendingModes(t *testing.T) {
	compileBank(t, `{"items":[]}`)

	out := captureStdout(t, func() {
		if err := runContextInject(nil); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "Pending session summaries") || !strings.Contains(out, "fixed the rollback script") {
		t.Errorf("without background compile:\n%s", out)
	}

	t.Setenv("MCP_COMPILE_BACKGROUND", "true")
	out = captureStdout(t, func() {
		if err := runContextInject(nil); err != nil {
			t.Error(err)
		}
	})
	if strings.Contains(out, "Pending session summaries") || strings.Contains(out, "fixed the rollback script") {
		t.Errorf("summary reached the session:\n%s", out)
	}
	if !strings.Contains(out, "1 session summaries are waiting") {
		t.Errorf("no queue line:\n%s", out)
	}
}

func TestParseCompileOutputRejects(t *testing.T) {
	envelope := func(result string, isError bool) []byte {
		data, _ := json.Marshal(map[string]any{"is_error": isError, "result": result})
		return data
	}
	for name, result := range map[string]string{
		"prose":        "nothing here",
		"no items":     `{"things":[]}`,
		"unknown kind": `{"items":[{"kind":"secret","title":"t","content":"c"}]}`,
		"empty title":  `{"items":[{"kind":"fact","title":" ","content":"c"}]}`,
		"empty text":   `{"items":[{"kind":"fact","title":"t","content":""}]}`,
		"importance":   `{"items":[{"kind":"fact","title":"t","content":"c","importance":7}]}`,
	} {
		if _, err := parseCompileOutput(envelope(result, false)); !errors.Is(err, errCompileOutput) {
			t.Errorf("%s: %v, want errCompileOutput", name, err)
		}
	}
	// These end the run instead of counting against a summary.
	if _, err := parseCompileOutput(envelope("Invalid API key", true)); err == nil || errors.Is(err, errCompileOutput) {
		t.Errorf("is_error: %v", err)
	}
	if _, err := parseCompileOutput([]byte("not json")); err == nil || errors.Is(err, errCompileOutput) {
		t.Errorf("broken envelope: %v", err)
	}
}
