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
)

func runResolveReviewItem(args []string) {
	fs := flag.NewFlagSet("resolve-review-item", flag.ExitOnError)
	resolution := fs.String("resolution", "resolved", "Resolution: resolved, dismissed, deferred")
	note := fs.String("note", "", "Optional resolution note")
	owner := fs.String("owner", "", "Optional owner or reviewer")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	mustParse(fs, args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "error: review item id is required")
		fs.Usage()
		os.Exit(1)
	}

	resolutionValue, err := normalizeReviewResolutionValue(*resolution)
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

	itemID := strings.TrimSpace(fs.Arg(0))
	mem, err := store.Get(itemID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if !memory.IsReviewQueueMemory(mem) {
		fmt.Fprintf(os.Stderr, "error: memory %s is not a review queue item\n", itemID)
		os.Exit(1)
	}

	metadata := map[string]string{
		memory.MetadataReviewRequired: "false",
		memory.MetadataStatus:         resolutionValue,
		"review_resolved_at":          time.Now().UTC().Format(time.RFC3339),
	}
	if trimmed := strings.TrimSpace(*note); trimmed != "" {
		metadata["review_resolution_note"] = trimmed
	}
	if trimmed := strings.TrimSpace(*owner); trimmed != "" {
		metadata["review_resolved_by"] = trimmed
	}

	if err := store.Update(context.Background(), itemID, memory.Update{
		Tags:     resolvedReviewQueueTags(mem.Tags, resolutionValue),
		Metadata: metadata,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		mustPrintJSON(map[string]any{
			"id":         itemID,
			"resolution": resolutionValue,
			"resolved":   true,
		})
		return
	}

	fmt.Printf("Resolved review item %s as %s\n", itemID, resolutionValue)
}

func normalizeReviewResolutionValue(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "resolved", nil
	}
	switch value {
	case "resolved", "dismissed", "deferred":
		return value, nil
	default:
		return "", fmt.Errorf("resolution must be resolved, dismissed, or deferred")
	}
}

func resolvedReviewQueueTags(tags []string, resolution string) []string {
	filtered := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		switch {
		case tag == "":
			continue
		case tag == "review:required":
			continue
		case strings.HasPrefix(tag, "review:"):
			continue
		case tag == "status:review_required":
			continue
		case tag == "status:resolved" || tag == "status:dismissed" || tag == "status:deferred":
			continue
		default:
			filtered = append(filtered, tag)
		}
	}
	filtered = append(filtered, "review:"+resolution, "status:"+resolution)
	return memory.NormalizeTags(filtered)
}
