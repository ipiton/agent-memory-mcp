package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testLauncher = "cd '/tmp/project' && exec '/usr/local/bin/agent-memory-mcp'"

// foreignConfig is a config that already holds someone else's server and keys
// we do not own, in each target's format.
func foreignConfig(format agentConfigFormat) string {
	switch format {
	case formatCodexTOML:
		return "model = \"o3\"\n\n[mcp_servers.other]\ncommand = \"npx\"\nargs = [\"x\"]\n\n[mcp_servers.other.env]\nTOKEN_FILE = \"/x\"\n"
	case formatVSCode:
		return "{\n    \"servers\": {\n        \"other\": {\"type\": \"stdio\", \"command\": \"npx\"}\n    },\n    \"inputs\": []\n}\n"
	case formatOpenCode:
		return "{\n  \"$schema\": \"https://opencode.ai/config.json\",\n  \"mcp\": {\"other\": {\"type\": \"local\", \"command\": [\"x\"]}},\n  \"theme\": \"dark\"\n}\n"
	default:
		return "{\n  \"zeta\": 1,\n  \"mcpServers\": {\"other\": {\"command\": \"npx\", \"args\": [\"x\"]}},\n  \"alpha\": {\"nested\": true}\n}\n"
	}
}

// conflictingConfig holds an entry named "memory" that launches something
// else and carries a field of its own that --force must keep.
func conflictingConfig(format agentConfigFormat) string {
	switch format {
	case formatCodexTOML:
		return "[mcp_servers.memory]\ncommand = \"npx\"\nargs = [\"-y\", \"mcp-remote\", \"http://localhost:18080/mcp\"]\n\n[mcp_servers.memory.tools.recall_memory]\napproval_mode = \"approve\"\n"
	case formatVSCode:
		return `{"servers": {"memory": {"type": "stdio", "command": "npx", "env": {"KEEP": "1"}}}}`
	case formatOpenCode:
		return `{"mcp": {"memory": {"type": "local", "command": ["npx"], "environment": {"KEEP": "1"}}}}`
	default:
		return `{"mcpServers": {"memory": {"command": "npx", "env": {"KEEP": "1"}}}}`
	}
}

