package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"

	_ "modernc.org/sqlite"
)

// contextInjectRow holds the minimal fields needed for context-inject output.
type contextInjectRow struct {
	ID      string
	Title   string
	Content string
	Context string
	Tags    string // JSON array
}

func (r contextInjectRow) displayTitle(maxRunes int) string {
	if t := strings.TrimSpace(r.Title); t != "" {
		return memory.TruncateRunes(t, maxRunes)
	}
	value := strings.TrimSpace(r.Content)
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	return memory.TruncateRunes(value, maxRunes)
}

func (r contextInjectRow) parseTags() []string {
	if r.Tags == "" || r.Tags == "null" {
		return nil
	}
	var tags []string
	_ = json.Unmarshal([]byte(r.Tags), &tags)
	return tags
}

// runContextInject outputs recent memories, pending raw summaries, and compilation
// instructions for session start injection.
//
// Lightweight path: opens SQLite directly, skips embedder/cache/background workers.
func runContextInject(args []string) {
	fs := flag.NewFlagSet("context-inject", flag.ExitOnError)
	limit := fs.Int("limit", 10, "Max recent knowledge items to include")
	pendingLimit := fs.Int("pending-limit", 5, "Max pending raw summaries to include for compilation")
	memContext := fs.String("context", "", "Filter by context")
	service := fs.String("service", "", "Filter by service")
	mustParse(fs, args)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", cfg.MemoryDBPath+"?_journal_mode=WAL&mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	hasOutput := false

	// Output recent knowledge items.
	if *limit > 0 {
		knowledge, err := queryKnowledge(ctx, db, *limit, *memContext, *service)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error querying knowledge: %v\n", err)
			os.Exit(1)
		}
		if len(knowledge) > 0 {
			fmt.Println("# Session Context (from agent-memory-mcp)")
			fmt.Println()
			for _, r := range knowledge {
				fmt.Printf("## %s\n", r.displayTitle(80))
				if r.Context != "" {
					fmt.Printf("Context: %s\n", r.Context)
				}
				if tags := r.parseTags(); len(tags) > 0 {
					fmt.Printf("Tags: %s\n", strings.Join(tags, ", "))
				}
				content := strings.TrimSpace(r.Content)
				if len(content) > 500 {
					content = content[:500] + "..."
				}
				fmt.Println(content)
				fmt.Println()
			}
			hasOutput = true
		}
	}

	// Output pending raw summaries with compilation instructions.
	if *pendingLimit > 0 {
		pending, err := queryPending(ctx, db, *pendingLimit, *memContext)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error querying pending: %v\n", err)
			os.Exit(1)
		}
		if len(pending) > 0 {
			if hasOutput {
				fmt.Println("---")
				fmt.Println()
			}
			fmt.Printf("# Pending session summaries (%d uncompiled)\n\n", len(pending))
			fmt.Println("Review the raw session summaries below. For each one, extract reusable knowledge")
			fmt.Println("using the MCP memory tools (store_decision, store_memory, etc.).")
			fmt.Println("After processing a summary, call update_memory to add the tag \"compiled\" to it.")
			fmt.Println()
			for _, r := range pending {
				fmt.Printf("## [pending] %s (id: %s)\n", r.displayTitle(80), r.ID)
				if r.Context != "" {
					fmt.Printf("Context: %s\n", r.Context)
				}
				content := strings.TrimSpace(r.Content)
				if len(content) > 1000 {
					content = content[:1000] + "..."
				}
				fmt.Println(content)
				fmt.Println()
			}
		}
	}
}

// queryKnowledge returns recent knowledge items (excluding session/checkpoint/review records).
func queryKnowledge(ctx context.Context, db *sql.DB, limit int, memContext, service string) ([]contextInjectRow, error) {
	var conditions []string
	var params []any

	// Exclude internal record kinds.
	conditions = append(conditions, `(
		json_extract(metadata, '$.record_kind') IS NULL
		OR json_extract(metadata, '$.record_kind') NOT IN ('session_summary', 'session_checkpoint', 'review_queue_item')
	)`)

	if memContext != "" {
		conditions = append(conditions, "context = ?")
		params = append(params, memContext)
	}
	if service != "" {
		conditions = append(conditions, "tags LIKE ?")
		params = append(params, `%"service:`+service+`"%`)
	}

	query := "SELECT id, COALESCE(title,''), content, COALESCE(context,''), COALESCE(tags,'') FROM memories"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	params = append(params, limit)

	return queryRows(ctx, db, query, params)
}

