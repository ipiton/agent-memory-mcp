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
//
// Concurrency: SweepArchive/EndTask are NOT safe for concurrent invocation on
// the same slug — the dedup check (reviewItemExists) races with the subsequent
// Store call and may produce duplicate review_queue items. Callers must
// serialize sweeps per slug.
//
// Symlinks: archive roots are traversed via os.ReadDir, which follows
// symlinks. Ensure archive roots are under administrator control; an untrusted
// symlink inside a root could cause the sweep to consider an unintended slug.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/textfmt"
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

// sweptTypes is the set of memory types the sweep considers. Procedural
// memories are included so the promotion-candidate branch in decide() can fire.
// Semantic/Episodic are deliberately excluded — they are durable knowledge,
// not task-scoped working state.
var sweptTypes = []memory.Type{memory.TypeWorking, memory.TypeProcedural}

// storeAPI is the narrow slice of *memory.Store that Sweeper depends on.
// Extracted to let tests inject failing/mocked stores for error-path coverage
// without spinning up the full SQLite store. *memory.Store satisfies this
// interface natively.
type storeAPI interface {
	List(ctx context.Context, filters memory.Filters, limit int) ([]*memory.Memory, error)
	Store(ctx context.Context, m *memory.Memory) error
	MarkOutdated(ctx context.Context, id string, reason string, supersededBy string) (*memory.MarkOutdatedResult, error)
	PromoteToCanonical(ctx context.Context, id string, owner string, verified bool) (*memory.PromoteToCanonicalResult, error)
}

// AutoPromoteOwner is the owner string written to memories that the sweep
// promotes directly (cfg.AutoPromote=true). Distinct from user-driven
// promotions so audit history can trace canonical entries back to the sweep.
const AutoPromoteOwner = "archive-sweep"

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

	// AutoPromote: when true, promotion candidates are promoted to canonical
	// in-place via store.PromoteToCanonical instead of emitting a
	// review-queue item for human triage. Used for autonomous post-archive
	// consolidation (T62) — reduces inbox growth proportional to closed
	// tasks. DryRun still wins: nothing is written.
	AutoPromote bool
}

