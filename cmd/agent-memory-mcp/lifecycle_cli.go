package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/lifecycle"
)

// runSweepArchive handles the "sweep-archive" subcommand.
func runSweepArchive(args []string) {
	fs := flag.NewFlagSet("sweep-archive", flag.ExitOnError)
	rootsCSV := fs.String("roots", "", "Colon-separated archive roots (overrides MCP_TASK_ARCHIVE_ROOTS)")
	pattern := fs.String("slug-pattern", "", "Regex that slug basenames must match (overrides MCP_TASK_SLUG_PATTERN)")
	dryRun := fs.Bool("dry-run", false, "Show actions without applying")
	slug := fs.String("slug", "", "Optional: process only this slug (implies single-slug sweep)")
	jsonOut := fs.Bool("json", false, "Output JSON")
	threshold := fs.Float64("promotion-threshold", lifecycle.DefaultPromotionThreshold, "Importance threshold for promotion candidates")
	keepTag := fs.String("keep-tag", lifecycle.KeepAfterArchiveTag, "Tag that opts a memory out of sweep")
	mustParse(fs, args)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	sweepCfg, err := buildSweepConfig(cfg, *rootsCSV, *pattern, *threshold, *keepTag, *dryRun)
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

	sweeper := lifecycle.NewSweeper(store)
	ctx := context.Background()

	var result *lifecycle.SweepResult
	if trimmed := strings.TrimSpace(*slug); trimmed != "" {
		result, err = sweeper.EndTask(ctx, trimmed, sweepCfg)
	} else {
		result, err = sweeper.SweepArchive(ctx, sweepCfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(result)
		return
	}
	printSweepResult(result)
}

// runEndTask handles the "end-task" subcommand — explicit single-slug sweep.
func runEndTask(args []string) {
	fs := flag.NewFlagSet("end-task", flag.ExitOnError)
	slug := fs.String("slug", "", "Task slug to end (required)")
	rootsCSV := fs.String("roots", "", "Colon-separated archive roots (overrides MCP_TASK_ARCHIVE_ROOTS)")
	dryRun := fs.Bool("dry-run", false, "Show actions without applying")
	jsonOut := fs.Bool("json", false, "Output JSON")
	threshold := fs.Float64("promotion-threshold", lifecycle.DefaultPromotionThreshold, "Importance threshold for promotion candidates")
	keepTag := fs.String("keep-tag", lifecycle.KeepAfterArchiveTag, "Tag that opts a memory out of sweep")
	mustParse(fs, args)

	if strings.TrimSpace(*slug) == "" {
		fmt.Fprintln(os.Stderr, "error: -slug is required")
		fs.Usage()
		os.Exit(2)
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	sweepCfg, err := buildSweepConfig(cfg, *rootsCSV, "", *threshold, *keepTag, *dryRun)
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

	sweeper := lifecycle.NewSweeper(store)
	result, err := sweeper.EndTask(context.Background(), *slug, sweepCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(result)
		return
	}
	printSweepResult(result)
}

// buildSweepConfig merges CLI flags with the env-loaded config.
func buildSweepConfig(cfg config.Config, rootsCSV, pattern string, threshold float64, keepTag string, dryRun bool) (lifecycle.ArchiveSweepConfig, error) {
	sweepCfg := lifecycle.ArchiveSweepConfig{
		Roots:              cfg.TaskArchiveRoots,
		SlugPattern:        cfg.TaskSlugPattern,
		DryRun:             dryRun,
		PromotionThreshold: threshold,
		KeepTag:            keepTag,
	}
	if strings.TrimSpace(rootsCSV) != "" {
		sweepCfg.Roots = splitColonList(rootsCSV)
	}
	if strings.TrimSpace(pattern) != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return sweepCfg, fmt.Errorf("invalid -slug-pattern: %w", err)
		}
		sweepCfg.SlugPattern = re
	}
	return sweepCfg, nil
}

func splitColonList(raw string) []string {
	parts := strings.Split(raw, ":")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func printSweepResult(r *lifecycle.SweepResult) {
	mode := "live"
	if r.DryRun {
		mode = "dry-run"
	}
	if r.Slug != "" {
		fmt.Printf("end-task sweep (%s) for slug %q:\n", mode, r.Slug)
	} else {
		fmt.Printf("archive sweep (%s):\n", mode)
	}
	fmt.Printf("  outdated:             %d\n", r.TotalOutdated)
	fmt.Printf("  promotion candidates: %d\n", r.TotalPromotionCand)
	fmt.Printf("  skipped:              %d\n", r.TotalSkipped)
	if len(r.PerSlug) > 0 {
		fmt.Println("\nPer-slug breakdown:")
		for slug, stats := range r.PerSlug {
			fmt.Printf("  %s — outdated=%d promotion=%d skipped=%d\n",
				slug, stats.OutdatedCount, stats.PromotionCandidates, stats.Skipped)
		}
	}
}
