package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func mustParse(fs *flag.FlagSet, args []string) {
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
}

func mustPrintJSON(v any) {
	if err := printJSON(v); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runStore handles the "store" subcommand.
func runStore(args []string) {
	fs := flag.NewFlagSet("store", flag.ExitOnError)
	content := fs.String("content", "", "Memory content (required unless -stdin)")
	title := fs.String("title", "", "Memory title")
	memType := fs.String("type", "semantic", "Memory type: episodic, semantic, procedural, working")
	tags := fs.String("tags", "", "Comma-separated tags")
	ctx := fs.String("context", "", "Context (task slug, session, etc.)")
	var importance optionalFloat64
	fs.Var(&importance, "importance", "Importance weight (0.0-1.0)")
	stdin := fs.Bool("stdin", false, "Read content from stdin")
	mustParse(fs, args)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	text := *content
	if *stdin {
		data, err := readStdin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
			os.Exit(1)
		}
		text = strings.TrimSpace(string(data))
	}
	if text == "" {
		fmt.Fprintln(os.Stderr, "error: -content or -stdin is required")
		fs.Usage()
		os.Exit(1)
	}
	text = strings.TrimSpace(text)
	if err := userio.ValidateMemoryContent(text); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	parsedType, err := userio.ParseMemoryType(*memType, memory.TypeSemantic, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	importanceRaw := math.NaN()
	if importance.set {
		importanceRaw = importance.value
	}
	importanceValue, err := userio.NormalizeImportance(importanceRaw, memory.DefaultImportance)
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

	m := &memory.Memory{
		Content:    text,
		Type:       parsedType,
		Title:      strings.TrimSpace(*title),
		Context:    strings.TrimSpace(*ctx),
		Importance: importanceValue,
	}
	m.Tags = userio.ParseCSVTags(*tags)

	if err := store.Store(m); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Stored memory %s\n", m.ID)
}

// runRecall handles the "recall" subcommand.
func runRecall(args []string) {
	fs := flag.NewFlagSet("recall", flag.ExitOnError)
	memType := fs.String("type", "", "Filter by memory type")
	limit := fs.Int("limit", 10, "Max results")
	tags := fs.String("tags", "", "Filter by tags (comma-separated, match any)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	mustParse(fs, args)

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: query is required (positional argument)")
		fs.Usage()
		os.Exit(1)
	}
	query = strings.TrimSpace(query)
	if err := userio.ValidateQuery(query); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

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

	parsedType, err := userio.ParseMemoryType(*memType, "", true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	filters := memory.Filters{Type: parsedType}
	filters.Tags = userio.ParseCSVTags(*tags)

	results, err := store.Recall(query, filters, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(results)
		return
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}
	for i, r := range results {
		printMemoryLine(i+1, r.Memory, r.Score, r.Trust)
	}
}

// runList handles the "list" subcommand.
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	memType := fs.String("type", "", "Filter by memory type")
	ctx := fs.String("context", "", "Filter by context")
	limit := fs.Int("limit", 50, "Max results")
	jsonOut := fs.Bool("json", false, "Output as JSON")
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

	parsedType, err := userio.ParseMemoryType(*memType, "", true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	filters := memory.Filters{
		Type:    parsedType,
		Context: strings.TrimSpace(*ctx),
	}

	results, err := store.List(filters, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(results)
		return
	}

	if len(results) == 0 {
		fmt.Println("No memories found.")
		return
	}
	for i, m := range results {
		printMemoryLine(i+1, m, 0, nil)
	}
}

// runDelete handles the "delete" subcommand.
func runDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	mustParse(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "error: memory ID is required (positional argument)")
		os.Exit(1)
	}
	id := fs.Arg(0)

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

	if err := store.Delete(id); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Deleted memory %s\n", id)
}

// runSearch handles the "search" subcommand (hybrid RAG retrieval).
func runSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	limit := fs.Int("limit", 10, "Max results")
	sourceType := fs.String("source-type", "", "Filter by source type: docs, adr, rfc, changelog, runbook, postmortem, ci_config, helm, terraform, k8s")
	debug := fs.Bool("debug", false, "Show score breakdown, applied filters, and ranking boosts")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	mustParse(fs, args)

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: query is required (positional argument)")
		fs.Usage()
		os.Exit(1)
	}
	query = strings.TrimSpace(query)
	if err := userio.ValidateQuery(query); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	engine, err := initRAGEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Stop()

	resp, err := engine.Search(query, *limit, *sourceType, *debug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(resp)
		return
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
			fmt.Printf("   Trust: %s\n", userio.FormatDocumentTrust(r.Trust))
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
}

// runIndex handles the "index" subcommand (RAG reindex).
func runIndex(args []string) {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	mustParse(fs, args)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	engine, err := initRAGEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer engine.Stop()

	fmt.Println("Indexing documents...")
	start := time.Now()
	if err := engine.IndexDocuments(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Indexing completed in %v\n", time.Since(start).Round(time.Millisecond))
}

// runStats handles the "stats" subcommand.
func runStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "Output as JSON")
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

	total := store.Count()
	byType := store.CountByType()
	byEmbeddingModel := store.CountByEmbeddingModel()

	stats := map[string]any{
		"total":              total,
		"by_type":            byType,
		"by_embedding_model": byEmbeddingModel,
	}

	if *jsonOut {
		mustPrintJSON(stats)
		return
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
}

// runReembed handles the "reembed" subcommand.
func runReembed(args []string) {
	fs := flag.NewFlagSet("reembed", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "Output as JSON")
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

	result, err := store.ReembedAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(result)
		return
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
}

// runExport handles the "export" subcommand.
func runExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	outFile := fs.String("o", "", "Output file (default: stdout)")
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

	memories, err := store.ExportAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var w *os.File
	if *outFile != "" {
		w, err = os.Create(*outFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = w.Close() }()
	} else {
		w = os.Stdout
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(memories); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *outFile != "" {
		fmt.Fprintf(os.Stderr, "Exported %d memories to %s\n", len(memories), *outFile)
	}
}

// runImport handles the "import" subcommand.
func runImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	mustParse(fs, args)

	var data []byte
	var err error

	if fs.NArg() > 0 {
		data, err = os.ReadFile(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading file: %v\n", err)
			os.Exit(1)
		}
	} else {
		data, err = readStdin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
			os.Exit(1)
		}
	}

	var memories []*memory.Memory
	if err := json.Unmarshal(data, &memories); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing JSON: %v\n", err)
		os.Exit(1)
	}

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

	imported := 0
	for _, m := range memories {
		// Clear embedding to regenerate with current embedder
		m.Embedding = nil
		m.EmbeddingModel = ""
		if err := store.Store(m); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to import memory %s: %v\n", m.ID, err)
			continue
		}
		imported++
	}

	fmt.Printf("Imported %d/%d memories\n", imported, len(memories))
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
		fmt.Printf("   Trust: %s\n", userio.FormatMemoryTrust(tm))
	}
	// Show truncated content
	content := m.Content
	if len(content) > 120 {
		content = content[:120] + "..."
	}
	content = strings.ReplaceAll(content, "\n", " ")
	fmt.Printf("   %s\n\n", content)
}
