package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// runIndexTriples is the retrofit CLI for T50 slice 3. It walks the memory
// store and asks the configured LLM extractor to fill (subj, rel, obj)
// triples for memories that don't have any yet. Idempotent by default
// (--resume): re-running the command after an interrupt keeps making
// progress without re-spending tokens on already-extracted memories.
//
// The CLI uses the same extractor configuration as the live ingest path
// (MCP_TRIPLE_EXTRACTOR_*), so any pricing knobs already in place — model
// choice, base URL, timeout — apply identically here.
func runIndexTriples(args []string) error {
	fs := flag.NewFlagSet("index-triples", flag.ContinueOnError)
	resume := fs.Bool("resume", true, "Skip memories that already have triples (default true)")
	force := fs.Bool("force", false, "Re-extract even when triples already exist (replace-all)")
	limit := fs.Int("limit", 0, "Process at most N memories (0 = unlimited)")
	memContext := fs.String("context", "", "Restrict to memories matching this context")
	dryRun := fs.Bool("dry-run", false, "Print what would be processed without calling the extractor")
	progressEvery := fs.Int("progress-every", 25, "Print progress every N memories processed")
	jsonOut := fs.Bool("json", false, "Emit a JSON summary at the end instead of human text")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	extractor, err := buildIndexTriplesExtractor(cfg)
	if err != nil && !*dryRun {
		fmt.Fprintln(os.Stderr, "hint: set MCP_TRIPLE_EXTRACTOR_ENABLED=true and the matching BASE_URL/API_KEY/MODEL envs, or run with --dry-run.")
		return fmt.Errorf("triple extractor not available: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memories, err := store.List(ctx, memory.Filters{Context: strings.TrimSpace(*memContext)}, 0)
	if err != nil {
		return fmt.Errorf("list memories: %w", err)
	}

	opts := indexTriplesLoopOptions{
		Resume:        *resume,
		Force:         *force,
		Limit:         *limit,
		DryRun:        *dryRun,
		ProgressEvery: *progressEvery,
		Verbose:       !*jsonOut,
	}
	stats := indexTriplesLoop(ctx, store, extractor, memories, opts)

	if *jsonOut {
		return printJSON(stats)
	}
	fmt.Printf("index-triples done in %s: total=%d processed=%d skipped=%d empty=%d errors=%d planned(dry-run)=%d triples=%d\n",
		stats.Elapsed, stats.Total, stats.Processed, stats.Skipped, stats.Empty, stats.Errors, stats.Planned, stats.TripleCount)
	return nil
}

// indexTriplesLoopOptions captures the runtime knobs the CLI loop honours.
// Extracted from runIndexTriples so the loop is testable without flag/file
// plumbing.
type indexTriplesLoopOptions struct {
	Resume        bool // skip memories with existing triples
	Force         bool // re-extract even when triples exist (overrides Resume)
	Limit         int  // 0 = unlimited
	DryRun        bool // count what would be extracted, do not call extractor
	ProgressEvery int  // 0 = silent
	Verbose       bool // emit per-memory dry-run lines and progress lines
}

// indexTriplesStore is the small subset of *memory.Store the loop needs.
// Defining it locally keeps the loop test-friendly and avoids importing the
// real store in unit tests of this CLI.
type indexTriplesStore interface {
	TriplesForMemory(ctx context.Context, memoryID string) ([]memory.Triple, error)
	DeleteTriplesForMemory(ctx context.Context, memoryID string) (int, error)
	AddTriples(ctx context.Context, triples []*memory.Triple) error
}

// indexTriplesLoop is the pure (no flag/IO setup) heart of runIndexTriples.
// It returns a stats summary describing what happened. Per-memory warnings
// are still written to stderr for human visibility, but the return value is
// the canonical machine-readable outcome.
func indexTriplesLoop(
	ctx context.Context,
	store indexTriplesStore,
	extractor memory.TripleExtractor,
	memories []*memory.Memory,
	opts indexTriplesLoopOptions,
) indexTriplesStats {
	stats := indexTriplesStats{Total: len(memories)}
	startedAt := time.Now()

	for i, mem := range memories {
		if opts.Limit > 0 && stats.Processed >= opts.Limit {
			break
		}

		existing, err := store.TriplesForMemory(ctx, mem.ID)
		if err != nil {
			stats.Errors++
			fmt.Fprintf(os.Stderr, "warn: triples lookup for %s failed: %v\n", mem.ID, err)
			continue
		}
		if len(existing) > 0 && opts.Resume && !opts.Force {
			stats.Skipped++
			continue
		}

		if opts.DryRun {
			stats.Planned++
			if opts.Verbose {
				fmt.Printf("would extract %s: %q\n", mem.ID, truncate(mem.Title, 60))
			}
			continue
		}

		triples, err := extractor.Extract(ctx, mem)
		if err != nil {
			stats.Errors++
			fmt.Fprintf(os.Stderr, "warn: extract %s failed: %v\n", mem.ID, err)
			continue
		}
		if len(triples) == 0 {
			stats.Empty++
			continue
		}

		// Replace-all keeps the retrofit idempotent under --force and
		// matches the live ingest path's semantics.
		if _, err := store.DeleteTriplesForMemory(ctx, mem.ID); err != nil {
			stats.Errors++
			fmt.Fprintf(os.Stderr, "warn: prior-triple cleanup for %s failed: %v\n", mem.ID, err)
			continue
		}
		if err := store.AddTriples(ctx, triples); err != nil {
			stats.Errors++
			fmt.Fprintf(os.Stderr, "warn: persist triples for %s failed: %v\n", mem.ID, err)
			continue
		}
		stats.Processed++
		stats.TripleCount += len(triples)

		if opts.ProgressEvery > 0 && stats.Processed%opts.ProgressEvery == 0 && opts.Verbose {
			fmt.Fprintf(os.Stderr, "progress: %d processed, %d skipped, %d errors (i=%d/%d, elapsed %s)\n",
				stats.Processed, stats.Skipped, stats.Errors, i+1, stats.Total, time.Since(startedAt).Round(time.Second))
		}
	}

	stats.Elapsed = time.Since(startedAt).Round(time.Millisecond).String()
	return stats
}

type indexTriplesStats struct {
	Total       int    `json:"total"`
	Processed   int    `json:"processed"`
	Skipped     int    `json:"skipped"` // had triples and --resume kept them
	Empty       int    `json:"empty"`   // extractor returned zero triples
	Errors      int    `json:"errors"`
	Planned     int    `json:"planned"` // dry-run only
	TripleCount int    `json:"triple_count"`
	Elapsed     string `json:"elapsed"`
}

// buildIndexTriplesExtractor mirrors the server-side wiring from
// internal/server/server.go so the CLI uses the same MCP_TRIPLE_EXTRACTOR_*
// envs as live ingest. Returns an error if extraction is disabled or
// misconfigured — callers in --dry-run mode can ignore the error.
func buildIndexTriplesExtractor(cfg config.Config) (memory.TripleExtractor, error) {
	if !cfg.TripleExtractorEnabled {
		return nil, fmt.Errorf("MCP_TRIPLE_EXTRACTOR_ENABLED is false")
	}
	apiKey := cfg.TripleExtractorAPIKey
	if apiKey == "" {
		apiKey = cfg.OpenAIAPIKey
	}
	baseURL := cfg.TripleExtractorBaseURL
	if baseURL == "" {
		baseURL = cfg.OpenAIBaseURL
	}
	return memory.NewOpenAIExtractor(memory.OpenAIExtractorConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   cfg.TripleExtractorModel,
		Timeout: cfg.TripleExtractorTimeout,
	}, zap.NewNop())
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
