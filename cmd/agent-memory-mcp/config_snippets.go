package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const shellLauncher = "/bin/sh"

type clientConfigOptions struct {
	ServerName  string
	ProjectRoot string
	Command     string
}

func runConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	name := fs.String("name", "memory", "Server name in the client config")
	root := fs.String("root", "", "Project root to run from (default: current directory)")
	command := fs.String("command", defaultConfigCommand(), "Executable to launch")
	mustParse(fs, args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "error: target client is required: claude-desktop, cursor, or codex")
		fs.Usage()
		os.Exit(1)
	}

	projectRoot := *root
	if projectRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		projectRoot = cwd
	}

	snippet, configPath, err := generateClientConfig(fs.Arg(0), clientConfigOptions{
		ServerName:  *name,
		ProjectRoot: projectRoot,
		Command:     *command,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if configPath != "" {
		fmt.Fprintf(os.Stderr, "Paste into %s\n\n", configPath)
	}
	fmt.Fprintf(os.Stderr, "Suggested workflow:\n%s\n\n", workflowHintSnippet())
	fmt.Println(snippet)
}

func generateClientConfig(target string, opts clientConfigOptions) (snippet string, configPath string, err error) {
	projectRoot, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve project root: %w", err)
	}
	if opts.ServerName == "" {
		opts.ServerName = "memory"
	}
	if opts.Command == "" {
		opts.Command = "agent-memory-mcp"
	}

	launcher := buildShellLauncher(projectRoot, opts.Command)

	switch strings.ToLower(target) {
	case "claude-desktop", "claude":
		snippet, err = jsonConfigSnippet(opts.ServerName, launcher)
		if err != nil {
			return "", "", err
		}
		return snippet, "~/Library/Application Support/Claude/claude_desktop_config.json", nil
	case "cursor":
		snippet, err = jsonConfigSnippet(opts.ServerName, launcher)
		if err != nil {
			return "", "", err
		}
		return snippet, "~/.cursor/mcp.json", nil
	case "codex":
		return codexConfigSnippet(opts.ServerName, launcher), "~/.codex/config.toml", nil
	default:
		return "", "", fmt.Errorf("unsupported client %q: use claude-desktop, cursor, or codex", target)
	}
}

func jsonConfigSnippet(serverName string, launcher string) (string, error) {
	payload := map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"command": shellLauncher,
				"args": []string{
					"-lc",
					launcher,
				},
			},
		},
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config snippet: %w", err)
	}
	return string(data), nil
}

func codexConfigSnippet(serverName string, launcher string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp_servers.%s]\n", serverName)
	fmt.Fprintf(&b, "command = %s\n", strconv.Quote(shellLauncher))
	fmt.Fprintf(&b, "args = [%s, %s]\n", strconv.Quote("-lc"), strconv.Quote(launcher))
	return b.String()
}

func buildShellLauncher(projectRoot string, command string) string {
	return fmt.Sprintf("cd %s && exec %s", shellQuote(projectRoot), shellQuote(command))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func defaultConfigCommand() string {
	path, err := os.Executable()
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
		if err == nil && filepath.Base(path) == "agent-memory-mcp" && !strings.Contains(path, string(filepath.Separator)+"go-build") {
			return path
		}
	}
	return "agent-memory-mcp"
}

func workflowHintSnippet() string {
	return strings.Join([]string{
		"- start sessions with summarize_project_context or project_bank_view",
		"- end sessions with close_session or review_session_changes",
		"- apply low-risk updates with accept_session_changes",
		"- fall back to raw-only capture when consolidation is too ambiguous",
	}, "\n")
}
