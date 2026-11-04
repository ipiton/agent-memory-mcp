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
// With an agent name first (`setup codex --yes`) it registers the MCP server in
// that agent's config instead — see setup_agents.go.
func runSetup(args []string) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return runSetupAgent(args[0], args[1:])
	}
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

	for _, event := range []string{"SessionStart", "SessionEnd", "PreCompact"} {
		found := false
		entries, _ := hooksMap[event].([]any)
		for _, entry := range entries {
			group, _ := entry.(map[string]any)
			// A flat entry from an older setup is not looked at: Claude Code skips it.
			inner, _ := group["hooks"].([]any)
			for _, hook := range inner {
				m, _ := hook.(map[string]any)
				cmd, _ := m["command"].(string)
				ours, current := isOurHookCommand(cmd, currentCommand)
				if ours {
					// Hook exists but points to a different binary — needs update.
					if !current {
						return false
					}
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func mergeHooks(settings map[string]any, hooks map[string][]hookGroup) {
	existing, _ := settings["hooks"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}

	for event, groups := range hooks {
		merged := withoutOurHooks(existing[event])
		for _, group := range groups {
			inner := make([]any, len(group.Hooks))
			for i, e := range group.Hooks {
				inner[i] = map[string]any{
					"type":    e.Type,
					"command": e.Command,
				}
			}
			merged = append(merged, map[string]any{"hooks": inner})
		}
		existing[event] = merged
	}

	settings["hooks"] = existing
}

// withoutOurHooks returns an event's entries minus this binary's hooks, flat ones
// from an older setup included. Everything else in the event stays as it was.
func withoutOurHooks(entries any) []any {
	arr, _ := entries.([]any)
	kept := make([]any, 0, len(arr))
	for _, entry := range arr {
		group, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		if cmd, ok := group["command"].(string); ok {
			if ours, _ := isOurHookCommand(cmd, ""); !ours {
				kept = append(kept, entry)
			}
			continue
		}
		inner, ok := group["hooks"].([]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		rest := make([]any, 0, len(inner))
		for _, hook := range inner {
			m, _ := hook.(map[string]any)
			cmd, _ := m["command"].(string)
			if ours, _ := isOurHookCommand(cmd, ""); !ours {
				rest = append(rest, hook)
			}
		}
		if len(rest) > 0 {
			group["hooks"] = rest
			kept = append(kept, group)
		}
	}
	return kept
}

// isOurHookCommand parses a hook command line (e.g.
// "/usr/local/bin/agent-memory-mcp hook session-start") and reports:
//   - ours:    the first token's basename is exactly "agent-memory-mcp"
//   - current: ours == true AND the first token is currentCommand
//
// Strict basename match avoids false positives like "agent-memory-mcp-old"
// or "/path/to/something-else/agent-memory-mcp" buried mid-string.
func isOurHookCommand(cmd, currentCommand string) (ours bool, current bool) {
	bin := hookCommandBinary(cmd)
	if filepath.Base(bin) != "agent-memory-mcp" {
		return false, false
	}
	// buildHooksConfig writes an absolute path whatever it was given.
	absolute, err := filepath.Abs(currentCommand)
	return true, bin == currentCommand || (err == nil && bin == absolute)
}

// hookCommandBinary returns a command line's first token, without the single quotes
// buildHooksConfig puts around it.
func hookCommandBinary(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if rest, ok := strings.CutPrefix(cmd, "'"); ok {
		if end := strings.IndexByte(rest, '\''); end >= 0 {
			return rest[:end]
		}
	}
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
