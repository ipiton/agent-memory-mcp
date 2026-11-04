package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const setupTestBinary = "/opt/test/agent-memory-mcp"

// runSetupOn runs setup over the given settings.json and returns what it left there.
func runSetupOn(t *testing.T, settings string, args ...string) map[string]any {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	if settings != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSetup(append([]string{"--command", setupTestBinary}, args...)); err != nil {
		t.Fatal(err)
	}
	result, err := readJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// eventCommands lists the commands Claude Code would run for an event: only those
// inside a "hooks" array, because it skips an element without one.
func eventCommands(settings map[string]any, event string) []string {
	var commands []string
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		inner, _ := group["hooks"].([]any)
		for _, hook := range inner {
			m, _ := hook.(map[string]any)
			command, _ := m["command"].(string)
			commands = append(commands, command)
		}
	}
	return commands
}

func TestSetupWritesHooksClaudeCodeRuns(t *testing.T) {
	settings := runSetupOn(t, "")
	want := map[string]string{
		"SessionStart": "'" + setupTestBinary + "' context-inject; '" + setupTestBinary + "' compile --detach",
		"SessionEnd":   "'" + setupTestBinary + "' auto-capture --hook-event; '" + setupTestBinary + "' compile --detach",
		"PreCompact":   "'" + setupTestBinary + "' checkpoint --hook-event",
	}
	for event, command := range want {
		got := eventCommands(settings, event)
		if len(got) != 1 || got[0] != command {
			t.Errorf("%s = %q, want %q", event, got, command)
		}
	}
}

func TestSetupReplacesFlatHooksAndKeepsForeignOnes(t *testing.T) {
	settings := runSetupOn(t, `{
		"model": "keep-me",
		"hooks": {
			"SessionEnd": [
				{"type": "command", "command": "'/old/agent-memory-mcp' auto-capture --hook-event"},
				{"hooks": [
					{"type": "command", "command": "python3 /foreign.py"},
					{"type": "command", "command": "/old/agent-memory-mcp checkpoint --hook-event"}
				]}
			],
			"Stop": [{"hooks": [{"type": "command", "command": "/foreign-stop"}]}]
		}
	}`)
	if settings["model"] != "keep-me" {
		t.Errorf("other settings lost: %v", settings)
	}
	end := eventCommands(settings, "SessionEnd")
	if len(end) != 2 || end[0] != "python3 /foreign.py" || !strings.Contains(end[1], setupTestBinary) {
		t.Errorf("SessionEnd = %q", end)
	}
	if got := eventCommands(settings, "Stop"); len(got) != 1 || got[0] != "/foreign-stop" {
		t.Errorf("Stop = %q", got)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/old/agent-memory-mcp") {
		t.Errorf("an old hook is left: %s", data)
	}
}

func TestSetupLeavesWorkingHooksAlone(t *testing.T) {
	custom := "'" + setupTestBinary + "' context-inject --limit 3"
	settingsJSON := `{"hooks": {
		"SessionStart": [{"hooks": [{"type": "command", "command": "` + custom + `"}]}],
		"SessionEnd": [{"hooks": [{"type": "command", "command": "` + setupTestBinary + ` auto-capture --hook-event"}]}],
		"PreCompact": [{"hooks": [{"type": "command", "command": "` + setupTestBinary + ` checkpoint --hook-event"}]}]
	}}`

	if got := eventCommands(runSetupOn(t, settingsJSON), "SessionStart"); len(got) != 1 || got[0] != custom {
		t.Errorf("without --force: %q", got)
	}
	if got := eventCommands(runSetupOn(t, settingsJSON, "--force"), "SessionStart"); len(got) != 1 || got[0] == custom {
		t.Errorf("with --force: %q", got)
	}
}

func TestIsOurHookCommand(t *testing.T) {
	const current = "/usr/local/bin/agent-memory-mcp"

	tests := []struct {
		name        string
		cmd         string
		current     string
		wantOurs    bool
		wantCurrent bool
	}{
		{
			name:        "exact match — current binary",
			cmd:         "/usr/local/bin/agent-memory-mcp hook session-start",
			current:     current,
			wantOurs:    true,
			wantCurrent: true,
		},
		{
			name:        "ours but different path",
			cmd:         "/Users/vit/.local/bin/agent-memory-mcp hook pre-compact",
			current:     current,
			wantOurs:    true,
			wantCurrent: false,
		},
		{
			name:        "not ours — agent-memory-mcp-old false positive must be rejected",
			cmd:         "/usr/local/bin/agent-memory-mcp-old hook session-start",
			current:     current,
			wantOurs:    false,
			wantCurrent: false,
		},
		{
			name:        "not ours — name appears mid-string in argv",
			cmd:         "/bin/echo agent-memory-mcp foo",
			current:     current,
			wantOurs:    false,
			wantCurrent: false,
		},
		{
			name:        "empty",
			cmd:         "",
			current:     current,
			wantOurs:    false,
			wantCurrent: false,
		},
		{
			name:        "quoted, as buildHooksConfig writes it",
			cmd:         "'/usr/local/bin/agent-memory-mcp' context-inject; '/usr/local/bin/agent-memory-mcp' compile --detach",
			current:     current,
			wantOurs:    true,
			wantCurrent: true,
		},
		{
			name:        "bare binary name",
			cmd:         "agent-memory-mcp hook session-end",
			current:     "agent-memory-mcp",
			wantOurs:    true,
			wantCurrent: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ours, current := isOurHookCommand(tc.cmd, tc.current)
			if ours != tc.wantOurs || current != tc.wantCurrent {
				t.Errorf("isOurHookCommand(%q, %q) = (ours=%v, current=%v), want (%v, %v)",
					tc.cmd, tc.current, ours, current, tc.wantOurs, tc.wantCurrent)
			}
		})
	}
}
