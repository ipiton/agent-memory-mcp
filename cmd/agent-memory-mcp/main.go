package main

import (
	"context"
	"errors"
	"flag"
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
	if err := run(os.Args[1:]); err != nil {
		// flag.ErrHelp means -h/--help was requested; usage was already
		// printed by the FlagSet, so exit cleanly.
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// run is the single dispatch point. Every subcommand handler returns an error
// instead of calling os.Exit, so deferred cleanup (SQLite WAL sync, engine
// stop) runs before the process exits and handlers stay unit-testable.
func run(argv []string) error {
	// Backward compat: no subcommand or flags starting with "-" → serve
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return runServe(argv)
	}

	cmd := argv[0]
	args := argv[1:]

	switch cmd {
	case "serve":
		return runServe(args)
	case "store":
		return runStore(args)
	case "recall":
		return runRecall(args)
	case "list":
		return runList(args)
	case "delete":
		return runDelete(args)
	case "search":
		return runSearch(args)
	case "index":
		return runIndex(args)
	case "close-session":
		return runCloseSession(args)
	case "review-session":
		return runReviewSession(args)
	case "accept-session":
		return runAcceptSession(args)
	case "stats":
		return runStats(args)
	case "config":
		return runConfig(args)
	case "project-bank":
		return runProjectBank(args)
	case "resolve-review-item":
		return runResolveReviewItem(args)
	case "reembed":
		return runReembed(args)
	case "export":
		return runExport(args)
	case "import":
		return runImport(args)
	case "setup":
		return runSetup(args)
	case "hooks-config":
		return runHooksConfig(args)
	case "context-inject":
		return runContextInject(args)
	case "auto-capture":
		return runAutoCapture(args)
	case "checkpoint":
		return runCheckpoint(args)
	case "sweep-archive":
		return runSweepArchive(args)
	case "end-task":
		return runEndTask(args)
	case "mark-dead-end":
		return runMarkDeadEnd(args)
	case "sediment-cycle":
		return runSedimentCycle(args)
	case "recount-refs":
		return runRecountRefs(args)
	case "index-triples":
		return runIndexTriples(args)
	case "dead-ends-stale":
		return runDeadEndsStale(args)
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func runServe(args []string) error {
	// Extract --config before flag.Parse() so dotenv chain uses it.
	args = extractConfigFlag(args)

	// Restore os.Args so flag.Parse() in config.Load() works correctly
	os.Args = append([]string{os.Args[0]}, args...)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	guard, err := paths.NewGuard(cfg)
	if err != nil {
		return fmt.Errorf("failed to build path guard: %w", err)
	}

	srv := server.New(cfg, guard)
	defer srv.Shutdown()

	// Start config file watcher for hot-reload (if config file is known).
	if cfgPath := config.ConfigFilePath(); cfgPath != "" {
		watcher := config.NewWatcher(cfgPath, 30*time.Second, func(oldCfg, newCfg config.Config) {
			fmt.Fprintf(os.Stderr, "Config changed, reloading config...\n")
			srv.ApplyReload(newCfg)
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
			fmt.Fprintf(os.Stderr, "SIGHUP received, reloading config...\n")
			if err := srv.ReloadFromFile(cfgPath); err != nil {
				fmt.Fprintf(os.Stderr, "SIGHUP reload failed: %v\n", err)
			}
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.HTTP.Mode == "http" {
		fmt.Fprintf(os.Stderr, "Starting HTTP server on %s\n", net.JoinHostPort(cfg.HTTP.Host, strconv.Itoa(cfg.HTTP.Port)))
		if err := server.RunHTTP(ctx, srv); err != nil {
			return fmt.Errorf("http server error: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Server stopped gracefully\n")
	} else {
		if err := server.RunStdio(srv); err != nil {
			return fmt.Errorf("mcp server stopped: %w", err)
		}
	}
	return nil
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
  mark-dead-end    Record an abandoned approach with its failure rationale so future agents avoid it
  dead-ends-stale  List dead_end memories older than --age (default 12 months) for re-evaluation

Memory sedimentation (T48):
  sediment-cycle  Scan memories for layer transitions; trivial ones auto-apply, non-trivial ones queue for review
  recount-refs    Backfill referenced_by_count metadata from existing cross-memory edges (idempotent)

Knowledge graph (T50):
  index-triples   Retrofit (subj, rel, obj) triples for memories that lack them (idempotent, --resume by default)

Run "agent-memory-mcp <command> -help" for details on a command.
`)
}
