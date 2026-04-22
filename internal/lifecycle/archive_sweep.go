// Package lifecycle provides task-lifecycle consolidation primitives.
//
// T47 introduces a pull-mode "archive sweep": for each configured archive root,
// enumerate subdirectories (task slugs) and mark all working memories tied to
// those slugs as outdated. High-importance or procedural memories are instead
// recorded as promotion candidates (review-queue items) so a human can decide.
//
// The sweep is idempotent: re-running it produces zero new actions (already-
// outdated memories are skipped; duplicate review-queue items are not
// re-created).
//
// Push-mode EndTask(slug) is the explicit one-off path used by the `end-task`
// CLI/MCP tool — it validates the slug is under at least one configured
// archive root before doing anything.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

// KeepAfterArchiveTag is the default tag a memory can carry to opt out of the
// sweep. Users can override it via ArchiveSweepConfig.KeepTag.
const KeepAfterArchiveTag = "keep-after-archive"

// DefaultPromotionThreshold is the importance at/above which a working memory
// becomes a promotion candidate instead of being marked outdated.
const DefaultPromotionThreshold = 0.7

// ErrNoRoots is returned by SweepArchive when ArchiveSweepConfig.Roots is empty.
// Defense-in-depth: we never want a misconfigured call to sweep "everything".
var ErrNoRoots = errors.New("archive sweep: no roots configured (MCP_TASK_ARCHIVE_ROOTS empty)")

// ArchiveSweepConfig configures a single sweep invocation.
type ArchiveSweepConfig struct {
	// Roots is the list of absolute directory paths to enumerate for task slugs.
	// Each immediate subdirectory name is treated as a slug (optionally filtered
	// by SlugPattern). Empty → ErrNoRoots.
	Roots []string

	// SlugPattern optionally restricts which slug basenames are swept. nil means
	// accept all.
	SlugPattern *regexp.Regexp

	// DryRun reports actions without writing. No MarkOutdated calls, no
	// review-queue items created.
	DryRun bool

	// PromotionThreshold is the importance at/above which a memory becomes a
	// promotion candidate instead of being marked outdated. Zero → use
	// DefaultPromotionThreshold.
	PromotionThreshold float64

	// KeepTag is the tag that opts a memory out of the sweep. Empty → use
	// KeepAfterArchiveTag.
	KeepTag string
}

// ArchiveAction is a single decision made during a sweep.
type ArchiveAction struct {
	MemoryID string `json:"memory_id"`
	Slug     string `json:"slug"`
	Action   string `json:"action"` // "outdated" | "promotion_candidate" | "skipped_keep_tag" | "already_outdated" | "skipped_non_working"
	Reason   string `json:"reason,omitempty"`
}

// SlugStats aggregates per-slug counters.
type SlugStats struct {
	OutdatedCount       int `json:"outdated_count"`
	PromotionCandidates int `json:"promotion_candidates"`
	Skipped             int `json:"skipped"`
}

// SweepResult is the outcome of SweepArchive or EndTask.
type SweepResult struct {
	Slug               string                `json:"slug,omitempty"` // empty for multi-slug sweep
	PerSlug            map[string]*SlugStats `json:"per_slug,omitempty"`
	TotalOutdated      int                   `json:"total_outdated"`
	TotalPromotionCand int                   `json:"total_promotion_candidates"`
	TotalSkipped       int                   `json:"total_skipped"`
	Actions            []ArchiveAction       `json:"actions,omitempty"`
	DryRun             bool                  `json:"dry_run"`
}

// Sweeper orchestrates archive-sweep runs against a memory.Store.
//
// Safe for concurrent use by multiple goroutines (all state is in the Store;
// Sweeper itself is stateless after construction).
type Sweeper struct {
	store     *memory.Store
	logger    *zap.Logger
	now       func() time.Time
	statFS    func(path string) (os.FileInfo, error) // injectable for tests
	readDirFS func(path string) ([]os.DirEntry, error)
}

// Option customizes Sweeper construction.
type Option func(*Sweeper)

// WithLogger sets a custom logger. Default: zap.NewNop().
func WithLogger(l *zap.Logger) Option {
	return func(sw *Sweeper) {
		if l != nil {
			sw.logger = l
		}
	}
}

// WithClock injects a time source. Default: time.Now.
func WithClock(now func() time.Time) Option {
	return func(sw *Sweeper) {
		if now != nil {
			sw.now = now
		}
	}
}

// WithFS injects stat and readdir functions for testing. Both must be non-nil
// for the override to take effect.
func WithFS(stat func(string) (os.FileInfo, error), readDir func(string) ([]os.DirEntry, error)) Option {
	return func(sw *Sweeper) {
		if stat != nil && readDir != nil {
			sw.statFS = stat
			sw.readDirFS = readDir
		}
	}
}

// NewSweeper constructs a Sweeper against the given memory.Store.
func NewSweeper(store *memory.Store, opts ...Option) *Sweeper {
	sw := &Sweeper{
		store:     store,
		logger:    zap.NewNop(),
		now:       time.Now,
		statFS:    os.Stat,
		readDirFS: os.ReadDir,
	}
	for _, opt := range opts {
		opt(sw)
	}
	return sw
}

