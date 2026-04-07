package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func runHooksConfig(args []string) {
	fs := flag.NewFlagSet("hooks-config", flag.ExitOnError)
	command := fs.String("command", defaultConfigCommand(), "Path to agent-memory-mcp binary")
	jsonOut := fs.Bool("json", false, "Output raw JSON only (no instructions)")
	mustParse(fs, args)

	binaryPath := *command
	if binaryPath == "" {
		binaryPath = "agent-memory-mcp"
	}

	hooks := buildHooksConfig(binaryPath)

	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		fmt.Println(string(data))
		return
	}

	fmt.Fprintf(os.Stderr, "Add the following to your .claude/settings.json under \"hooks\":\n\n")
	fmt.Println(string(data))
	fmt.Fprintf(os.Stderr, "\nHook descriptions:\n")
	fmt.Fprintf(os.Stderr, "  SessionStart  — injects recent memories and project context into the session\n")
	fmt.Fprintf(os.Stderr, "  SessionEnd    — auto-captures session knowledge (extract, plan, apply)\n")
	fmt.Fprintf(os.Stderr, "  PreCompact    — saves a checkpoint before context window compression\n")
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func buildHooksConfig(binaryPath string) map[string][]hookEntry {
	bin := shellQuoteHooks(binaryPath)
	return map[string][]hookEntry{
		"SessionStart": {
			{
				Type:    "command",
				Command: fmt.Sprintf("%s context-inject", bin),
			},
		},
		"SessionEnd": {
			{
				Type:    "command",
				Command: fmt.Sprintf("%s auto-capture --stdin", bin),
			},
		},
		"PreCompact": {
			{
				Type:    "command",
				Command: fmt.Sprintf("%s checkpoint --boundary pre_compact --stdin", bin),
			},
		},
	}
}

func shellQuoteHooks(path string) string {
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = absPath
	}
	return shellQuote(path)
}
