package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// runStore handles the "store" subcommand.
func runStore(args []string) {
	fs := flag.NewFlagSet("store", flag.ExitOnError)
	content := fs.String("content", "", "Memory content (required unless -stdin)")
	title := fs.String("title", "", "Memory title")
	memType := fs.String("type", "semantic", "Memory type: episodic, semantic, procedural, working")
	tags := fs.String("tags", "", "Comma-separated tags")
	ctx := fs.String("context", "", "Context (task slug, session, etc.)")
	importance := fs.Float64("importance", 0.5, "Importance weight (0.0-1.0)")
	stdin := fs.Bool("stdin", false, "Read content from stdin")
	fs.Parse(args)

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

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	m := &memory.Memory{
		Content:    text,
		Type:       memory.Type(*memType),
		Title:      *title,
		Context:    *ctx,
		Importance: *importance,
	}
	if *tags != "" {
		m.Tags = splitTags(*tags)
	}

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
	fs.Parse(args)

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: query is required (positional argument)")
		fs.Usage()
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

	filters := memory.Filters{
		Type: memory.Type(*memType),
	}
	if *tags != "" {
		filters.Tags = splitTags(*tags)
	}

	results, err := store.Recall(query, filters, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(results)
		return
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}
	for i, r := range results {
		printMemoryLine(i+1, r.Memory, r.Score)
	}
}

// runList handles the "list" subcommand.
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	memType := fs.String("type", "", "Filter by memory type")
	ctx := fs.String("context", "", "Filter by context")
	limit := fs.Int("limit", 50, "Max results")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

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

	filters := memory.Filters{
		Type:    memory.Type(*memType),
		Context: *ctx,
	}

	results, err := store.List(filters, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(results)
		return
	}

	if len(results) == 0 {
		fmt.Println("No memories found.")
		return
	}
	for i, m := range results {
		printMemoryLine(i+1, m, 0)
	}
}

// runDelete handles the "delete" subcommand.
func runDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	fs.Parse(args)

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

// runSearch handles the "search" subcommand (RAG).
func runSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	limit := fs.Int("limit", 10, "Max results")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "error: query is required (positional argument)")
		fs.Usage()
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

	resp, err := engine.Search(query, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(resp)
		return
	}

	fmt.Printf("Query: %s (%d results, %dms)\n\n", resp.Query, resp.TotalFound, resp.SearchTime)
	for i, r := range resp.Results {
		fmt.Printf("%d. [%.2f] %s\n", i+1, r.Score, r.Title)
		fmt.Printf("   Path: %s\n", r.Path)
		if r.Snippet != "" {
			fmt.Printf("   %s\n", r.Snippet)
		}
		fmt.Println()
	}
}

// runIndex handles the "index" subcommand (RAG reindex).
func runIndex(args []string) {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	fs.Parse(args)

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
	fs.Parse(args)

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

	stats := map[string]any{
		"total":   total,
		"by_type": byType,
	}

	if *jsonOut {
		printJSON(stats)
		return
	}

	fmt.Printf("Total memories: %d\n", total)
	if len(byType) > 0 {
		fmt.Println("By type:")
		for t, c := range byType {
			fmt.Printf("  %-12s %d\n", t, c)
		}
	}
}

// runExport handles the "export" subcommand.
func runExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	outFile := fs.String("o", "", "Output file (default: stdout)")
	fs.Parse(args)

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
		defer w.Close()
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
	fs.Parse(args)

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
		if err := store.Store(m); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to import memory %s: %v\n", m.ID, err)
			continue
		}
		imported++
	}

	fmt.Printf("Imported %d/%d memories\n", imported, len(memories))
}

// splitTags splits a comma-separated tag string into a slice.
func splitTags(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// printMemoryLine prints a single memory entry in human-readable format.
func printMemoryLine(n int, m *memory.Memory, score float64) {
	if score > 0 {
		fmt.Printf("%d. [%.2f] %s\n", n, score, memoryTitle(m))
	} else {
		fmt.Printf("%d. %s\n", n, memoryTitle(m))
	}
	fmt.Printf("   ID: %s  Type: %s  Importance: %.1f\n", m.ID, m.Type, m.Importance)
	if m.Context != "" {
		fmt.Printf("   Context: %s\n", m.Context)
	}
	if len(m.Tags) > 0 {
		fmt.Printf("   Tags: %s\n", strings.Join(m.Tags, ", "))
	}
	// Show truncated content
	content := m.Content
	if len(content) > 120 {
		content = content[:120] + "..."
	}
	content = strings.ReplaceAll(content, "\n", " ")
	fmt.Printf("   %s\n\n", content)
}

// memoryTitle returns a display title for a memory.
func memoryTitle(m *memory.Memory) string {
	if m.Title != "" {
		return m.Title
	}
	content := m.Content
	if len(content) > 60 {
		content = content[:60] + "..."
	}
	return strings.ReplaceAll(content, "\n", " ")
}
