package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// T159: `setup <agent>` registers the MCP server in an agent's own config file.
// Bare `setup` keeps configuring Claude Code hooks — the Homebrew caveat runs it.
//
// Writing into another tool's config is an Ask-First action, so the defaults
// are the cautious ones: without --yes nothing is written and the plan is
// printed; a write backs the original up first; a re-run on a configured file
// is a no-op; an entry of the same name that launches something else is left
// alone unless --force, and even then only its launch fields are replaced —
// per-tool approvals, env and the like stay. Keys outside our entry are never
// touched.

type agentConfigFormat int

const (
	formatMCPServers agentConfigFormat = iota // {"mcpServers": {name: {...}}}
	formatVSCode                              // {"servers": {name: {"type": "stdio", ...}}}
	formatOpenCode                            // {"mcp": {name: {"type": "local", "command": [...]}}}
	formatCodexTOML                           // [mcp_servers.name]
)

type agentTarget struct {
	Name   string
	Label  string
	Format agentConfigFormat
	// TypeField is the "type" value Claude Code expects next to command/args.
	TypeField string
	Path      func(home, goos string) string
	// ManualHint is shown when the file cannot be edited safely.
	ManualHint string
}

var agentTargets = []agentTarget{
	{Name: "claude-code", Label: "Claude Code", Format: formatMCPServers, TypeField: "stdio",
		Path:       func(home, _ string) string { return filepath.Join(home, ".claude.json") },
		ManualHint: "claude mcp add-json --scope user <name> '<json>'"},
	{Name: "codex", Label: "Codex", Format: formatCodexTOML,
		Path: func(home, _ string) string { return filepath.Join(home, ".codex", "config.toml") }},
	{Name: "cursor", Label: "Cursor", Format: formatMCPServers,
		Path: func(home, _ string) string { return filepath.Join(home, ".cursor", "mcp.json") }},
	{Name: "gemini", Label: "Gemini CLI", Format: formatMCPServers,
		Path: func(home, _ string) string { return filepath.Join(home, ".gemini", "settings.json") }},
	{Name: "opencode", Label: "OpenCode", Format: formatOpenCode,
		Path: func(home, _ string) string { return filepath.Join(home, ".config", "opencode", "opencode.json") }},
	{Name: "vscode", Label: "VS Code (Copilot)", Format: formatVSCode,
		Path: func(home, goos string) string {
			if goos == "darwin" {
				return filepath.Join(home, "Library", "Application Support", "Code", "User", "mcp.json")
			}
			return filepath.Join(home, ".config", "Code", "User", "mcp.json")
		},
		ManualHint: "code --add-mcp '{\"name\":\"<name>\",...}'"},
	{Name: "windsurf", Label: "Windsurf", Format: formatMCPServers,
		Path: func(home, _ string) string { return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json") }},
}

func findAgentTarget(name string) (agentTarget, bool) {
	for _, t := range agentTargets {
		if t.Name == strings.ToLower(name) {
			return t, true
		}
	}
	return agentTarget{}, false
}

func agentTargetNames() string {
	names := make([]string, len(agentTargets))
	for i, t := range agentTargets {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type agentSetupOptions struct {
	ServerName string
	Launcher   string // shell line run by /bin/sh -lc
	Yes        bool
	Force      bool
	Now        time.Time
}

type agentSetupResult struct {
	Path    string
	Action  string // "create", "add", "update", "unchanged"
	Old     string // previous entry, for update
	New     string // entry as written
	Backup  string
	Written bool
}

func runSetupAgent(name string, args []string) error {
	target, ok := findAgentTarget(name)
	if !ok {
		return fmt.Errorf("unknown agent %q: use one of %s (or bare `setup` for Claude Code hooks)", name, agentTargetNames())
	}

	fs := flag.NewFlagSet("setup "+target.Name, flag.ContinueOnError)
	serverName := fs.String("name", "memory", "Server name in the agent config")
	root := fs.String("root", "", "Project root the server runs from (default: current directory)")
	command := fs.String("command", defaultConfigCommand(), "Path to agent-memory-mcp binary")
	yes := fs.Bool("yes", false, "Write the change (default: print the plan only)")
	force := fs.Bool("force", false, "Replace the launch command of an existing entry with the same name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !serverNamePattern.MatchString(*serverName) {
		return fmt.Errorf("server name %q: use letters, digits, '-' or '_'", *serverName)
	}

	projectRoot := *root
	if projectRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		projectRoot = cwd
	}
	projectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	result, err := setupAgent(target, home, runtime.GOOS, agentSetupOptions{
		ServerName: *serverName,
		Launcher:   buildShellLauncher(projectRoot, *command),
		Yes:        *yes,
		Force:      *force,
		Now:        time.Now(),
	})
	if err != nil {
		return err
	}
	printAgentSetupResult(os.Stderr, target, *serverName, result, *yes)
	return nil
}

func printAgentSetupResult(w io.Writer, target agentTarget, serverName string, r agentSetupResult, yes bool) {
	if r.Action == "unchanged" {
		fmt.Fprintf(w, "%s: server %q already configured in %s — nothing to do.\n", target.Label, serverName, r.Path)
		return
	}
	verb := map[string]string{"create": "create the file with server", "add": "add server", "update": "replace the launch command of server"}[r.Action]
	if r.Written {
		fmt.Fprintf(w, "%s: %s %q in %s\n", target.Label, pastTense(r.Action), serverName, r.Path)
		if r.Backup != "" {
			fmt.Fprintf(w, "Backup of the previous file: %s\n", r.Backup)
		}
		fmt.Fprintln(w, "Restart the agent to load the server.")
		return
	}
	fmt.Fprintf(w, "%s: would %s %q in %s\n", target.Label, verb, serverName, r.Path)
	if r.Old != "" {
		fmt.Fprintln(w, prefixLines("- ", r.Old))
	}
	fmt.Fprintln(w, prefixLines("+ ", r.New))
	if !yes {
		fmt.Fprintln(w, "Dry run: nothing written. Re-run with --yes to apply.")
	}
}

func pastTense(action string) string {
	switch action {
	case "create":
		return "created the file with server"
	case "add":
		return "added server"
	default:
		return "updated the launch command of server"
	}
}

func prefixLines(prefix, text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// setupAgent computes the change and, with Yes, writes it.
func setupAgent(target agentTarget, home, goos string, opts agentSetupOptions) (agentSetupResult, error) {
	path := target.Path(home, goos)
	result := agentSetupResult{Path: path}

	original, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("reading %s: %w", path, err)
	}

	var updated []byte
	if target.Format == formatCodexTOML {
		updated, result.Action, result.Old, result.New, err = applyCodexEntry(original, opts)
	} else {
		updated, result.Action, result.Old, result.New, err = applyJSONEntry(target, original, opts)
	}
	if err != nil {
		if target.ManualHint != "" {
			return result, fmt.Errorf("%s: %w\nAdd it manually instead, e.g. %s", path, err, target.ManualHint)
		}
		return result, fmt.Errorf("%s: %w", path, err)
	}
	if !exists {
		result.Action = "create"
	}
	if result.Action == "unchanged" || !opts.Yes {
		return result, nil
	}

	mode := os.FileMode(0o644)
	if exists {
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
		result.Backup = fmt.Sprintf("%s.bak-agent-memory-%s", path, opts.Now.Format("20060102-150405"))
		if err := os.WriteFile(result.Backup, original, mode); err != nil {
			return result, fmt.Errorf("writing backup %s: %w", result.Backup, err)
		}
	}
	if err := writeFileAtomic(path, updated, mode); err != nil {
		return result, err
	}
	result.Written = true
	return result, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// launchEntry is the entry this binary writes, as a generic JSON object.
func launchEntry(target agentTarget, launcher string) map[string]any {
	args := []any{"-lc", launcher}
	switch target.Format {
	case formatOpenCode:
		return map[string]any{"type": "local", "command": []any{shellLauncher, "-lc", launcher}, "enabled": true}
	case formatVSCode:
		return map[string]any{"type": "stdio", "command": shellLauncher, "args": args}
	default:
		entry := map[string]any{"command": shellLauncher, "args": args}
		if target.TypeField != "" {
			entry["type"] = target.TypeField
		}
		return entry
	}
}

func containerKey(format agentConfigFormat) string {
	switch format {
	case formatVSCode:
		return "servers"
	case formatOpenCode:
		return "mcp"
	default:
		return "mcpServers"
	}
}

// jsonMember is one key of a JSON object with its value kept verbatim, so a
// rewrite keeps the file's key order and every value we do not own byte-equal
// (modulo indentation).
type jsonMember struct {
	Key   string
	Value json.RawMessage
}

func decodeOrderedObject(data []byte) ([]jsonMember, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("expected a JSON object")
	}
	var members []jsonMember
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		members = append(members, jsonMember{Key: key, Value: raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON object")
	}
	return members, nil
}

func encodeOrderedObject(members []jsonMember) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(m.Key)
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.Value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func setMember(members []jsonMember, key string, value json.RawMessage) []jsonMember {
	for i := range members {
		if members[i].Key == key {
			members[i].Value = value
			return members
		}
	}
	return append(members, jsonMember{Key: key, Value: value})
}

func applyJSONEntry(target agentTarget, original []byte, opts agentSetupOptions) ([]byte, string, string, string, error) {
	top := []jsonMember{}
	if len(bytes.TrimSpace(original)) > 0 {
		var err error
		if top, err = decodeOrderedObject(original); err != nil {
			return nil, "", "", "", fmt.Errorf("not plain JSON (comments or trailing commas are not edited automatically): %v", err)
		}
	}

	key := containerKey(target.Format)
	servers := []jsonMember{}
	for _, m := range top {
		if m.Key == key {
			var err error
			if servers, err = decodeOrderedObject(m.Value); err != nil {
				return nil, "", "", "", fmt.Errorf("%q is not an object: %v", key, err)
			}
		}
	}

	want := launchEntry(target, opts.Launcher)
	action := "add"
	oldText := ""
	entry := want
	for _, m := range servers {
		if m.Key != opts.ServerName {
			continue
		}
		var existing map[string]any
		if err := json.Unmarshal(m.Value, &existing); err != nil {
			return nil, "", "", "", fmt.Errorf("entry %q is not an object: %v", opts.ServerName, err)
		}
		if launchFieldsEqual(existing, want) {
			return original, "unchanged", "", "", nil
		}
		oldText = indentJSON(m.Value)
		if !opts.Force {
			return nil, "", "", "", fmt.Errorf("server %q already exists and launches something else:\n%s\nUse --force to replace its launch command (other fields are kept) or --name to pick another name", opts.ServerName, oldText)
		}
		for k, v := range want {
			existing[k] = v
		}
		entry = existing
		action = "update"
	}

	// No HTML escaping: the launcher's "&&" would otherwise land in a file
	// people read as "&&".
	var entryBuf bytes.Buffer
	enc := json.NewEncoder(&entryBuf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entry); err != nil {
		return nil, "", "", "", err
	}
	entryRaw := bytes.TrimRight(entryBuf.Bytes(), "\n")
	servers = setMember(servers, opts.ServerName, entryRaw)
	top = setMember(top, key, encodeOrderedObject(servers))

	var out bytes.Buffer
	if err := json.Indent(&out, encodeOrderedObject(top), "", detectIndent(original)); err != nil {
		return nil, "", "", "", err
	}
	out.WriteByte('\n')
	return out.Bytes(), action, oldText, indentJSON(entryRaw), nil
}

func launchFieldsEqual(existing, want map[string]any) bool {
	for k, v := range want {
		if !reflect.DeepEqual(existing[k], v) {
			return false
		}
	}
	return true
}

func indentJSON(raw []byte) string {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return string(raw)
	}
	return b.String()
}

// detectIndent keeps the file's own indentation unit (two spaces when unknown).
func detectIndent(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// applyCodexEntry edits ~/.codex/config.toml as text: the stdlib has no TOML
// encoder, and a round-trip through one would reformat the user's file anyway.
// Only the [mcp_servers.<name>] table's own command/args lines are ever
// replaced; its sub-tables (per-tool approvals, env) are left as they are.
func applyCodexEntry(original []byte, opts agentSetupOptions) ([]byte, string, string, string, error) {
	snippet := codexConfigSnippet(opts.ServerName, opts.Launcher)
	wantLines := strings.Split(strings.TrimRight(snippet, "\n"), "\n")[1:] // command, args
	text := string(original)
	lines := strings.Split(text, "\n")
	header := "[mcp_servers." + opts.ServerName + "]"

	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			start = i
			break
		}
	}
	if start < 0 {
		var b strings.Builder
		b.WriteString(text)
		if text != "" && !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
		if text != "" {
			b.WriteByte('\n')
		}
		b.WriteString(snippet)
		return []byte(b.String()), "add", "", snippet, nil
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	found := map[string]int{}
	var oldLines []string
	for i := start + 1; i < end; i++ {
		key := tomlKey(lines[i])
		if key != "command" && key != "args" {
			continue
		}
		if strings.Count(lines[i], "[") != strings.Count(lines[i], "]") {
			return nil, "", "", "", fmt.Errorf("server %q has a multi-line %s value; edit it by hand", opts.ServerName, key)
		}
		found[key] = i
		oldLines = append(oldLines, strings.TrimSpace(lines[i]))
	}
	sort.Strings(oldLines)
	sortedWant := append([]string(nil), wantLines...)
	sort.Strings(sortedWant)
	if reflect.DeepEqual(oldLines, sortedWant) {
		return original, "unchanged", "", "", nil
	}
	oldText := header + "\n" + strings.Join(oldLines, "\n")
	if !opts.Force {
		return nil, "", "", "", fmt.Errorf("server %q already exists and launches something else:\n%s\nUse --force to replace its command/args (sub-tables are kept) or --name to pick another name", opts.ServerName, oldText)
	}

	replaced := append([]string(nil), lines...)
	var insert []string
	for _, wl := range wantLines {
		if idx, ok := found[tomlKey(wl)]; ok {
			replaced[idx] = wl
		} else {
			insert = append(insert, wl)
		}
	}
	if len(insert) > 0 {
		replaced = append(replaced[:start+1], append(insert, replaced[start+1:]...)...)
	}
	return []byte(strings.Join(replaced, "\n")), "update", oldText, header + "\n" + strings.Join(wantLines, "\n"), nil
}

func tomlKey(line string) string {
	key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}
