package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// runSetup automatically configures Claude Code hooks in ~/.claude/settings.json.
func runSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	command := fs.String("command", defaultConfigCommand(), "Path to agent-memory-mcp binary")
	dryRun := fs.Bool("dry-run", false, "Show what would be written without modifying files")
	mustParse(fs, args)

	settingsPath := claudeSettingsPath()
	hooks := buildHooksConfig(*command)

	existing, err := readJSONFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", settingsPath, err)
		os.Exit(1)
	}
	if existing == nil {
		existing = make(map[string]any)
	}

	if hooksAlreadyConfigured(existing) {
		fmt.Fprintf(os.Stderr, "Hooks already configured in %s — skipping.\n", settingsPath)
		return
	}

	mergeHooks(existing, hooks)

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *dryRun {
		fmt.Fprintf(os.Stderr, "Would write to %s:\n", settingsPath)
		fmt.Println(string(data))
		return
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error creating directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(settingsPath, append(data, '\n'), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", settingsPath, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Claude Code hooks configured in %s\n", settingsPath)
}

func claudeSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func readJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return result, nil
}

func hooksAlreadyConfigured(settings map[string]any) bool {
	hooks, ok := settings["hooks"]
	if !ok {
		return false
	}
	hooksMap, ok := hooks.(map[string]any)
	if !ok {
		return false
	}
	// Check if any of our hook events are already set.
	for _, event := range []string{"SessionStart", "SessionEnd", "PreCompact"} {
		if entries, ok := hooksMap[event]; ok {
			if arr, ok := entries.([]any); ok && len(arr) > 0 {
				// Check if it's our hook (contains "agent-memory-mcp").
				for _, entry := range arr {
					if m, ok := entry.(map[string]any); ok {
						if cmd, ok := m["command"].(string); ok {
							if containsStr(cmd, "agent-memory-mcp") {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

func mergeHooks(settings map[string]any, hooks map[string][]hookEntry) {
	existing, _ := settings["hooks"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}

	for event, entries := range hooks {
		// Convert hookEntry to map[string]any for JSON.
		jsonEntries := make([]any, len(entries))
		for i, e := range entries {
			jsonEntries[i] = map[string]any{
				"type":    e.Type,
				"command": e.Command,
			}
		}
		existing[event] = jsonEntries
	}

	settings["hooks"] = existing
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
