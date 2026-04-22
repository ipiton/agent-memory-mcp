package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"github.com/ipiton/agent-memory-mcp/internal/server"
)

func main() {
	// Backward compat: no subcommand or flags starting with "-" → serve
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		runServe(os.Args[1:])
		return
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "serve":
		runServe(args)
	case "store":
		runStore(args)
	case "recall":
		runRecall(args)
	case "list":
		runList(args)
	case "delete":
		runDelete(args)
	case "search":
		runSearch(args)
	case "index":
		runIndex(args)
	case "close-session":
		runCloseSession(args)
	case "review-session":
		runReviewSession(args)
	case "accept-session":
		runAcceptSession(args)
	case "stats":
		runStats(args)
	case "config":
		runConfig(args)
	case "project-bank":
		runProjectBank(args)
	case "resolve-review-item":
		runResolveReviewItem(args)
	case "reembed":
		runReembed(args)
	case "export":
		runExport(args)
	case "import":
		runImport(args)
	case "setup":
		runSetup(args)
	case "hooks-config":
		runHooksConfig(args)
	case "context-inject":
		runContextInject(args)
	case "auto-capture":
		runAutoCapture(args)
	case "checkpoint":
		runCheckpoint(args)
	case "sweep-archive":
		runSweepArchive(args)
	case "end-task":
		runEndTask(args)
	case "mark-dead-end":
		runMarkDeadEnd(args)
	default:
		printUsage()
		os.Exit(1)
	}
}

func runServe(args []string) {
	// Extract --config before flag.Parse() so dotenv chain uses it.
	args = extractConfigFlag(args)

	// Restore os.Args so flag.Parse() in config.Load() works correctly
	os.Args = append([]string{os.Args[0]}, args...)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	guard, err := paths.NewGuard(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build path guard: %v\n", err)
		os.Exit(1)
	}

	srv := server.New(cfg, guard)
	defer srv.Shutdown()

	// Start config file watcher for hot-reload (if config file is known).
	if cfgPath := config.ConfigFilePath(); cfgPath != "" {
		watcher := config.NewWatcher(cfgPath, 30*time.Second, func(oldCfg, newCfg config.Config) {
			fmt.Fprintf(os.Stderr, "Config changed, reloading RAG engine...\n")
			srv.ReloadRAG(newCfg)
		})
		watcher.Start()
		defer watcher.Stop()
	}

	// SIGHUP triggers immediate config reload.
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			cfgPath := config.ConfigFilePath()
			if cfgPath == "" {
				fmt.Fprintf(os.Stderr, "SIGHUP received but no config file path known, skipping reload\n")
				continue
			}
			newCfg, err := config.LoadFromFile(cfgPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "SIGHUP reload failed: %v\n", err)
				continue
			}
			fmt.Fprintf(os.Stderr, "SIGHUP received, reloading RAG engine...\n")
			srv.ReloadRAG(newCfg)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.HTTPMode == "http" {
		fmt.Fprintf(os.Stderr, "Starting HTTP server on %s\n", net.JoinHostPort(cfg.HTTPHost, strconv.Itoa(cfg.HTTPPort)))
		if err := server.RunHTTP(ctx, srv); err != nil {
			fmt.Fprintf(os.Stderr, "http server error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Server stopped gracefully\n")
	} else {
		if err := server.RunStdio(srv); err != nil {
			fmt.Fprintf(os.Stderr, "mcp server stopped: %v\n", err)
			os.Exit(1)
		}
	}
}

// extractConfigFlag scans args for --config or --config=value, sets the
// explicit config path, and returns args with the flag removed (so flag.Parse
// in config.Load() doesn't choke on it).
func extractConfigFlag(args []string) []string {
	var filtered []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--config" || arg == "-config" {
			if i+1 < len(args) {
				config.SetExplicitConfigPath(args[i+1])
				i++ // skip value
			}
			continue
		}
		if strings.HasPrefix(arg, "--config=") {
			config.SetExplicitConfigPath(strings.TrimPrefix(arg, "--config="))
			continue
		}
		if strings.HasPrefix(arg, "-config=") {
			config.SetExplicitConfigPath(strings.TrimPrefix(arg, "-config="))
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `agent-memory-mcp — MCP server and CLI for agent memory & RAG

Usage:
  agent-memory-mcp [command] [flags]

Commands:
  serve     Start MCP server (stdio/http) — default when no command given
  store     Store a memory
  recall    Semantic search in memories
  list      List memories with filters
  delete    Delete a memory by ID
  search    RAG hybrid search across documents
  index     Re-index documents for RAG
  close-session Analyze an end-of-session summary
  review-session Show the review-oriented close-session report
  accept-session Save raw summary and auto-apply low-risk session changes
  stats     Show memory statistics
  config    Generate ready MCP client config snippets
  project-bank Show structured project bank views
  resolve-review-item Resolve a pending review queue item
  reembed   Re-generate memory embeddings with the active model
  export    Export all memories to JSON (stdout)
  import    Import memories from JSON (file or stdin)

Setup:
  setup           Auto-configure Claude Code hooks in ~/.claude/settings.json

Hooks (for Claude Code integration):
  hooks-config    Generate Claude Code hooks configuration (manual)
  context-inject  Output recent memories for session start injection
  auto-capture    Capture session knowledge from transcript (stdin)
  checkpoint      Save a session checkpoint (pre-compact or manual)

Task lifecycle (T47):
  sweep-archive   Mark working memories tied to archived task slugs as outdated
  end-task        Explicitly consolidate working memories for a single archived task slug

Knowledge capture (T46):
  mark-dead-end   Record an abandoned approach with its failure rationale so future agents avoid it

Run "agent-memory-mcp <command> -help" for details on a command.
`)
}
