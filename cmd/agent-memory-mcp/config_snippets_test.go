package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateClientConfigClaudeDesktop(t *testing.T) {
	snippet, configPath, err := generateClientConfig("claude-desktop", clientConfigOptions{
		ServerName:  "memory",
		ProjectRoot: "/tmp/project",
		Command:     "/usr/local/bin/agent-memory-mcp",
	})
	if err != nil {
		t.Fatalf("generateClientConfig: %v", err)
	}
	if configPath != "~/Library/Application Support/Claude/claude_desktop_config.json" {
		t.Fatalf("configPath = %q", configPath)
	}

	var payload map[string]map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(snippet), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	server := payload["mcpServers"]["memory"]
	if server.Command != shellLauncher {
		t.Fatalf("command = %q, want %q", server.Command, shellLauncher)
	}
	if len(server.Args) != 2 {
		t.Fatalf("args len = %d, want 2", len(server.Args))
	}
	if server.Args[0] != "-lc" {
		t.Fatalf("args[0] = %q, want -lc", server.Args[0])
	}
	if want := "cd '/tmp/project' && exec '/usr/local/bin/agent-memory-mcp'"; server.Args[1] != want {
		t.Fatalf("args[1] = %q, want %q", server.Args[1], want)
	}
}

func TestGenerateClientConfigCodex(t *testing.T) {
	snippet, configPath, err := generateClientConfig("codex", clientConfigOptions{
		ServerName:  "memory",
		ProjectRoot: "/tmp/project",
		Command:     "agent-memory-mcp",
	})
	if err != nil {
		t.Fatalf("generateClientConfig: %v", err)
	}
	if configPath != "~/.codex/config.toml" {
		t.Fatalf("configPath = %q", configPath)
	}

	if !strings.Contains(snippet, `[mcp_servers.memory]`) {
		t.Fatalf("snippet = %q, want mcp_servers section", snippet)
	}
	if !strings.Contains(snippet, `command = "/bin/sh"`) {
		t.Fatalf("snippet = %q, want shell command", snippet)
	}
	if !strings.Contains(snippet, `args = ["-lc", "cd '/tmp/project' && exec 'agent-memory-mcp'"]`) {
		t.Fatalf("snippet = %q, want launcher args", snippet)
	}
}

func TestGenerateClientConfigRejectsUnknownClient(t *testing.T) {
	_, _, err := generateClientConfig("unknown", clientConfigOptions{
		ServerName:  "memory",
		ProjectRoot: "/tmp/project",
		Command:     "agent-memory-mcp",
	})
	if err == nil {
		t.Fatal("expected error for unknown client")
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	got := shellQuote(`/tmp/it's/project`)
	want := `'/tmp/it'"'"'s/project'`
	if got != want {
		t.Fatalf("shellQuote = %q, want %q", got, want)
	}
}

func TestWorkflowHintSnippetMentionsSessionCloseFlow(t *testing.T) {
	hint := workflowHintSnippet()
	for _, expected := range []string{
		"summarize_project_context",
		"project_bank_view",
		"close_session",
		"accept_session_changes",
		"raw-only",
	} {
		if !strings.Contains(hint, expected) {
			t.Fatalf("workflow hint missing %q:\n%s", expected, hint)
		}
	}
}
