package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

type commandCtx struct {
	cfg     config.Config
	store   *memory.Store
	cleanup func()
	fs      *flag.FlagSet
}

func newCommandCtx(name string, args []string) *commandCtx {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	ctx := &commandCtx{fs: fs}
	// We don't parse yet because handlers might add more flags
	return ctx
}

func (c *commandCtx) initStore() error {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	c.cfg = cfg

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		return err
	}
	c.store = store
	c.cleanup = cleanup
	return nil
}

func (c *commandCtx) initRAG() (*rag.Engine, error) {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	c.cfg = cfg

	engine, err := initRAGEngine(cfg)
	if err != nil {
		return nil, err
	}
	return engine, nil
}

func (c *commandCtx) parse(args []string) error {
	return c.fs.Parse(args)
}

func (c *commandCtx) close() {
	if c.cleanup != nil {
		c.cleanup()
	}
}

// runStore handles the "store" subcommand.
func runStore(args []string) error {
	ctx := newCommandCtx("store", args)
	content := ctx.fs.String("content", "", "Memory content (required unless -stdin)")
	title := ctx.fs.String("title", "", "Memory title")
	memType := ctx.fs.String("type", "semantic", "Memory type: episodic, semantic, procedural, working")
	tags := ctx.fs.String("tags", "", "Comma-separated tags")
	memContext := ctx.fs.String("context", "", "Context (task slug, session, etc.)")
	var importance optionalFloat64
	ctx.fs.Var(&importance, "importance", "Importance weight (0.0-1.0)")
	stdin := ctx.fs.Bool("stdin", false, "Read content from stdin")
	if err := ctx.parse(args); err != nil {
		return err
	}

	text := *content
	if *stdin {
		data, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		text = strings.TrimSpace(string(data))
	}
	if text == "" {
		ctx.fs.Usage()
		return errors.New("-content or -stdin is required")
	}
	text = strings.TrimSpace(text)
	if err := userio.ValidateMemoryContent(text); err != nil {
		return err
	}

	parsedType, err := userio.ParseMemoryType(*memType, memory.TypeSemantic, false)
	if err != nil {
		return err
	}
	importanceRaw := math.NaN()
	if importance.set {
		importanceRaw = importance.value
	}
	importanceValue, err := userio.NormalizeImportance(importanceRaw, memory.DefaultImportance)
	if err != nil {
		return err
	}

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	m := &memory.Memory{
		Content:    text,
		Type:       parsedType,
		Title:      strings.TrimSpace(*title),
		Context:    strings.TrimSpace(*memContext),
		Importance: importanceValue,
	}
	m.Tags = userio.ParseCSVTags(*tags)

	if err := ctx.store.Store(context.Background(), m); err != nil {
		return err
	}

	fmt.Printf("Stored memory %s\n", m.ID)
	return nil
}

// runRecall handles the "recall" subcommand.
func runRecall(args []string) error {
	ctx := newCommandCtx("recall", args)
	memType := ctx.fs.String("type", "", "Filter by memory type")
	limit := ctx.fs.Int("limit", 10, "Max results")
	tags := ctx.fs.String("tags", "", "Filter by tags (comma-separated, match any)")
	jsonOut := ctx.fs.Bool("json", false, "Output as JSON")
	if err := ctx.parse(args); err != nil {
		return err
	}

	query := strings.TrimSpace(strings.Join(ctx.fs.Args(), " "))
	if query == "" {
		ctx.fs.Usage()
		return errors.New("query is required (positional argument)")
	}
	if err := userio.ValidateQuery(query); err != nil {
		return err
	}

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	parsedType, err := userio.ParseMemoryType(*memType, "", true)
	if err != nil {
		return err
	}
	filters := memory.Filters{Type: parsedType}
	filters.Tags = userio.ParseCSVTags(*tags)

	results, err := ctx.store.Recall(context.Background(), query, filters, *limit)
	if err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(results)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return nil
	}
	for i, r := range results {
		printMemoryLine(i+1, r.Memory, r.Score, r.Trust)
	}
	return nil
}

// runList handles the "list" subcommand.
func runList(args []string) error {
	ctx := newCommandCtx("list", args)
	memType := ctx.fs.String("type", "", "Filter by memory type")
	memCtx := ctx.fs.String("context", "", "Filter by context")
	limit := ctx.fs.Int("limit", 50, "Max results")
	jsonOut := ctx.fs.Bool("json", false, "Output as JSON")
	if err := ctx.parse(args); err != nil {
		return err
	}

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	parsedType, err := userio.ParseMemoryType(*memType, "", true)
	if err != nil {
		return err
	}

	filters := memory.Filters{
		Type:    parsedType,
		Context: strings.TrimSpace(*memCtx),
	}

	results, err := ctx.store.List(context.Background(), filters, *limit)
	if err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(results)
	}

	if len(results) == 0 {
		fmt.Println("No memories found.")
		return nil
	}
	for i, m := range results {
		printMemoryLine(i+1, m, 0, nil)
	}
	return nil
}

