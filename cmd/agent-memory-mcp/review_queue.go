package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/review"
)

func runResolveReviewItem(args []string) error {
	fs := flag.NewFlagSet("resolve-review-item", flag.ContinueOnError)
	resolution := fs.String("resolution", "resolved", "Resolution: resolved, dismissed, deferred")
	note := fs.String("note", "", "Optional resolution note")
	owner := fs.String("owner", "", "Optional owner or reviewer")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("review item id is required")
	}

	resolutionValue, err := review.NormalizeResolution(*resolution)
	if err != nil {
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

	itemID := strings.TrimSpace(fs.Arg(0))
	mem, err := store.Get(itemID)
	if err != nil {
		return err
	}
	if !memory.IsReviewQueueMemory(mem) {
		return fmt.Errorf("memory %s is not a review queue item", itemID)
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
		Tags:     review.ResolvedTags(mem.Tags, resolutionValue),
		Metadata: metadata,
	}); err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(map[string]any{
			"id":         itemID,
			"resolution": resolutionValue,
			"resolved":   true,
		})
	}

	fmt.Printf("Resolved review item %s as %s\n", itemID, resolutionValue)
	return nil
}