// SweepArchive enumerates all slugs under cfg.Roots and processes each one.
// Returns a merged SweepResult aggregating all slugs.
func (sw *Sweeper) SweepArchive(ctx context.Context, cfg ArchiveSweepConfig) (*SweepResult, error) {
	if sw == nil || sw.store == nil {
		return nil, errors.New("archive sweep: sweeper not initialized")
	}
	if len(cfg.Roots) == 0 {
		return nil, ErrNoRoots
	}
	cfg = applyDefaults(cfg)

	result := &SweepResult{
		PerSlug: make(map[string]*SlugStats),
		DryRun:  cfg.DryRun,
	}

	for _, root := range cfg.Roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		info, err := sw.statFS(root)
		if err != nil {
			sw.logger.Warn("archive-sweep: root not accessible, skipping",
				zap.String("root", root), zap.Error(err))
			continue
		}
		if !info.IsDir() {
			sw.logger.Warn("archive-sweep: root is not a directory, skipping",
				zap.String("root", root))
			continue
		}

		entries, err := sw.readDirFS(root)
		if err != nil {
			sw.logger.Warn("archive-sweep: failed to read root, skipping",
				zap.String("root", root), zap.Error(err))
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			slug := entry.Name()
			if cfg.SlugPattern != nil && !cfg.SlugPattern.MatchString(slug) {
				continue
			}
			if err := sw.sweepSlug(ctx, slug, cfg, result); err != nil {
				return nil, fmt.Errorf("archive-sweep: slug %q: %w", slug, err)
			}
		}
	}

	return result, nil
}

// EndTask sweeps exactly one slug after validating it lives under one of the
// configured archive roots. Returns an error if the slug is absent from every
// root (defense-in-depth).
func (sw *Sweeper) EndTask(ctx context.Context, slug string, cfg ArchiveSweepConfig) (*SweepResult, error) {
	if sw == nil || sw.store == nil {
		return nil, errors.New("archive sweep: sweeper not initialized")
	}
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, errors.New("end-task: slug is required")
	}
	if len(cfg.Roots) == 0 {
		return nil, ErrNoRoots
	}
	cfg = applyDefaults(cfg)

	// Defense-in-depth: verify slug exists as a subdirectory under at least
	// one root. Never mark memories outdated for a slug we can't locate on disk.
	if err := sw.verifySlugInRoots(slug, cfg.Roots); err != nil {
		return nil, err
	}

	result := &SweepResult{
		Slug:    slug,
		PerSlug: make(map[string]*SlugStats),
		DryRun:  cfg.DryRun,
	}
	if err := sw.sweepSlug(ctx, slug, cfg, result); err != nil {
		return nil, err
	}
	return result, nil
}

