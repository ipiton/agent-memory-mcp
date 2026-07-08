package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runSetup automatically configures Claude Code hooks in ~/.claude/settings.json.
func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	command := fs.String("command", defaultConfigCommand(), "Path to agent-memory-mcp binary")
	dryRun := fs.Bool("dry-run", false, "Show what would be written without modifying files")
	force := fs.Bool("force", false, "Overwrite existing hooks (useful after brew upgrade)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	settingsPath, err := claudeSettingsPath()
	if err != nil {
		return err
	}
	hooks := buildHooksConfig(*command)

	existing, err := readJSONFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", settingsPath, err)
	}
	if existing == nil {
		existing = make(map[string]any)
	}

	if !*force && hooksAlreadyConfigured(existing, *command) {
		fmt.Fprintf(os.Stderr, "Hooks already configured in %s — skipping. Use --force to overwrite.\n", settingsPath)
		return nil
	}

	mergeHooks(existing, hooks)

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}

	if *dryRun {
		fmt.Fprintf(os.Stderr, "Would write to %s:\n", settingsPath)
		fmt.Println(string(data))
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	if err := os.WriteFile(settingsPath, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", settingsPath, err)
	}

	fmt.Fprintf(os.Stderr, "Claude Code hooks configured in %s\n", settingsPath)
	return nil
}

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
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

// hooksAlreadyConfigured returns true only if our hooks exist AND point to the current binary.
func hooksAlreadyConfigured(settings map[string]any, currentCommand string) bool {
	hooks, ok := settings["hooks"]
	if !ok {
		return false
	}
	hooksMap, ok := hooks.(map[string]any)
	if !ok {
		return false
	}

	found := 0
	for _, event := range []string{"SessionStart", "SessionEnd", "PreCompact"} {
		entries, ok := hooksMap[event]
		if !ok {
			continue
		}
		arr, ok := entries.([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		for _, entry := range arr {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			cmd, ok := m["command"].(string)
			if !ok {
				continue
			}
			ours, current := isOurHookCommand(cmd, currentCommand)
			if ours {
				// Hook exists but points to a different binary — needs update.
				if !current {
					return false
				}
				found++
			}
		}
	}
	return found >= 3
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

// isOurHookCommand parses a hook command line (e.g.
// "/usr/local/bin/agent-memory-mcp hook session-start") and reports:
//   - ours:    the first token's basename is exactly "agent-memory-mcp"
//   - current: ours == true AND the first token equals currentCommand
//
// Strict basename match avoids false positives like "agent-memory-mcp-old"
// or "/path/to/something-else/agent-memory-mcp" buried mid-string.
func isOurHookCommand(cmd, currentCommand string) (ours bool, current bool) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return false, false
	}
	bin := parts[0]
	if filepath.Base(bin) != "agent-memory-mcp" {
		return false, false
	}
	return true, bin == currentCommand
}
