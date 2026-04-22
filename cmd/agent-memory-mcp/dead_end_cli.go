package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

// runMarkDeadEnd handles the "mark-dead-end" subcommand. It persists a
// first-class dead_end engineering memory so future retrieval can surface
// it when an agent is about to repeat a known pitfall.
func runMarkDeadEnd(args []string) {
	fs := flag.NewFlagSet("mark-dead-end", flag.ExitOnError)
	attempted := fs.String("attempted", "", "Short description of the failed attempt (required)")
	whyFailed := fs.String("why-failed", "", "Root cause of the failure (required)")
	alternative := fs.String("alternative", "", "Alternative approach that actually worked")
	slug := fs.String("slug", "", "Related task slug (T-xxx)")
	service := fs.String("service", "", "Service or component name")
	memContext := fs.String("context", "", "Memory context (task slug, project, session)")
	title := fs.String("title", "", "Short title for the dead end (defaults to truncated attempted)")
	tagsCSV := fs.String("tags", "", "Comma-separated extra tags")
	jsonOut := fs.Bool("json", false, "Output JSON")
	mustParse(fs, args)

	if strings.TrimSpace(*attempted) == "" {
		fmt.Fprintln(os.Stderr, "error: -attempted is required")
		fs.Usage()
		os.Exit(2)
	}
	if strings.TrimSpace(*whyFailed) == "" {
		fmt.Fprintln(os.Stderr, "error: -why-failed is required")
		fs.Usage()
		os.Exit(2)
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

	content := joinLines(
		labeledLine("Attempted approach", *attempted),
		labeledLine("Why failed", *whyFailed),
		labeledLine("Alternative used", *alternative),
		labeledLine("Related task", *slug),
		labeledLine("Service", *service),
	)
	if err := userio.ValidateMemoryContent(content); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	extraMeta := map[string]string{}
	if s := strings.TrimSpace(*slug); s != "" {
		extraMeta["related_task_slug"] = s
	}
	if a := strings.TrimSpace(*alternative); a != "" {
		extraMeta["alternative_used"] = a
	}

	extraTags := parseCSVTags(*tagsCSV)

	resolvedTitle := strings.TrimSpace(*title)
	if resolvedTitle == "" {
		resolvedTitle = strings.TrimSpace(*attempted)
		if len(resolvedTitle) > 80 {
			resolvedTitle = resolvedTitle[:80] + "..."
		}
	}

	mem := &memory.Memory{
		Title:      resolvedTitle,
		Content:    content,
		Type:       memory.DefaultStorageTypeForEngineeringType(memory.EngineeringTypeDeadEnd),
		Context:    strings.TrimSpace(*memContext),
		Importance: 0.80,
		Tags:       memory.BuildEngineeringTags(memory.EngineeringTypeDeadEnd, *service, "", "", false, extraTags),
		Metadata:   memory.BuildEngineeringMetadata(memory.EngineeringTypeDeadEnd, *service, "", "", false, extraMeta),
	}

	if err := store.Store(context.Background(), mem); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(map[string]any{
			"id":    mem.ID,
			"title": mem.Title,
			"type":  string(mem.Type),
			"tags":  mem.Tags,
		})
		return
	}
	fmt.Printf("Dead end stored:\n- ID: %s\n- Title: %s\n- Type: %s\n- Tags: %v\n",
		mem.ID, mem.Title, mem.Type, mem.Tags)
}

func labeledLine(label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return fmt.Sprintf("%s: %s", label, value)
}

func joinLines(lines ...string) string {
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}
