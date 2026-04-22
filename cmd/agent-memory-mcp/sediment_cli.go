package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// runSedimentCycle drives the T48 sediment-cycle job. Scans memories for
// layer-transition candidates; trivial ones (surface→episodic by age) are
// auto-applied, non-trivial ones enter the review_queue_item pipeline.
//
// Feature flag MCP_SEDIMENT_ENABLED does NOT gate this CLI — operators may
// want to preview transitions via --dry-run before enabling. The flag only
// gates retrieval-side layer weighting.
func runSedimentCycle(args []string) {
	fs := flag.NewFlagSet("sediment-cycle", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "Don't mutate; AutoApplied in result counts proposed transitions that WOULD be applied")
	sinceDays := fs.Int("since-days", 0, "Only consider memories OLDER than N days (0 = all). Useful for limiting cycle scope to stable memories.")
	limit := fs.Int("limit", 0, "Cap on transitions per run (0 = no limit)")
	verbose := fs.Bool("verbose", false, "Print each transition")
	jsonOut := fs.Bool("json", false, "Output JSON")
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

	result, err := store.RunSedimentCycle(context.Background(), memory.SedimentCycleConfig{
		DryRun:    *dryRun,
		SinceDays: *sinceDays,
		Limit:     *limit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(result)
		maybeExitErrors(result.Errors)
		return
	}

	fmt.Print(formatSedimentCycleResult(result, *verbose))
	maybeExitErrors(result.Errors)
}

// formatSedimentCycleResult renders a SedimentCycleResult for text output.
func formatSedimentCycleResult(r *memory.SedimentCycleResult, verbose bool) string {
	mode := "live"
	if r.DryRun {
		mode = "dry-run"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sediment cycle (%s):\n", mode)
	fmt.Fprintf(&b, "- Auto applied: %d\n", r.AutoApplied)
	fmt.Fprintf(&b, "- Review queued: %d\n", r.ReviewQueued)
	fmt.Fprintf(&b, "- Skipped: %d\n", r.Skipped)
	if verbose && len(r.Transitions) > 0 {
		b.WriteString("\nTransitions:\n")
		for _, tr := range r.Transitions {
			auto := "review"
			if tr.Auto {
				auto = "auto"
			}
			fmt.Fprintf(&b, "- %s: %s → %s (%s, %s)\n", tr.MemoryID, tr.From, tr.To, tr.Reason, auto)
		}
	}
	if len(r.Errors) > 0 {
		b.WriteString("\nErrors:\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	return b.String()
}

func maybeExitErrors(errs []string) {
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "sediment-cycle completed with %d partial failures; see 'errors' in result\n", len(errs))
		os.Exit(1)
	}
}
