package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/lifecycle"
)

// runSweepArchive handles the "sweep-archive" subcommand.
func runSweepArchive(args []string) error {
	fs := flag.NewFlagSet("sweep-archive", flag.ContinueOnError)
	rootsCSV := fs.String("roots", "", "Colon-separated archive roots (overrides MCP_TASK_ARCHIVE_ROOTS)")
	pattern := fs.String("slug-pattern", "", "Regex that slug basenames must match (overrides MCP_TASK_SLUG_PATTERN)")
	dryRun := fs.Bool("dry-run", false, "Show actions without applying")
	slug := fs.String("slug", "", "Optional: process only this slug (implies single-slug sweep)")
	jsonOut := fs.Bool("json", false, "Output JSON")
	threshold := fs.Float64("promotion-threshold", lifecycle.DefaultPromotionThreshold, "Importance threshold for promotion candidates")
	keepTag := fs.String("keep-tag", lifecycle.KeepAfterArchiveTag, "Tag that opts a memory out of sweep")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	sweepCfg, err := buildSweepConfig(cfg, *rootsCSV, *pattern, *threshold, *keepTag, *dryRun)
	if err != nil {
		return err
	}

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		return err
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
		return err
	}

	if *jsonOut {
		if err := printJSON(result); err != nil {
			return err
		}
	} else {
		fmt.Print(lifecycle.FormatSweepResult(result))
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("sweep completed with %d partial failures; see 'errors' in result", len(result.Errors))
	}
	return nil
}

// runEndTask handles the "end-task" subcommand — explicit single-slug sweep.
func runEndTask(args []string) error {
	fs := flag.NewFlagSet("end-task", flag.ContinueOnError)
	slug := fs.String("slug", "", "Task slug to end (required)")
	rootsCSV := fs.String("roots", "", "Colon-separated archive roots (overrides MCP_TASK_ARCHIVE_ROOTS)")
	dryRun := fs.Bool("dry-run", false, "Show actions without applying")
	jsonOut := fs.Bool("json", false, "Output JSON")
	threshold := fs.Float64("promotion-threshold", lifecycle.DefaultPromotionThreshold, "Importance threshold for promotion candidates")
	keepTag := fs.String("keep-tag", lifecycle.KeepAfterArchiveTag, "Tag that opts a memory out of sweep")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*slug) == "" {
		fs.Usage()
		return errors.New("-slug is required")
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	sweepCfg, err := buildSweepConfig(cfg, *rootsCSV, "", *threshold, *keepTag, *dryRun)
	if err != nil {
		return err
	}

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	sweeper := lifecycle.NewSweeper(store)
	result, err := sweeper.EndTask(context.Background(), *slug, sweepCfg)
	if err != nil {
		return err
	}

	if *jsonOut {
		if err := printJSON(result); err != nil {
			return err
		}
	} else {
		fmt.Print(lifecycle.FormatSweepResult(result))
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("end-task completed with %d partial failures; see 'errors' in result", len(result.Errors))
	}
	return nil
}

// buildSweepConfig merges CLI flags with the env-loaded config. The roots
// slice is defensively copied so downstream mutation cannot poison the
// long-lived config.TaskArchiveRoots slice.
func buildSweepConfig(cfg config.Config, rootsCSV, pattern string, threshold float64, keepTag string, dryRun bool) (lifecycle.ArchiveSweepConfig, error) {
	sweepCfg := lifecycle.ArchiveSweepConfig{
		Roots:              append([]string(nil), cfg.Lifecycle.TaskArchiveRoots...),
		SlugPattern:        cfg.Lifecycle.TaskSlugPattern,
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
