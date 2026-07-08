package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// runDeadEndsStale lists dead_end memories older than a configurable
// threshold so the operator can decide which abandoned approaches deserve a
// second look (the original constraint may no longer apply after a library
// upgrade, infra change, or supersession). T46 hygiene slice.
func runDeadEndsStale(args []string) error {
	fs := flag.NewFlagSet("dead-ends-stale", flag.ContinueOnError)
	age := fs.Duration("age", 12*30*24*time.Hour, "Minimum age (default 12 months ≈ 8640h). Set to 0 to list all dead_ends with ages.")
	limit := fs.Int("limit", 50, "Maximum number of stale dead_ends to print (0 = unlimited)")
	jsonOut := fs.Bool("json", false, "Emit a JSON array instead of human-readable text")
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

	stale, err := store.StaleDeadEnds(context.Background(), *age)
	if err != nil {
		return err
	}
	if *limit > 0 && len(stale) > *limit {
		stale = stale[:*limit]
	}

	if *jsonOut {
		return printJSON(formatStaleDeadEndsJSON(stale))
	}
	if len(stale) == 0 {
		if *age > 0 {
			fmt.Printf("No dead_end memories older than %s.\n", *age)
		} else {
			fmt.Println("No dead_end memories found.")
		}
		return nil
	}
	threshold := "any age"
	if *age > 0 {
		threshold = fmt.Sprintf("≥ %s old", *age)
	}
	fmt.Printf("Stale dead_ends (%s, %d shown):\n\n", threshold, len(stale))
	for i, item := range stale {
		fmt.Printf("%d. [%s] age=%s\n", i+1, item.Memory.ID, formatAgeForHumans(item.Age))
		if title := item.Memory.Title; title != "" {
			fmt.Printf("   title: %s\n", title)
		}
		if ctx := item.Memory.Context; ctx != "" {
			fmt.Printf("   context: %s\n", ctx)
		}
		if !item.Memory.CreatedAt.IsZero() {
			fmt.Printf("   created: %s\n", item.Memory.CreatedAt.UTC().Format("2006-01-02"))
		}
		fmt.Println()
	}
	return nil
}

// formatStaleDeadEndsJSON flattens the result into a JSON-friendly shape
// so machine consumers do not need to reach into nested Memory structs.
func formatStaleDeadEndsJSON(stale []*memory.StaleDeadEnd) []map[string]any {
	out := make([]map[string]any, 0, len(stale))
	for _, item := range stale {
		row := map[string]any{
			"id":         item.Memory.ID,
			"title":      item.Memory.Title,
			"context":    item.Memory.Context,
			"age":        item.Age.String(),
			"age_days":   int(item.Age.Hours() / 24),
			"created_at": item.Memory.CreatedAt.UTC().Format(time.RFC3339),
		}
		out = append(out, row)
	}
	return out
}

// formatAgeForHumans turns a Duration into a compact "1y 3mo" / "8mo" /
// "21d" string. Plain `Duration.String()` is unreadable past a few hours.
func formatAgeForHumans(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours() / 24)
	if days < 1 {
		return d.Round(time.Hour).String()
	}
	years := days / 365
	rem := days - years*365
	months := rem / 30
	rem -= months * 30
	switch {
	case years > 0 && months > 0:
		return fmt.Sprintf("%dy %dmo", years, months)
	case years > 0:
		return fmt.Sprintf("%dy", years)
	case months > 0:
		return fmt.Sprintf("%dmo", months)
	default:
		return fmt.Sprintf("%dd", days)
	}
}