// verifySlugInRoots returns nil if slug resolves to a directory under any root,
// otherwise an error.
func (sw *Sweeper) verifySlugInRoots(slug string, roots []string) error {
	for _, root := range roots {
		candidate := filepath.Join(root, slug)
		info, err := sw.statFS(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() {
			return nil
		}
	}
	return fmt.Errorf("end-task: slug %q not found as a subdirectory under any configured archive root", slug)
}

// sweepSlug processes the working-memory cohort tied to a single slug.
func (sw *Sweeper) sweepSlug(ctx context.Context, slug string, cfg ArchiveSweepConfig, result *SweepResult) error {
	filters := memory.Filters{Context: slug, Type: memory.TypeWorking}
	memories, err := sw.store.List(ctx, filters, 0)
	if err != nil {
		return fmt.Errorf("list memories for slug %q: %w", slug, err)
	}
	stats := &SlugStats{}
	result.PerSlug[slug] = stats

	for _, m := range memories {
		if m == nil {
			continue
		}
		action := sw.decide(m, slug, cfg)
		result.Actions = append(result.Actions, action)

		switch action.Action {
		case "outdated":
			stats.OutdatedCount++
			result.TotalOutdated++
			if !cfg.DryRun {
				if _, err := sw.store.MarkOutdated(ctx, m.ID, action.Reason, ""); err != nil {
					sw.logger.Warn("archive-sweep: mark outdated failed",
						zap.String("id", m.ID), zap.String("slug", slug), zap.Error(err))
				}
			}
		case "promotion_candidate":
			stats.PromotionCandidates++
			result.TotalPromotionCand++
			if !cfg.DryRun {
				if err := sw.createPromotionCandidate(ctx, m, slug); err != nil {
					sw.logger.Warn("archive-sweep: create review-queue item failed",
						zap.String("id", m.ID), zap.String("slug", slug), zap.Error(err))
				}
			}
		default:
			stats.Skipped++
			result.TotalSkipped++
		}
	}

	sw.logger.Info("archive-sweep: slug processed",
		zap.String("slug", slug),
		zap.Int("outdated", stats.OutdatedCount),
		zap.Int("promotion_candidates", stats.PromotionCandidates),
		zap.Int("skipped", stats.Skipped),
		zap.Bool("dry_run", cfg.DryRun),
	)
	return nil
}

// decide returns the action for a single memory without performing any writes.
func (sw *Sweeper) decide(m *memory.Memory, slug string, cfg ArchiveSweepConfig) ArchiveAction {
	base := ArchiveAction{MemoryID: m.ID, Slug: slug}

	if m.Type != memory.TypeWorking {
		// Belt-and-suspenders: the List filter should exclude these, but if
		// something slips through we skip rather than mutate unrelated types.
		base.Action = "skipped_non_working"
		base.Reason = fmt.Sprintf("type=%s (only working memories are swept)", m.Type)
		return base
	}

	// Review-queue items (including ones we created in a previous sweep) are
	// workflow records, not task-knowledge — never sweep them. This is what
	// makes the sweep idempotent across runs.
	if memory.IsReviewQueueMemory(m) {
		base.Action = "skipped_review_queue_item"
		base.Reason = "record_kind=review_queue_item"
		return base
	}

	if hasTag(m.Tags, cfg.KeepTag) {
		base.Action = "skipped_keep_tag"
		base.Reason = fmt.Sprintf("carries %q tag", cfg.KeepTag)
		return base
	}

	if memory.LifecycleStatusOf(m) == memory.LifecycleOutdated {
		base.Action = "already_outdated"
		base.Reason = "lifecycle=outdated"
		return base
	}

	// Procedural type → always promotion candidate (patterns are reusable).
	// Working memories use Type=working so this path usually fires on
	// importance only, but defense-in-depth for future schema changes.
	if m.Type == memory.TypeProcedural || m.Importance >= cfg.PromotionThreshold {
		base.Action = "promotion_candidate"
		base.Reason = fmt.Sprintf("importance=%.2f threshold=%.2f", m.Importance, cfg.PromotionThreshold)
		return base
	}

	base.Action = "outdated"
	base.Reason = fmt.Sprintf("task archived: %s", slug)
	return base
}

// createPromotionCandidate persists a review_queue_item memory suggesting the
// given memory be promoted. Idempotent: returns nil without writing if a
// matching review item already exists.
func (sw *Sweeper) createPromotionCandidate(ctx context.Context, m *memory.Memory, slug string) error {
	if sw.reviewItemExists(ctx, m.ID) {
		return nil
	}

	content := fmt.Sprintf(
		"Promotion candidate: memory %s from archived task %s.\n"+
			"Importance=%.2f, type=%s.\n"+
			"Suggested action: promote_to_canonical.",
		m.ID, slug, m.Importance, m.Type,
	)

	reviewMem := &memory.Memory{
		Title:   truncate(fmt.Sprintf("Review: promote %s?", displayTitle(m)), 120),
		Content: content,
		Type:    memory.TypeWorking, // review-queue items are working-memory by convention (see session_tracker)
		Context: slug,               // keep the origin slug so review-queue views can filter by it
		Importance: 0.5,
		Tags: []string{
			"review-queue",
			"archive-sweep",
			"review:required",
			"slug:" + slug,
		},
		Metadata: map[string]string{
			memory.MetadataRecordKind:     memory.RecordKindReviewQueueItem,
			memory.MetadataReviewRequired: "true",
			memory.MetadataReviewReason:   "archive_sweep_promotion_candidate",
			"review_target_memory_id":     m.ID,
			"review_source":               "archive_sweep",
			"review_slug":                 slug,
		},
	}
	return sw.store.Store(ctx, reviewMem)
}

// reviewItemExists returns true if a review_queue_item already targets the
// given memory ID. Used for sweep idempotency.
func (sw *Sweeper) reviewItemExists(ctx context.Context, targetID string) bool {
	// Scan working-type memories; review items live in TypeWorking per
	// session_tracker convention.
	all, err := sw.store.List(ctx, memory.Filters{Type: memory.TypeWorking}, 0)
	if err != nil {
		sw.logger.Warn("archive-sweep: failed to list memories for dedup check", zap.Error(err))
		return false
	}
	for _, m := range all {
		if m == nil || !memory.IsReviewQueueMemory(m) {
			continue
		}
		if m.Metadata["review_target_memory_id"] == targetID &&
			m.Metadata["review_source"] == "archive_sweep" {
			return true
		}
	}
	return false
}

func hasTag(tags []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), target) {
			return true
		}
	}
	return false
}

func applyDefaults(cfg ArchiveSweepConfig) ArchiveSweepConfig {
	if cfg.PromotionThreshold <= 0 {
		cfg.PromotionThreshold = DefaultPromotionThreshold
	}
	if strings.TrimSpace(cfg.KeepTag) == "" {
		cfg.KeepTag = KeepAfterArchiveTag
	}
	return cfg
}

func displayTitle(m *memory.Memory) string {
	if m == nil {
		return ""
	}
	if t := strings.TrimSpace(m.Title); t != "" {
		return t
	}
	return m.ID
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
