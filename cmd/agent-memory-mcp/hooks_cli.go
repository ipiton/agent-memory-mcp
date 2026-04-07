package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
)

// runContextInject outputs recent memories, pending raw summaries, and compilation
// instructions for session start injection. The agent reads this output and
// compiles pending summaries into structured knowledge using MCP tools.
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

	store, cleanup, err := initMemoryStore(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	filters := memory.Filters{
		Context: strings.TrimSpace(*memContext),
	}
	if svc := strings.TrimSpace(*service); svc != "" {
		filters.Tags = []string{"service:" + svc}
	}

	allMemories, err := store.List(context.Background(), filters, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Split into knowledge items and pending raw summaries.
	var knowledge []*memory.Memory
	var pending []*memory.Memory
	for _, m := range allMemories {
		if memory.IsSessionSummaryMemory(m) && !hasTag(m.Tags, "compiled") {
			pending = append(pending, m)
		} else if !memory.IsSessionSummaryMemory(m) && !memory.IsSessionCheckpointMemory(m) && !memory.IsReviewQueueMemory(m) {
			knowledge = append(knowledge, m)
		}
	}

	hasOutput := false

	// Output recent knowledge.
	if len(knowledge) > 0 {
		if len(knowledge) > *limit {
			knowledge = knowledge[:*limit]
		}
		fmt.Println("# Session Context (from agent-memory-mcp)")
		fmt.Println()
		for _, m := range knowledge {
			title := memory.DisplayTitle(m, 80)
			fmt.Printf("## %s\n", title)
			if m.Context != "" {
				fmt.Printf("Context: %s\n", m.Context)
			}
			if len(m.Tags) > 0 {
				fmt.Printf("Tags: %s\n", strings.Join(m.Tags, ", "))
			}
			content := strings.TrimSpace(m.Content)
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			fmt.Println(content)
			fmt.Println()
		}
		hasOutput = true
	}

	// Output pending raw summaries with compilation instructions.
	if len(pending) > 0 {
		if len(pending) > *pendingLimit {
			pending = pending[:*pendingLimit]
		}
		if hasOutput {
			fmt.Println("---")
			fmt.Println()
		}
		fmt.Printf("# Pending session summaries (%d uncompiled)\n\n", len(pending))
		fmt.Println("Review the raw session summaries below. For each one, extract reusable knowledge")
		fmt.Println("using the MCP memory tools (store_decision, store_memory, etc.).")
		fmt.Println("After processing a summary, call update_memory to add the tag \"compiled\" to it.")
		fmt.Println()
		for _, m := range pending {
			title := memory.DisplayTitle(m, 80)
			fmt.Printf("## [pending] %s (id: %s)\n", title, m.ID)
			if m.Context != "" {
				fmt.Printf("Context: %s\n", m.Context)
			}
			content := strings.TrimSpace(m.Content)
			if len(content) > 1000 {
				content = content[:1000] + "..."
			}
			fmt.Println(content)
			fmt.Println()
		}
	}
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
