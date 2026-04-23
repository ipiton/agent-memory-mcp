package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// runRecountRefs drives the one-time backfill of referenced_by_count metadata
// from existing cross-memory edges (avoided_dead_end_id + superseded_by).
// Idempotent: re-runs yield Updated=0 once counters match the derived tally.
func runRecountRefs(args []string) {
	fs := flag.NewFlagSet("recount-refs", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "Preview changes without writing; Updated counts rows that would change")
	jsonOut := fs.Bool("json", false, "Output JSON instead of human-readable text")
	verbose := fs.Bool("verbose", false, "List every changed memory ID and new count")
	mustParse(fs, args)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	result, err := store.RecountReferences(context.Background(), *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(result)
		return
	}

	mode := "live"
	if result.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("Recount references (%s):\n", mode)
	fmt.Printf("- Scanned: %d\n", result.Scanned)
	fmt.Printf("- Updated: %d\n", result.Updated)
	if *verbose && len(result.Counts) > 0 {
		fmt.Println("\nChanges:")
		for id, count := range result.Counts {
			fmt.Printf("- %s → %d\n", id, count)
		}
	}
}