// queryPending returns uncompiled session summary records.
func queryPending(ctx context.Context, db *sql.DB, limit int, memContext string) ([]contextInjectRow, error) {
	var conditions []string
	var params []any

	conditions = append(conditions, "json_extract(metadata, '$.record_kind') = 'session_summary'")
	conditions = append(conditions, `tags NOT LIKE '%"compiled"%'`)

	if memContext != "" {
		conditions = append(conditions, "context = ?")
		params = append(params, memContext)
	}

	query := "SELECT id, COALESCE(title,''), content, COALESCE(context,''), COALESCE(tags,'') FROM memories WHERE " +
		strings.Join(conditions, " AND ") +
		" ORDER BY created_at DESC LIMIT ?"
	params = append(params, limit)

	return queryRows(ctx, db, query, params)
}

func queryRows(ctx context.Context, db *sql.DB, query string, params []any) ([]contextInjectRow, error) {
	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []contextInjectRow
	for rows.Next() {
		var r contextInjectRow
		if err := rows.Scan(&r.ID, &r.Title, &r.Content, &r.Context, &r.Tags); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func hasTag(tags []string, target string) bool {
	for _, t := range tags {
		if t == target {
			return true
		}
	}
	return false
}

// runAutoCapture reads session transcript from stdin and runs the full extract/plan/apply pipeline.
func runAutoCapture(args []string) {
	fs := flag.NewFlagSet("auto-capture", flag.ExitOnError)
	stdin := fs.Bool("stdin", false, "Read transcript from stdin")
	summary := fs.String("summary", "", "Session summary text")
	mode := fs.String("mode", "", "Session mode: coding, incident, migration, research, cleanup")
	memContext := fs.String("context", "", "Project or task context")
	service := fs.String("service", "", "Service or component name")
	tags := fs.String("tags", "", "Comma-separated tags")
	dryRun := fs.Bool("dry-run", false, "Show what would be captured without saving")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	mustParse(fs, args)

	summaryText, err := readSessionSummary(*summary, *stdin, fs.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	modeValue, err := parseSessionModeValue(*mode)
	if err != nil {
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

	svc := sessionclose.New(store)
	sessionSummary := memory.SessionSummary{
		Mode:    modeValue,
		Context: strings.TrimSpace(*memContext),
		Service: strings.TrimSpace(*service),
		Summary: summaryText,
		Tags:    parseCSVTags(*tags),
		Metadata: map[string]string{
			memory.MetadataSessionOrigin: "hook_auto_capture",
		},
	}

	result, err := svc.Analyze(context.Background(), sessionclose.AnalyzeRequest{
		Summary:          sessionSummary,
		DryRun:           *dryRun,
		SaveRaw:          !*dryRun,
		AutoApplyLowRisk: !*dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(result)
		return
	}
	fmt.Println(sessionclose.FormatAnalysis(result))
}

// runCheckpoint saves a raw session checkpoint (used by PreCompact hook).
func runCheckpoint(args []string) {
	fs := flag.NewFlagSet("checkpoint", flag.ExitOnError)
	stdin := fs.Bool("stdin", false, "Read content from stdin")
	summary := fs.String("summary", "", "Checkpoint summary text")
	boundary := fs.String("boundary", "checkpoint", "Boundary type: checkpoint or pre_compact")
	memContext := fs.String("context", "", "Project or task context")
	service := fs.String("service", "", "Service or component name")
	tags := fs.String("tags", "", "Comma-separated tags")
	mustParse(fs, args)

	summaryText, err := readSessionSummary(*summary, *stdin, fs.Args())
	if err != nil {
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

	svc := sessionclose.New(store)
	sessionSummary := memory.SessionSummary{
		Context: strings.TrimSpace(*memContext),
		Service: strings.TrimSpace(*service),
		Summary: summaryText,
		Tags:    parseCSVTags(*tags),
	}

	boundaryValue := strings.TrimSpace(*boundary)
	if boundaryValue == "" {
		boundaryValue = "checkpoint"
	}

	extraTags := []string{"session-checkpoint"}
	if boundaryValue != "checkpoint" {
		extraTags = append(extraTags, boundaryValue)
	}

	rawID, err := svc.SaveRawSummaryWithOptions(context.Background(), sessionSummary, sessionclose.RawSaveOptions{
		RecordKind: memory.RecordKindSessionCheckpoint,
		ExtraTags:  extraTags,
		Metadata: map[string]string{
			memory.MetadataSessionBoundary: boundaryValue,
			memory.MetadataSessionOrigin:   "hook_checkpoint",
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Checkpoint saved as memory %s (boundary: %s)\n", rawID, boundaryValue)
}
