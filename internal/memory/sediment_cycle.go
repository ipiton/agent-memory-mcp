package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// SedimentCycleConfig configures a single sediment-cycle sweep.
type SedimentCycleConfig struct {
	// Policy governs the thresholds used by Decide. Zero-value fields inherit
	// defaults via SedimentPolicy.DefaultsApplied().
	Policy SedimentPolicy

	// DryRun reports actions without writing. No PromoteSediment calls, no
	// review-queue items created.
	DryRun bool

	// SinceDays optionally restricts the cycle to memories OLDER than N
	// days (CreatedAt <= now - N*24h). Zero/negative = scan all. Useful
	// for limiting cycle scope to stable memories — the age-based transition
	// rules (surface≥7d, episodic≥30d, character-decay≥90d) all require
	// older memories, so filtering for newer memories would silently yield
	// zero transitions.
	SinceDays int

	// Limit caps the number of transitions considered. 0 = no cap.
	Limit int
}

// SedimentCycleResult is the outcome of a SedimentCycle run.
type SedimentCycleResult struct {
	AutoApplied  int                   `json:"auto_applied"`
	ReviewQueued int                   `json:"review_queued"`
	Skipped      int                   `json:"skipped"`
	Errors       []string              `json:"errors,omitempty"`
	DryRun       bool                  `json:"dry_run"`
	Transitions  []SedimentTransition  `json:"transitions,omitempty"`
}

// RunSedimentCycle scans all memories, computes Decide transitions, and
// applies them: trivial ones directly via PromoteSediment, non-trivial ones
// as review_queue_item memories for human review.
//
// Idempotent: review-queue items are dedup'd by (target_memory_id, review_source),
// so a second run produces zero new queue items for already-proposed
// transitions. Auto-applied transitions also become no-ops on the second
// pass because Decide sees the new current layer and returns nil (or a
// different rule).
//
// The cycle does NOT claim cfg.SedimentEnabled gates it — callers may want
// to run in dry-run mode even when the feature flag is off to preview what
// transitions would occur. If the server wants to hard-gate at startup,
// check cfg.SedimentEnabled at the call site.
func (ms *Store) RunSedimentCycle(ctx context.Context, cfg SedimentCycleConfig) (*SedimentCycleResult, error) {
	policy := cfg.Policy.DefaultsApplied()
	now := policy.Now()

	// SinceDays filters for memories OLDER than N days. We post-filter
	// after List() rather than pushing into Filters{Since: ...} because
	// Filters.Since selects memories NEWER than the threshold — the
	// opposite semantic. Age-based transition rules only fire on older
	// memories, so pre-filtering via Filters.Since would silently drop
	// the exact candidates we need to consider.
	memories, err := ms.List(ctx, Filters{}, 0)
	if err != nil {
		return nil, fmt.Errorf("sediment-cycle: list memories: %w", err)
	}

	var sinceCutoff time.Time
	if cfg.SinceDays > 0 {
		sinceCutoff = now.Add(-time.Duration(cfg.SinceDays) * 24 * time.Hour)
	}

	result := &SedimentCycleResult{DryRun: cfg.DryRun}

	processed := 0
	for _, m := range memories {
		if m == nil {
			continue
		}
		if !sinceCutoff.IsZero() && m.CreatedAt.After(sinceCutoff) {
			// Memory is younger than cutoff — skip.
			continue
		}
		if cfg.Limit > 0 && processed >= cfg.Limit {
			break
		}
		tr := Decide(m, policy)
		if tr == nil {
			result.Skipped++
			continue
		}
		processed++
		result.Transitions = append(result.Transitions, *tr)

		if tr.Auto {
			if cfg.DryRun {
				result.AutoApplied++
				continue
			}
			if _, err := ms.PromoteSediment(ctx, tr.MemoryID, tr.To); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: promote %s→%s: %v", tr.MemoryID, tr.From, tr.To, err))
				ms.logger.Warn("sediment-cycle: auto-promote failed",
					zap.String("id", tr.MemoryID),
					zap.String("from", string(tr.From)),
					zap.String("to", string(tr.To)),
					zap.Error(err),
				)
				continue
			}
			result.AutoApplied++
			continue
		}

		// Non-auto: route to review queue.
		if cfg.DryRun {
			result.ReviewQueued++
			continue
		}
		queued, err := ms.createSedimentReviewItem(ctx, m, tr)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: review queue %s→%s: %v", tr.MemoryID, tr.From, tr.To, err))
			ms.logger.Warn("sediment-cycle: review queue write failed",
				zap.String("id", tr.MemoryID),
				zap.String("to", string(tr.To)),
				zap.Error(err),
			)
			continue
		}
		if queued {
			result.ReviewQueued++
		}
	}

	ms.logger.Info("sediment-cycle completed",
		zap.Int("auto_applied", result.AutoApplied),
		zap.Int("review_queued", result.ReviewQueued),
		zap.Int("skipped", result.Skipped),
		zap.Int("errors", len(result.Errors)),
		zap.Bool("dry_run", cfg.DryRun),
	)

	return result, nil
}