// runDelete handles the "delete" subcommand.
func runDelete(args []string) error {
	ctx := newCommandCtx("delete", args)
	if err := ctx.parse(args); err != nil {
		return err
	}

	if ctx.fs.NArg() < 1 {
		return errors.New("memory ID is required (positional argument)")
	}
	id := ctx.fs.Arg(0)

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	if err := ctx.store.Delete(context.Background(), id); err != nil {
		return err
	}
	fmt.Printf("Deleted memory %s\n", id)
	return nil
}

// runSearch handles the "search" subcommand (hybrid RAG retrieval).
func runSearch(args []string) error {
	ctx := newCommandCtx("search", args)
	limit := ctx.fs.Int("limit", 10, "Max results")
	sourceType := ctx.fs.String("source-type", "", "Filter by source type: docs, adr, rfc, changelog, runbook, postmortem, ci_config, helm, terraform, k8s")
	debug := ctx.fs.Bool("debug", false, "Show score breakdown, applied filters, and ranking boosts")
	jsonOut := ctx.fs.Bool("json", false, "Output as JSON")
	if err := ctx.parse(args); err != nil {
		return err
	}

	query := strings.TrimSpace(strings.Join(ctx.fs.Args(), " "))
	if query == "" {
		ctx.fs.Usage()
		return errors.New("query is required (positional argument)")
	}
	if err := userio.ValidateQuery(query); err != nil {
		return err
	}

	engine, err := ctx.initRAG()
	if err != nil {
		return err
	}
	defer engine.Stop()

	resp, err := engine.Search(context.Background(), query, *limit, *sourceType, *debug)
	if err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(resp)
	}

	fmt.Printf("Query: %s (%d results, %dms)\n\n", resp.Query, resp.TotalFound, resp.SearchTime)
	if resp.Debug != nil {
		if len(resp.Debug.AppliedFilters) > 0 {
			fmt.Printf("Applied filters: %s\n", strings.Join(resp.Debug.AppliedFilters, ", "))
		} else {
			fmt.Println("Applied filters: none")
		}
		fmt.Printf("Ranking signals: %s\n", strings.Join(resp.Debug.RankingSignals, ", "))
		fmt.Printf("Indexed chunks: %d | Filtered out: %d | Discarded as noise: %d | Candidates: %d | Returned: %d\n\n",
			resp.Debug.IndexedChunks,
			resp.Debug.FilteredOut,
			resp.Debug.DiscardedAsNoise,
			resp.Debug.CandidateCount,
			resp.Debug.ReturnedCount,
		)
	}
	for i, r := range resp.Results {
		fmt.Printf("%d. [%.2f] %s\n", i+1, r.Score, r.Title)
		fmt.Printf("   Path: %s\n", r.Path)
		if r.SourceType != "" {
			fmt.Printf("   Source type: %s\n", r.SourceType)
		}
		if r.Trust != nil {
			fmt.Printf("   Trust: %s\n", userio.FormatTrust(r.Trust))
		}
		if r.Debug != nil {
			fmt.Printf("   Score breakdown: semantic=%.3f keyword_raw=%.3f keyword_norm=%.3f recency=%.3f source=%.3f confidence=%.3f final=%.3f\n",
				r.Debug.Breakdown.Semantic,
				r.Debug.Breakdown.KeywordRaw,
				r.Debug.Breakdown.KeywordNormalized,
				r.Debug.Breakdown.RecencyBoost,
				r.Debug.Breakdown.SourceBoost,
				r.Debug.Breakdown.ConfidenceBoost,
				r.Debug.Breakdown.FinalScore,
			)
			if len(r.Debug.AppliedBoosts) > 0 {
				fmt.Printf("   Applied boosts: %s\n", strings.Join(r.Debug.AppliedBoosts, ", "))
			}
		}
		if r.Snippet != "" {
			fmt.Printf("   %s\n", r.Snippet)
		}
		fmt.Println()
	}
	return nil
}

// runIndex handles the "index" subcommand (RAG reindex).
func runIndex(args []string) error {
	ctx := newCommandCtx("index", args)
	if err := ctx.parse(args); err != nil {
		return err
	}

	engine, err := ctx.initRAG()
	if err != nil {
		return err
	}
	defer engine.Stop()

	fmt.Println("Indexing documents...")
	start := time.Now()
	if err := engine.IndexDocuments(context.Background()); err != nil {
		return err
	}
	fmt.Printf("Indexing completed in %v\n", time.Since(start).Round(time.Millisecond))
	return nil
}

