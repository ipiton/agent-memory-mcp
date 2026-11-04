package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runHooksConfig(args []string) error {
	fs := flag.NewFlagSet("hooks-config", flag.ContinueOnError)
	command := fs.String("command", defaultConfigCommand(), "Path to agent-memory-mcp binary")
	jsonOut := fs.Bool("json", false, "Output raw JSON only (no instructions)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	binaryPath := *command
	if binaryPath == "" {
		binaryPath = "agent-memory-mcp"
	}

	hooks := buildHooksConfig(binaryPath)

	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}

	if *jsonOut {
		fmt.Println(string(data))
		return nil
	}

	fmt.Fprintf(os.Stderr, "Add the following to your .claude/settings.json under \"hooks\":\n\n")
	fmt.Println(string(data))
	fmt.Fprintf(os.Stderr, "\nHook descriptions:\n")
	fmt.Fprintf(os.Stderr, "  SessionStart  — injects recent memories and project context into the session\n")
	fmt.Fprintf(os.Stderr, "  SessionEnd    — auto-captures session knowledge (extract, plan, apply)\n")
	fmt.Fprintf(os.Stderr, "  compile       — runs after both, only with MCP_COMPILE_BACKGROUND=true\n")
	fmt.Fprintf(os.Stderr, "  PreCompact    — saves a checkpoint before context window compression\n")
	return nil
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// hookGroup is one element of an event's array in settings.json. Claude Code skips
// an element with no "hooks" array, so a bare hookEntry there never runs.
type hookGroup struct {
	Hooks []hookEntry `json:"hooks"`
}

func buildHooksConfig(binaryPath string) map[string][]hookGroup {
	bin := shellQuoteHooks(binaryPath)
	command := func(format string) []hookGroup {
		return []hookGroup{{Hooks: []hookEntry{{
			Type:    "command",
			Command: strings.ReplaceAll(format, "BIN", bin),
		}}}}
	}
	// compile shares a command with what precedes it: Claude Code runs the hooks of
	// one event in parallel, and compile has to see the summary auto-capture wrote.
	return map[string][]hookGroup{
		"SessionStart": command("BIN context-inject; BIN compile --detach"),
		"SessionEnd":   command("BIN auto-capture --hook-event; BIN compile --detach"),
		"PreCompact":   command("BIN checkpoint --hook-event"),
	}
}

func shellQuoteHooks(path string) string {
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = absPath
	}
	return shellQuote(path)
}