// ArchiveAction is a single decision made during a sweep.
type ArchiveAction struct {
	MemoryID string `json:"memory_id"`
	Slug     string `json:"slug"`
	// Action is one of: "outdated" | "promotion_candidate" | "skipped_keep_tag"
	// | "already_outdated" | "skipped_non_working" | "skipped_review_queue_item".
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// SlugStats aggregates per-slug counters.
type SlugStats struct {
	OutdatedCount       int `json:"outdated_count"`
	PromotionCandidates int `json:"promotion_candidates"`
	Promoted            int `json:"promoted"` // T62: in-place canonical promotions (AutoPromote=true)
	Skipped             int `json:"skipped"`
}

// SweepResult is the outcome of SweepArchive or EndTask.
type SweepResult struct {
	Slug               string                `json:"slug,omitempty"` // empty for multi-slug sweep
	PerSlug            map[string]*SlugStats `json:"per_slug,omitempty"`
	TotalOutdated      int                   `json:"total_outdated"`
	TotalPromotionCand int                   `json:"total_promotion_candidates"`
	TotalPromoted      int                   `json:"total_promoted"` // T62: in-place canonical promotions
	TotalSkipped       int                   `json:"total_skipped"`
	Actions            []ArchiveAction       `json:"actions,omitempty"`
	// Errors records per-memory partial failures as "<memory-id>: <error>"
	// entries. A non-empty Errors slice means counters reflect only successful
	// writes — callers should surface this to operators (CLI exits non-zero,
	// MCP response includes the list).
	Errors []string `json:"errors,omitempty"`
	DryRun bool     `json:"dry_run"`
}

// Receiver name 'sw' avoids a local secret-scanner false positive on 's.*'.
// Safe to rename later once the scanner is tightened.
//
// Sweeper orchestrates archive-sweep runs against a memory store.
//
// After construction Sweeper holds only read-only references — the store
// itself owns concurrency guarantees. See package godoc for the per-slug
// serialization requirement.
type Sweeper struct {
	store     storeAPI
	logger    *zap.Logger
	now       func() time.Time
	statFS    func(path string) (os.FileInfo, error) // injectable for tests
	readDirFS func(path string) ([]os.DirEntry, error)

	// slugLocks serialises per-slug sweeps. Round 3 M8: previously the
	// "not safe for concurrent invocation on the same slug" property was
	// document-only — a concurrent SweepArchive + EndTask race could
	// duplicate review-queue items between the existence check and the
	// Store call. The map is loaded with sync.LoadOrStore so we keep one
	// *sync.Mutex per slug across the lifetime of the Sweeper.
	slugLocks sync.Map // map[string]*sync.Mutex
}

// lockSlug returns a function that releases the per-slug lock when called.
func (sw *Sweeper) lockSlug(slug string) func() {
	v, _ := sw.slugLocks.LoadOrStore(slug, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
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
	return newSweeperFromAPI(store, opts...)
}

// newSweeperFromAPI is the internal constructor that accepts the narrow
// storeAPI interface. Tests use it to inject failing/fake stores; production
// callers use NewSweeper which passes *memory.Store.
func newSweeperFromAPI(store storeAPI, opts ...Option) *Sweeper {
	sw := &Sweeper{
		store:     store,
		logger:    zap.NewNop(),
		now:       time.Now,
		statFS:    statNoSymlink,
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
	if slug == "." || slug == ".." || strings.ContainsAny(slug, "/\\") {
		return nil, fmt.Errorf("end-task: invalid slug %q", slug)
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
// otherwise an error. Defends against path-traversal by rejecting empty /
// "." / ".." / slash-bearing slugs up front and confirming filepath.Rel
// between root and join(root, slug) doesn't escape.
func (sw *Sweeper) verifySlugInRoots(slug string, roots []string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" || slug == "." || slug == ".." || strings.ContainsAny(slug, "/\\") {
		return fmt.Errorf("end-task: invalid slug %q", slug)
	}
	for _, root := range roots {
		candidate := filepath.Join(root, slug)
		rel, err := filepath.Rel(root, candidate)
		if err != nil {
			continue
		}
		if strings.HasPrefix(rel, "..") || rel == "." {
			continue
		}
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

// sweepSlug processes the memory cohort tied to a single slug.
//
// Lists memories of every type in sweptTypes (Working + Procedural) with the
// slug as Context; each entry is passed to decide() which emits one
// ArchiveAction. The procedural path in decide() would be dead code if the
// List filter were restricted to TypeWorking — see the sweptTypes comment.
func (sw *Sweeper) sweepSlug(ctx context.Context, slug string, cfg ArchiveSweepConfig, result *SweepResult) error {
	defer sw.lockSlug(slug)()

	var memories []*memory.Memory
	for _, t := range sweptTypes {
		m, err := sw.store.List(ctx, memory.Filters{Context: slug, Type: t}, 0)
		if err != nil {
			return fmt.Errorf("list %s memories for slug %q: %w", t, slug, err)
		}
		memories = append(memories, m...)
	}

	// Round 3 H7: pre-load existing review-queue items for this slug ONCE
	// so createPromotionCandidate doesn't re-List on every promotion
	// candidate (was O(n*m)). One List instead of N for slugs with many
	// promotion candidates.
	existingReviews := make(map[string]struct{})
	for _, m := range memories {
		if m == nil || !memory.IsReviewQueueMemory(m) {
			continue
		}
		if m.Metadata[memory.MetadataReviewSource] != memory.ReviewSourceArchiveSweep {
			continue
		}
		if target := strings.TrimSpace(m.Metadata[memory.MetadataReviewTargetMemoryID]); target != "" {
			existingReviews[target] = struct{}{}
		}
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
			if cfg.DryRun {
				stats.OutdatedCount++
				result.TotalOutdated++
				break
			}
			if _, err := sw.store.MarkOutdated(ctx, m.ID, action.Reason, ""); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: mark outdated: %v", m.ID, err))
				sw.logger.Warn("archive-sweep: mark outdated failed",
					zap.String("id", m.ID), zap.String("slug", slug), zap.Error(err))
				break
			}
			stats.OutdatedCount++
			result.TotalOutdated++
		case "promotion_candidate":
			if cfg.DryRun {
				if cfg.AutoPromote {
					stats.Promoted++
					result.TotalPromoted++
				} else {
					stats.PromotionCandidates++
					result.TotalPromotionCand++
				}
				break
			}
			if cfg.AutoPromote {
				_, err := sw.store.PromoteToCanonical(ctx, m.ID, AutoPromoteOwner, false)
				if errors.Is(err, memory.ErrPromotionRequiresVerification) {
					// T77: a conversational-origin record must not be
					// auto-canonicalized (memory-poisoning defense). Route it to
					// human review instead of promoting or hard-erroring.
					if cerr := sw.createPromotionCandidate(ctx, m, slug, existingReviews); cerr != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("%s: create review-queue item: %v", m.ID, cerr))
						sw.logger.Warn("archive-sweep: create review-queue item failed",
							zap.String("id", m.ID), zap.String("slug", slug), zap.Error(cerr))
						break
					}
					existingReviews[m.ID] = struct{}{}
					stats.PromotionCandidates++
					result.TotalPromotionCand++
					break
				}
				if err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s: promote to canonical: %v", m.ID, err))
					sw.logger.Warn("archive-sweep: promote to canonical failed",
						zap.String("id", m.ID), zap.String("slug", slug), zap.Error(err))
					break
				}
				stats.Promoted++
				result.TotalPromoted++
				break
			}
			if err := sw.createPromotionCandidate(ctx, m, slug, existingReviews); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: create review-queue item: %v", m.ID, err))
				sw.logger.Warn("archive-sweep: create review-queue item failed",
					zap.String("id", m.ID), zap.String("slug", slug), zap.Error(err))
				break
			}
			existingReviews[m.ID] = struct{}{} // keep dedup set hot
			stats.PromotionCandidates++
			result.TotalPromotionCand++
		default:
			stats.Skipped++
			result.TotalSkipped++
		}
	}

	sw.logger.Info("archive-sweep: slug processed",
		zap.String("slug", slug),
		zap.Int("outdated", stats.OutdatedCount),
		zap.Int("promotion_candidates", stats.PromotionCandidates),
		zap.Int("promoted", stats.Promoted),
		zap.Int("skipped", stats.Skipped),
		zap.Bool("dry_run", cfg.DryRun),
		zap.Bool("auto_promote", cfg.AutoPromote),
	)
	return nil
}