// runStats handles the "stats" subcommand.
func runStats(args []string) error {
	ctx := newCommandCtx("stats", args)
	jsonOut := ctx.fs.Bool("json", false, "Output as JSON")
	if err := ctx.parse(args); err != nil {
		return err
	}

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	total := ctx.store.Count()
	byType := ctx.store.CountByType()
	byEmbeddingModel := ctx.store.CountByEmbeddingModel()

	stats := map[string]any{
		"total":              total,
		"by_type":            byType,
		"by_embedding_model": byEmbeddingModel,
	}

	if *jsonOut {
		return printJSON(stats)
	}

	fmt.Printf("Total memories: %d\n", total)
	if len(byType) > 0 {
		fmt.Println("By type:")
		for t, c := range byType {
			fmt.Printf("  %-12s %d\n", t, c)
		}
	}
	if len(byEmbeddingModel) > 0 {
		fmt.Println("By embedding model:")
		for modelID, c := range byEmbeddingModel {
			fmt.Printf("  %-40s %d\n", modelID, c)
		}
	}
	return nil
}

// runReembed handles the "reembed" subcommand.
func runReembed(args []string) error {
	ctx := newCommandCtx("reembed", args)
	jsonOut := ctx.fs.Bool("json", false, "Output as JSON")
	if err := ctx.parse(args); err != nil {
		return err
	}

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	result, err := ctx.store.ReembedAll(context.Background())
	if err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(result)
	}

	fmt.Printf("Re-embed completed for %d memories\n", result.Total)
	if result.CurrentModel != "" {
		fmt.Printf("Current model: %s\n", result.CurrentModel)
	}
	fmt.Printf("Re-embedded: %d\n", result.Reembedded)
	fmt.Printf("Already current: %d\n", result.AlreadyCurrent)
	fmt.Printf("Failed: %d\n", result.Failed)
	if len(result.ChangedFromByModel) > 0 {
		fmt.Println("Changed from:")
		for modelID, count := range result.ChangedFromByModel {
			fmt.Printf("  %-40s %d\n", modelID, count)
		}
	}
	return nil
}

// runExport handles the "export" subcommand.
func runExport(args []string) error {
	ctx := newCommandCtx("export", args)
	outFile := ctx.fs.String("o", "", "Output file (default: stdout)")
	if err := ctx.parse(args); err != nil {
		return err
	}

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	memories, err := ctx.store.ExportAll(context.Background())
	if err != nil {
		return err
	}

	var w *os.File
	if *outFile != "" {
		w, err = os.Create(*outFile)
		if err != nil {
			return err
		}
		defer func() { _ = w.Close() }()
	} else {
		w = os.Stdout
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(memories); err != nil {
		return err
	}

	if *outFile != "" {
		fmt.Fprintf(os.Stderr, "Exported %d memories to %s\n", len(memories), *outFile)
	}
	return nil
}

// runImport handles the "import" subcommand.
func runImport(args []string) error {
	ctx := newCommandCtx("import", args)
	if err := ctx.parse(args); err != nil {
		return err
	}

	var data []byte
	var err error

	if ctx.fs.NArg() > 0 {
		data, err = os.ReadFile(ctx.fs.Arg(0))
		if err != nil {
			return fmt.Errorf("reading file: %w", err)
		}
	} else {
		data, err = readStdin()
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
	}

	var memories []*memory.Memory
	if err := json.Unmarshal(data, &memories); err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	if err := ctx.initStore(); err != nil {
		return err
	}
	defer ctx.close()

	imported := 0
	for _, m := range memories {
		// Clear embedding to regenerate with current embedder
		m.Embedding = nil
		m.EmbeddingModel = ""
		if err := ctx.store.Store(context.Background(), m); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to import memory %s: %v\n", m.ID, err)
			continue
		}
		imported++
	}

	fmt.Printf("Imported %d/%d memories\n", imported, len(memories))
	return nil
}

// printMemoryLine prints a single memory entry in human-readable format.
func printMemoryLine(n int, m *memory.Memory, score float64, tm *trust.Metadata) {
	if score > 0 {
		fmt.Printf("%d. [%.2f] %s\n", n, score, memory.DisplayTitle(m, 60))
	} else {
		fmt.Printf("%d. %s\n", n, memory.DisplayTitle(m, 60))
	}
	fmt.Printf("   ID: %s  Type: %s  Importance: %.1f\n", m.ID, m.Type, m.Importance)
	if m.Context != "" {
		fmt.Printf("   Context: %s\n", m.Context)
	}
	if len(m.Tags) > 0 {
		fmt.Printf("   Tags: %s\n", strings.Join(m.Tags, ", "))
	}
	if tm != nil {
		fmt.Printf("   Trust: %s\n", userio.FormatTrust(tm))
	}
	// Show truncated content
	content := m.Content
	if len(content) > 120 {
		content = content[:120] + "..."
	}
	content = strings.ReplaceAll(content, "\n", " ")
	fmt.Printf("   %s\n\n", content)
}
