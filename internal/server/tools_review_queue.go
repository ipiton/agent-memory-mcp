package server

// resolve_review_item / resolve_review_queue and their target resolution.
// T91: split out of tools_workflow.go; moved verbatim.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/review"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func (s *MCPServer) callResolveReviewItem(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	id, rsErr := requiredString(args, "id")
	if rsErr != nil {
		return nil, rsErr
	}

	resolution, err := review.NormalizeResolution(mustString(args, "resolution"))
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	note := strings.TrimSpace(mustString(args, "note"))
	owner := strings.TrimSpace(mustString(args, "owner"))

	resolved, err := resolveReviewItemInStore(s.memoryStore, strings.TrimSpace(id), resolution, note, owner, time.Now().UTC())
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to resolve review item", Data: err.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	switch format {
	case "text":
		text := fmt.Sprintf("Review item resolved:\n- ID: %s\n- Resolution: %s", resolved["id"], resolved["resolution"])
		if owner != "" {
			text += fmt.Sprintf("\n- Owner: %s", owner)
		}
		if note != "" {
			text += fmt.Sprintf("\n- Note: %s", note)
		}
		return toolResultText(text), nil
	default:
		return toolResultJSON(resolved), nil
	}
}

func (s *MCPServer) callResolveReviewQueue(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	resolution, err := review.NormalizeResolution(mustString(args, "resolution"))
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	note := strings.TrimSpace(mustString(args, "note"))
	owner := strings.TrimSpace(mustString(args, "owner"))
	dryRun, _ := getBool(args, "dry_run")
	limit := boundedLimit(args, 20, 100)

	createdBefore, tErr := parseOptionalRFC3339(args, "created_before")
	if tErr != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: tErr.Error()}
	}
	kind := strings.TrimSpace(mustString(args, "kind"))

	ids, err := resolveReviewQueueTargetIDs(s.memoryStore, getStringSlice(args, "ids"), memory.ProjectBankOptions{
		Filters: memory.Filters{
			Context: strings.TrimSpace(mustString(args, "context")),
		},
		Service: strings.TrimSpace(mustString(args, "service")),
		Tags:    userio.NormalizeTags(getStringSlice(args, "tags")),
		Limit:   limit,
	}, createdBefore, kind)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to select review queue items", Data: err.Error()}
	}

	result := map[string]any{
		"resolution": resolution,
		"count":      len(ids),
		"ids":        ids,
		"dry_run":    dryRun,
	}
	if note != "" {
		result["note"] = note
	}
	if owner != "" {
		result["owner"] = owner
	}

	if !dryRun {
		resolvedItems := make([]map[string]any, 0, len(ids))
		now := time.Now().UTC()
		for _, id := range ids {
			resolved, err := resolveReviewItemInStore(s.memoryStore, id, resolution, note, owner, now)
			if err != nil {
				return nil, &rpcError{Code: rpcErrServerError, Message: "failed to resolve review queue", Data: err.Error()}
			}
			resolvedItems = append(resolvedItems, resolved)
		}
		result["resolved_items"] = resolvedItems
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	switch format {
	case "text":
		if len(ids) == 0 {
			return toolResultText("Review queue resolution matched no pending items."), nil
		}
		if dryRun {
			return toolResultText(fmt.Sprintf("Review queue dry-run:\n- Count: %d\n- Resolution: %s\n- IDs: %s", len(ids), resolution, strings.Join(ids, ", "))), nil
		}
		return toolResultText(fmt.Sprintf("Review queue resolved:\n- Count: %d\n- Resolution: %s\n- IDs: %s", len(ids), resolution, strings.Join(ids, ", "))), nil
	default:
		return toolResultJSON(result), nil
	}
}

