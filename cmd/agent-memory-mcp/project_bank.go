package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func runProjectBank(args []string) {
	fs := flag.NewFlagSet("project-bank", flag.ExitOnError)
	view := fs.String("view", "canonical_overview", "View: canonical_overview, decisions, runbooks, incidents, caveats, migrations, review_queue")
	ctx := fs.String("context", "", "Filter by context")
	service := fs.String("service", "", "Filter by service")
	status := fs.String("status", "", "Filter by lifecycle or status")
	owner := fs.String("owner", "", "Filter by owner")
	tags := fs.String("tags", "", "Filter by tags (comma-separated, match all)")
	limit := fs.Int("limit", 10, "Max items per section")
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

	parsedView, err := memory.ValidateProjectBankView(*view)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	result, err := store.ProjectBankView(context.Background(), parsedView, memory.ProjectBankOptions{
		Filters: memory.Filters{
			Context: strings.TrimSpace(*ctx),
		},
		Service: strings.TrimSpace(*service),
		Status:  strings.TrimSpace(*status),
		Owner:   strings.TrimSpace(*owner),
		Tags:    userio.ParseCSVTags(*tags),
		Limit:   *limit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(result)
		return
	}

	fmt.Print(formatProjectBankCLIView(result))
}

func formatProjectBankCLIView(result *memory.ProjectBankViewResult) string {
	if result == nil {
		return "Project bank view unavailable.\n"
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Project bank view: %s\n", result.Title)
	if result.Context != "" {
		fmt.Fprintf(&buf, "Context: %s\n", result.Context)
	}
	if result.Service != "" {
		fmt.Fprintf(&buf, "Service: %s\n", result.Service)
	}
	if result.Status != "" {
		fmt.Fprintf(&buf, "Status filter: %s\n", result.Status)
	}
	if result.Owner != "" {
		fmt.Fprintf(&buf, "Owner filter: %s\n", result.Owner)
	}
	if len(result.Tags) > 0 {
		fmt.Fprintf(&buf, "Tags filter: %s\n", strings.Join(result.Tags, ", "))
	}
	buf.WriteString("\n")

	if result.TotalCount > 0 {
		fmt.Fprintf(&buf, "Visible items: %d\n", result.TotalCount)
	}
	if len(result.EntityCounts) > 0 {
		buf.WriteString("Entity counts:\n")
		for _, key := range []string{"decisions", "runbooks", "incidents", "caveats", "migrations"} {
			if count := result.EntityCounts[key]; count > 0 {
				fmt.Fprintf(&buf, "  %-12s %d\n", key, count)
			}
		}
		buf.WriteString("\n")
	}

	for idx, section := range result.Sections {
		if idx > 0 {
			buf.WriteString("\n")
		}
		fmt.Fprintf(&buf, "%s (%d):\n", section.Title, len(section.Items))
		if section.Description != "" {
			fmt.Fprintf(&buf, "%s\n", section.Description)
		}
		if len(section.Items) == 0 {
			buf.WriteString("No items found.\n")
			continue
		}
		for i, item := range section.Items {
			fmt.Fprintf(&buf, "%d. %s\n", i+1, item.Title)
			if item.Entity != "" {
				fmt.Fprintf(&buf, "   Entity: %s\n", item.Entity)
			}
			if item.Service != "" {
				fmt.Fprintf(&buf, "   Service: %s\n", item.Service)
			}
			if item.Lifecycle != "" {
				fmt.Fprintf(&buf, "   Lifecycle: %s\n", item.Lifecycle)
			}
			if item.KnowledgeLayer != "" {
				fmt.Fprintf(&buf, "   Layer: %s\n", item.KnowledgeLayer)
			}
			if item.Owner != "" {
				fmt.Fprintf(&buf, "   Owner: %s\n", item.Owner)
			}
			if item.SessionMode != "" {
				fmt.Fprintf(&buf, "   Session mode: %s\n", item.SessionMode)
			}
			if item.ReviewRequired {
				buf.WriteString("   Review: required\n")
			}
			if item.Trust != nil {
				fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(item.Trust))
			}
			if !item.LastVerifiedAt.IsZero() {
				fmt.Fprintf(&buf, "   Last verified: %s\n", item.LastVerifiedAt.UTC().Format(time.RFC3339))
			}
			summary := strings.TrimSpace(item.Summary)
			if len(summary) > 220 {
				summary = summary[:220] + "..."
			}
			if summary != "" {
				fmt.Fprintf(&buf, "   %s\n", summary)
			}
		}
	}

	return strings.TrimSpace(buf.String()) + "\n"
}