// decide returns the action for a single memory without performing any writes.
func (sw *Sweeper) decide(m *memory.Memory, slug string, cfg ArchiveSweepConfig) ArchiveAction {
	base := ArchiveAction{MemoryID: m.ID, Slug: slug}

	if !isSweptType(m.Type) {
		// Belt-and-suspenders: the List filter should exclude these, but if a
		// future change adds a new type to sweptTypes without updating the
		// decide() switch, we skip rather than mutate unrelated types.
		base.Action = "skipped_non_working"
		base.Reason = fmt.Sprintf("type=%s (only working/procedural memories are swept)", m.Type)
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
	// importance only.
	if m.Type == memory.TypeProcedural || m.Importance >= cfg.PromotionThreshold {
		base.Action = "promotion_candidate"
		base.Reason = fmt.Sprintf("importance=%.2f threshold=%.2f type=%s", m.Importance, cfg.PromotionThreshold, m.Type)
		return base
	}

	base.Action = "outdated"
	base.Reason = fmt.Sprintf("task archived: %s", slug)
	return base
}

// isSweptType reports whether t is in sweptTypes.
func isSweptType(t memory.Type) bool {
	for _, s := range sweptTypes {
		if t == s {
			return true
		}
	}
	return false
}

// createPromotionCandidate persists a review_queue_item memory suggesting the
// given memory be promoted. Idempotent: returns nil without writing if a
// matching review item already exists.
//
// existingReviews is a pre-loaded set of target IDs that already have a
// review-queue item from an earlier sweep (Round 3 H7); pass nil to fall
// back to the legacy per-call List scan (used by paths that don't run
// inside sweepSlug).
func (sw *Sweeper) createPromotionCandidate(ctx context.Context, m *memory.Memory, slug string, existingReviews map[string]struct{}) error {
	if existingReviews != nil {
		if _, exists := existingReviews[m.ID]; exists {
			return nil
		}
	} else {
		exists, err := sw.reviewItemExists(ctx, slug, m.ID)
		if err != nil {
			return fmt.Errorf("dedup check: %w", err)
		}
		if exists {
			return nil
		}
	}

	content := fmt.Sprintf(
		"Promotion candidate: memory %s from archived task %s.\n"+
			"Importance=%.2f, type=%s.\n"+
			"Suggested action: promote_to_canonical.",
		m.ID, slug, m.Importance, m.Type,
	)

	reviewMem := &memory.Memory{
		Title:      truncate(fmt.Sprintf("Review: promote %s?", displayTitle(m)), 120),
		Content:    content,
		Type:       memory.TypeWorking, // review-queue items are working-memory by convention (see session_tracker)
		Context:    slug,               // keep the origin slug so review-queue views can filter by it
		Importance: 0.5,
		Tags: []string{
			"review-queue",
			"archive-sweep",
			"review:required",
			"slug:" + slug,
		},
		Metadata: map[string]string{
			memory.MetadataRecordKind:           memory.RecordKindReviewQueueItem,
			memory.MetadataReviewRequired:       "true",
			memory.MetadataReviewReason:         "archive_sweep_promotion_candidate",
			memory.MetadataReviewTargetMemoryID: m.ID,
			memory.MetadataReviewSource:         memory.ReviewSourceArchiveSweep,
			memory.MetadataReviewSlug:           slug,
		},
	}
	return sw.store.Store(ctx, reviewMem)
}

// reviewItemExists returns true iff a review_queue_item from an earlier sweep
// already targets the given memory ID within the same slug. Scoped to the
// slug's Context (that's where createPromotionCandidate writes) to avoid a
// full-store scan on every high-importance working memory.
func (sw *Sweeper) reviewItemExists(ctx context.Context, slug, targetID string) (bool, error) {
	items, err := sw.store.List(ctx, memory.Filters{Context: slug, Type: memory.TypeWorking}, 0)
	if err != nil {
		return false, err
	}
	for _, m := range items {
		if m == nil || !memory.IsReviewQueueMemory(m) {
			continue
		}
		if m.Metadata[memory.MetadataReviewTargetMemoryID] == targetID &&
			m.Metadata[memory.MetadataReviewSource] == memory.ReviewSourceArchiveSweep {
			return true, nil
		}
	}
	return false, nil
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

// truncate is a thin alias for textfmt.Truncate. Kept to minimize churn at
// the call sites; new code should call textfmt.Truncate directly.
func truncate(s string, max int) string {
	return textfmt.Truncate(s, max)
}

// statNoSymlink is the production statFS implementation. It uses os.Lstat
// (does not follow symlinks) and returns os.ErrNotExist when the target is
// a symlink, so a symlink under an archive root cannot redirect the
// sweeper to mark an unrelated slug as stale (Round 3 M9 — symlink
// injection guard). For directories that are not symlinks, behaviour
// matches os.Stat.
func statNoSymlink(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrNotExist
	}
	return info, nil
}

// FormatSweepResult renders a SweepResult as a human-readable multi-line
// string. Shared by the CLI and MCP paths so both surfaces report identical
// wording.
func FormatSweepResult(r *SweepResult) string {
	mode := "live"
	if r.DryRun {
		mode = "dry-run"
	}
	var b strings.Builder
	if r.Slug != "" {
		fmt.Fprintf(&b, "end_task sweep (%s) for slug %q:\n", mode, r.Slug)
	} else {
		fmt.Fprintf(&b, "Archive sweep (%s):\n", mode)
	}
	fmt.Fprintf(&b, "- Outdated: %d\n", r.TotalOutdated)
	fmt.Fprintf(&b, "- Promotion candidates: %d\n", r.TotalPromotionCand)
	if r.TotalPromoted > 0 {
		fmt.Fprintf(&b, "- Promoted: %d\n", r.TotalPromoted)
	}
	fmt.Fprintf(&b, "- Skipped: %d\n", r.TotalSkipped)
	if len(r.PerSlug) > 0 {
		b.WriteString("\nPer-slug:\n")
		for slug, stats := range r.PerSlug {
			fmt.Fprintf(&b, "- %s: outdated=%d, promotion=%d, promoted=%d, skipped=%d\n",
				slug, stats.OutdatedCount, stats.PromotionCandidates, stats.Promoted, stats.Skipped)
		}
	}
	if len(r.Errors) > 0 {
		b.WriteString("\nErrors:\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	return b.String()
}