// resolveReviewQueueTargetIDs resolves the review-queue items to act on. Explicit
// ids win. Otherwise it selects from the review-queue view, optionally narrowing
// by record kind and by creation date (T81: `created_before` lets bulk cleanup
// of a monthly backlog run through the tool instead of hand-writing SQL).
//
// T110: the two groups of filters used to run against different populations.
// `context`/`service`/`tags` are applied inside ProjectBankView, which then
// truncates each section to Limit; `kind` and `created_before` were applied to
// the truncated page. With 885 pending items and a limit of 100, the date cutoff
// was filtering the 100 most recent — so `tags` alone matched 100, the cutoff
// alone matched 5, and together they matched 0. A date cutoff that behaves that
// way is useless as the safety rail on a bulk operation, which is the one job it
// has. The view is now read unbounded, every filter runs over the same set, and
// Limit truncates last — so it means "act on at most N matching items".
func resolveReviewQueueTargetIDs(store *memory.Store, ids []string, options memory.ProjectBankOptions, createdBefore time.Time, kind string) ([]string, error) {
	normalizedIDs := normalizeIDs(ids)
	if len(normalizedIDs) > 0 {
		return normalizedIDs, nil
	}

	limit := options.Limit
	viewOptions := options
	viewOptions.Limit = unlimitedProjectBankLimit

	view, err := store.ProjectBankView(context.Background(), memory.ProjectBankViewReviewQueue, viewOptions)
	if err != nil {
		return nil, err
	}

	kind = strings.TrimSpace(kind)
	targets := make([]string, 0)
	for _, section := range view.Sections {
		for _, item := range section.Items {
			if item == nil || strings.TrimSpace(item.ID) == "" {
				continue
			}
			if kind != "" && !strings.EqualFold(strings.TrimSpace(item.RecordKind), kind) {
				continue
			}
			if !createdBefore.IsZero() {
				// The view item's UpdatedAt is not a reliable creation time, so
				// read the memory's actual CreatedAt (cheap: served from cache).
				mem, err := store.Get(item.ID)
				if err != nil || !mem.CreatedAt.Before(createdBefore) {
					continue
				}
			}
			targets = append(targets, item.ID)
		}
	}
	targets = normalizeIDs(targets)
	if limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	return targets, nil
}

// unlimitedProjectBankLimit asks ProjectBankView for every matching item. The
// options type has no "no limit" value — zero is normalized to 10 — and the
// view is built from the in-memory cache, so a large bound costs a slice
// capacity rather than a query.
const unlimitedProjectBankLimit = 1 << 30

func normalizeIDs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func resolveReviewItemInStore(store *memory.Store, id string, resolution string, note string, owner string, resolvedAt time.Time) (map[string]any, error) {
	item, err := store.Get(strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if !memory.IsReviewQueueMemory(item) {
		return nil, fmt.Errorf("memory is not a review queue item")
	}

	// T84: re-tag the item resolved rather than deleting it. Deleting broke three
	// invariants — the archive-sweep re-proposal guard keys off an EXISTING
	// review item for the target (delete → dismissed candidates get re-suggested
	// every sweep), the resolution audit trail (note/owner/resolved-at) would be
	// lost, and a retried resolve would hard-error on the missing id. The kNN
	// pollution these caused is already fixed independently: Recall excludes
	// review_queue_item (read.go) and they are no longer embedded (write.go), so
	// a persisted-but-inert resolved pointer costs only a row.
	metadata := map[string]string{
		memory.MetadataReviewRequired: "false",
		memory.MetadataStatus:         resolution,
		"review_resolved_at":          resolvedAt.UTC().Format(time.RFC3339),
	}
	if note != "" {
		metadata["review_resolution_note"] = note
	}
	if owner != "" {
		metadata["review_resolved_by"] = owner
	}

	if err := store.Update(context.Background(), item.ID, memory.Update{
		Tags:     review.ResolvedTags(item.Tags, resolution),
		Metadata: metadata,
	}); err != nil {
		return nil, err
	}

	result := map[string]any{
		"id":         item.ID,
		"resolution": resolution,
		"resolved":   true,
	}
	if note != "" {
		result["note"] = note
	}
	if owner != "" {
		result["owner"] = owner
	}
	return result, nil
}