func testSetupOpts(yes, force bool) agentSetupOptions {
	return agentSetupOptions{
		ServerName: "memory",
		Launcher:   testLauncher,
		Yes:        yes,
		Force:      force,
		Now:        time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
}

// assertRegistered checks the file now launches our server under "memory".
func assertRegistered(t *testing.T, target agentTarget, data []byte) {
	t.Helper()
	if target.Format == formatCodexTOML {
		for _, want := range strings.Split(strings.TrimRight(codexConfigSnippet("memory", testLauncher), "\n"), "\n") {
			if !strings.Contains(string(data), want) {
				t.Fatalf("config lacks %q:\n%s", want, data)
			}
		}
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("written file is not JSON: %v\n%s", err, data)
	}
	var servers map[string]map[string]any
	if err := json.Unmarshal(doc[containerKey(target.Format)], &servers); err != nil {
		t.Fatalf("%s is not an object of servers: %v", containerKey(target.Format), err)
	}
	entry := servers["memory"]
	if !launchFieldsEqual(entry, launchEntry(target, testLauncher)) {
		t.Fatalf("entry = %v, want launch fields %v", entry, launchEntry(target, testLauncher))
	}
}

func backups(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".bak-agent-memory-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestSetupAgentAllTargets(t *testing.T) {
	for _, target := range agentTargets {
		t.Run(target.Name, func(t *testing.T) {
			t.Run("missing file: dry run writes nothing, --yes creates it", func(t *testing.T) {
				home := t.TempDir()
				path := target.Path(home, "darwin")

				r, err := setupAgent(target, home, "darwin", testSetupOpts(false, false))
				if err != nil {
					t.Fatalf("dry run: %v", err)
				}
				if r.Action != "create" || r.Written {
					t.Fatalf("dry run result = %+v, want create, not written", r)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("dry run created %s", path)
				}

				r, err = setupAgent(target, home, "darwin", testSetupOpts(true, false))
				if err != nil {
					t.Fatalf("--yes: %v", err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				assertRegistered(t, target, data)
				if r.Backup != "" || len(backups(t, path)) != 0 {
					t.Errorf("a new file needs no backup, got %q", r.Backup)
				}
			})

			t.Run("foreign config: kept, backed up, re-run is a no-op", func(t *testing.T) {
				home := t.TempDir()
				path := target.Path(home, "darwin")
				original := foreignConfig(target.Format)
				writeFixture(t, path, original)

				r, err := setupAgent(target, home, "darwin", testSetupOpts(false, false))
				if err != nil {
					t.Fatalf("dry run: %v", err)
				}
				if r.Action != "add" {
					t.Fatalf("dry run action = %q, want add", r.Action)
				}
				assertBytes(t, path, original, "dry run changed the file")
				if len(backups(t, path)) != 0 {
					t.Fatal("dry run wrote a backup")
				}

				r, err = setupAgent(target, home, "darwin", testSetupOpts(true, false))
				if err != nil {
					t.Fatalf("--yes: %v", err)
				}
				data, _ := os.ReadFile(path)
				assertRegistered(t, target, data)
				assertForeignKept(t, target, original, data)
				assertBytes(t, r.Backup, original, "backup differs from the original")

				written := string(data)
				r, err = setupAgent(target, home, "darwin", testSetupOpts(true, false))
				if err != nil {
					t.Fatalf("re-run: %v", err)
				}
				if r.Action != "unchanged" || r.Written {
					t.Fatalf("re-run result = %+v, want unchanged", r)
				}
				assertBytes(t, path, written, "re-run changed the file")
				if n := len(backups(t, path)); n != 1 {
					t.Fatalf("re-run left %d backups, want 1", n)
				}
			})

			t.Run("same name, other launch: refused without --force, launch-only replace with it", func(t *testing.T) {
				home := t.TempDir()
				path := target.Path(home, "darwin")
				original := conflictingConfig(target.Format)
				writeFixture(t, path, original)

				if _, err := setupAgent(target, home, "darwin", testSetupOpts(true, false)); err == nil || !strings.Contains(err.Error(), "--force") {
					t.Fatalf("err = %v, want a refusal pointing at --force", err)
				}
				assertBytes(t, path, original, "a refused run changed the file")

				r, err := setupAgent(target, home, "darwin", testSetupOpts(true, true))
				if err != nil {
					t.Fatalf("--force: %v", err)
				}
				if r.Action != "update" || r.Old == "" {
					t.Fatalf("--force result = %+v, want update with the old entry shown", r)
				}
				data, _ := os.ReadFile(path)
				assertRegistered(t, target, data)
				keep := `"KEEP"`
				if target.Format == formatCodexTOML {
					keep = "[mcp_servers.memory.tools.recall_memory]\napproval_mode = \"approve\""
				}
				if !strings.Contains(string(data), keep) {
					t.Fatalf("--force dropped the entry's own settings %s:\n%s", keep, data)
				}
			})

			if target.Format != formatCodexTOML {
				t.Run("JSON with comments is refused, not rewritten", func(t *testing.T) {
					home := t.TempDir()
					path := target.Path(home, "darwin")
					original := "{\n  // user note\n  \"" + containerKey(target.Format) + "\": {}\n}\n"
					writeFixture(t, path, original)

					if _, err := setupAgent(target, home, "darwin", testSetupOpts(true, false)); err == nil || !strings.Contains(err.Error(), "not plain JSON") {
						t.Fatalf("err = %v, want a not-plain-JSON refusal", err)
					}
					assertBytes(t, path, original, "a refused run changed the file")
				})
			}
		})
	}
}

func TestSetupAgentVSCodeLinuxPath(t *testing.T) {
	target, _ := findAgentTarget("vscode")
	if got, want := target.Path("/home/u", "linux"), "/home/u/.config/Code/User/mcp.json"; got != want {
		t.Fatalf("linux path = %q, want %q", got, want)
	}
}

func TestRunSetupAgentRejectsUnknownAgentAndBadName(t *testing.T) {
	if err := runSetupAgent("notepad", nil); err == nil || !strings.Contains(err.Error(), "claude-code") {
		t.Fatalf("err = %v, want unknown-agent error listing the targets", err)
	}
	if err := runSetupAgent("cursor", []string{"--name", "a.b"}); err == nil {
		t.Fatal("a dotted server name must be refused: it would split the TOML table name")
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertBytes(t *testing.T, path, want, msg string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
	if string(got) != want {
		t.Fatalf("%s:\n got: %s\nwant: %s", msg, got, want)
	}
}

// assertForeignKept: every key and server we do not own survives with its
// value, and top-level key order is unchanged.
func assertForeignKept(t *testing.T, target agentTarget, original string, written []byte) {
	t.Helper()
	if target.Format == formatCodexTOML {
		if !strings.HasPrefix(string(written), original) {
			t.Fatalf("codex config was rewritten instead of appended to:\n%s", written)
		}
		return
	}
	before, err := decodeOrderedObject([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	after, err := decodeOrderedObject(written)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("top-level keys %d → %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Key != after[i].Key {
			t.Fatalf("top-level key order changed: %q at %d became %q", before[i].Key, i, after[i].Key)
		}
		if before[i].Key == containerKey(target.Format) {
			var servers map[string]json.RawMessage
			_ = json.Unmarshal(after[i].Value, &servers)
			if _, ok := servers["other"]; !ok {
				t.Fatalf("foreign server dropped: %s", after[i].Value)
			}
			continue
		}
		if !jsonEqual(before[i].Value, after[i].Value) {
			t.Fatalf("key %q changed: %s → %s", before[i].Key, before[i].Value, after[i].Value)
		}
	}
}

func jsonEqual(a, b []byte) bool {
	var ca, cb bytes.Buffer
	return json.Compact(&ca, a) == nil && json.Compact(&cb, b) == nil && ca.String() == cb.String()
}
