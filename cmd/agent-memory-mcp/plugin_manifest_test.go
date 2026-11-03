package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// T167: Claude Code reads these files only at install time on someone else's
// machine, so a broken reference shows up nowhere in this repository's own runs.
func TestPluginManifests(t *testing.T) {
	repo := filepath.Join("..", "..")

	var plugin struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	readJSON(t, filepath.Join(repo, "plugin", ".claude-plugin", "plugin.json"), &plugin)
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(plugin.Version) {
		t.Errorf("plugin version %q is not what scripts/plugin-pin.sh writes", plugin.Version)
	}

	var marketplace struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	readJSON(t, filepath.Join(repo, ".claude-plugin", "marketplace.json"), &marketplace)
	if len(marketplace.Plugins) != 1 || marketplace.Plugins[0].Name != plugin.Name {
		t.Fatalf("marketplace does not list plugin %q: %+v", plugin.Name, marketplace.Plugins)
	}
	if _, err := os.Stat(filepath.Join(repo, marketplace.Plugins[0].Source, ".claude-plugin", "plugin.json")); err != nil {
		t.Errorf("marketplace source: %v", err)
	}

	// A pinned archive of another version means launch.sh would refuse to install.
	sums, err := os.ReadFile(filepath.Join(repo, "plugin", "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(sums), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "agent-memory-mcp-"+plugin.Version+"-") {
			t.Errorf("checksums.txt does not match plugin version %s: %s", plugin.Version, line)
		}
	}

	// Every hook has to be a subcommand this binary has, with the flag it reads.
	var hooks struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	readJSON(t, filepath.Join(repo, "plugin", "hooks", "hooks.json"), &hooks)
	want := map[string]string{
		"SessionStart": "launch.sh hook context-inject",
		"SessionEnd":   "launch.sh hook auto-capture --hook-event",
		"PreCompact":   "launch.sh hook checkpoint --hook-event",
	}
	if len(hooks.Hooks) != len(want) {
		t.Errorf("hook events = %d, want %d", len(hooks.Hooks), len(want))
	}
	for event, suffix := range want {
		entries := hooks.Hooks[event]
		if len(entries) != 1 || len(entries[0].Hooks) != 1 || !strings.HasSuffix(entries[0].Hooks[0].Command, suffix) {
			t.Errorf("%s hook = %+v, want a command ending in %q", event, entries, suffix)
		}
	}

	// The launcher takes these names; see the comment on them in launch.sh.
	var mcp struct {
		MCPServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	readJSON(t, filepath.Join(repo, "plugin", ".mcp.json"), &mcp)
	env := mcp.MCPServers["agent-memory"].Env
	if env["AMM_PLUGIN_DATA"] != "${CLAUDE_PLUGIN_DATA}" || env["AMM_PROJECT_DIR"] != "${CLAUDE_PROJECT_DIR}" {
		t.Errorf("mcp server env = %v", env)
	}
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// The first session of a project: the hook fires before anything created the bank.
func TestContextInjectWithoutDatabaseIsSilent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("MCP_MEMORY_DB_PATH", filepath.Join(dir, "absent", "memories.db"))
	t.Setenv("MCP_RAG_INDEX_PATH", filepath.Join(dir, "rag"))
	t.Setenv("MCP_DATA_PATH", dir)
	if err := runContextInject(nil); err != nil {
		t.Fatalf("context-inject: %v", err)
	}
}