// createSedimentReviewItem writes a review_queue_item memory targeted at the
// given memory+transition. Returns (queued=false, nil) if a matching review
// item already exists (idempotency).
func (ms *Store) createSedimentReviewItem(ctx context.Context, target *Memory, tr *SedimentTransition) (bool, error) {
	exists, err := ms.sedimentReviewItemExists(ctx, target.Context, target.ID)
	if err != nil {
		return false, fmt.Errorf("dedup check: %w", err)
	}
	if exists {
		return false, nil
	}

	content := fmt.Sprintf(
		"Sediment promotion candidate: memory %s.\n"+
			"Transition: %s → %s (reason: %s).\n"+
			"Suggested action: promote_sediment.",
		target.ID, tr.From, tr.To, tr.Reason,
	)

	// Use rune-safe truncation; byte slicing breaks multi-byte codepoints
	// (e.g. Cyrillic chars are 2 bytes each) and produces invalid UTF-8.
	displayTitleStr := strings.TrimSpace(target.Title)
	if displayTitleStr == "" {
		displayTitleStr = target.ID
	}
	displayTitleStr = TruncateRunes(displayTitleStr, 80)

	reviewMem := &Memory{
		Title:      fmt.Sprintf("Review: %s→%s for %s", tr.From, tr.To, displayTitleStr),
		Content:    content,
		Type:       TypeWorking,
		Context:    target.Context,
		Importance: 0.5,
		Tags: []string{
			"review-queue",
			"sediment-cycle",
			"review:required",
			"sediment-target:" + string(tr.To),
		},
		Metadata: map[string]string{
			MetadataRecordKind:           RecordKindReviewQueueItem,
			MetadataReviewRequired:       "true",
			MetadataReviewReason:         "sediment_cycle_" + tr.Reason,
			MetadataReviewSource:         ReviewSourceSedimentCycle,
			MetadataReviewTargetMemoryID: target.ID,
			MetadataReviewTargetLayer:    string(tr.To),
		},
	}
	if err := ms.Store(ctx, reviewMem); err != nil {
		return false, err
	}
	return true, nil
}

// sedimentReviewItemExists reports whether a review_queue_item from an
// earlier cycle already targets the given memory ID. Scoped to the target's
// Context to avoid a full-store scan.
func (ms *Store) sedimentReviewItemExists(ctx context.Context, memContext, targetID string) (bool, error) {
	items, err := ms.List(ctx, Filters{Context: memContext, Type: TypeWorking}, 0)
	if err != nil {
		return false, err
	}
	for _, m := range items {
		if m == nil || !IsReviewQueueMemory(m) {
			continue
		}
		if m.Metadata[MetadataReviewTargetMemoryID] == targetID &&
			m.Metadata[MetadataReviewSource] == ReviewSourceSedimentCycle {
			// Skip already-resolved items so a re-run can propose again if
			// the previous review was dismissed.
			if strings.EqualFold(strings.TrimSpace(m.Metadata[MetadataReviewRequired]), "false") {
				continue
			}
			return true, nil
		}
	}
	return false, nil
}
