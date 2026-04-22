package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := memory.NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedTempArchive creates roots with the given slug subdirectories and returns
// the root path(s).
func seedTempArchive(t *testing.T, slugs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, slug := range slugs {
		if err := mkdirp(filepath.Join(root, slug)); err != nil {
			t.Fatalf("mkdirp: %v", err)
		}
	}
	return root
}

// mkdirp is a tiny helper mirroring os.MkdirAll to keep tests concise.
func mkdirp(path string) error {
	return os.MkdirAll(path, 0o755)
}

// seedWorkingMemory stores a working-type memory with given slug, tags,
// importance, and extra metadata.
func seedWorkingMemory(t *testing.T, store *memory.Store, slug, title string, importance float64, tags []string, metadata map[string]string) *memory.Memory {
	t.Helper()
	m := &memory.Memory{
		Title:      title,
		Content:    title + " content",
		Type:       memory.TypeWorking,
		Context:    slug,
		Importance: importance,
		Tags:       append([]string(nil), tags...),
		Metadata:   copyMap(metadata),
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	return m
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func countReviewQueueItemsForTarget(t *testing.T, store *memory.Store, targetID string) int {
	t.Helper()
	all, err := store.List(context.Background(), memory.Filters{Type: memory.TypeWorking}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, m := range all {
		if m == nil || !memory.IsReviewQueueMemory(m) {
			continue
		}
		if m.Metadata["review_target_memory_id"] == targetID &&
			m.Metadata["review_source"] == "archive_sweep" {
			count++
		}
	}
	return count
}

func TestSweep_EmptyRoots_Errors(t *testing.T) {
	store := newTestStore(t)
	sw := NewSweeper(store)
	_, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{})
	if err == nil {
		t.Fatal("expected ErrNoRoots, got nil")
	}
	if err != ErrNoRoots {
		t.Fatalf("expected ErrNoRoots, got %v", err)
	}
}

func TestSweep_MarksOutdatedWorkingMemories(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-A")

	seedWorkingMemory(t, store, "task-A", "note one", 0.3, nil, nil)
	seedWorkingMemory(t, store, "task-A", "note two", 0.4, nil, nil)
	seedWorkingMemory(t, store, "task-A", "note three", 0.5, nil, nil)

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{Roots: []string{root}})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}
	if result.TotalOutdated != 3 {
		t.Fatalf("expected 3 outdated, got %d (actions=%+v)", result.TotalOutdated, result.Actions)
	}
	if result.TotalPromotionCand != 0 {
		t.Fatalf("expected 0 promotion candidates, got %d", result.TotalPromotionCand)
	}

	// Verify memories actually carry lifecycle=outdated (via metadata).
	memories, err := store.List(context.Background(), memory.Filters{Context: "task-A", Type: memory.TypeWorking}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range memories {
		if memory.IsReviewQueueMemory(m) {
			continue
		}
		if memory.LifecycleStatusOf(m) != memory.LifecycleOutdated {
			t.Fatalf("memory %s not marked outdated (lifecycle=%s)", m.ID, memory.LifecycleStatusOf(m))
		}
	}
}

func TestSweep_PromotionCandidate_KeepsActive(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-B")

	m := seedWorkingMemory(t, store, "task-B", "important note", 0.8, nil, nil)

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{Roots: []string{root}})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}

	if result.TotalPromotionCand != 1 {
		t.Fatalf("expected 1 promotion candidate, got %d (actions=%+v)", result.TotalPromotionCand, result.Actions)
	}
	if result.TotalOutdated != 0 {
		t.Fatalf("expected 0 outdated, got %d", result.TotalOutdated)
	}

	// The original memory should NOT be outdated.
	fresh, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if memory.LifecycleStatusOf(fresh) == memory.LifecycleOutdated {
		t.Fatalf("promotion candidate was incorrectly marked outdated")
	}

	// A review-queue item should exist for this target.
	if n := countReviewQueueItemsForTarget(t, store, m.ID); n != 1 {
		t.Fatalf("expected 1 review-queue item for %s, got %d", m.ID, n)
	}
}

func TestSweep_KeepTagRespected(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-C")

	kept := seedWorkingMemory(t, store, "task-C", "keep me", 0.3, []string{KeepAfterArchiveTag}, nil)

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{Roots: []string{root}})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}
	if result.TotalSkipped != 1 {
		t.Fatalf("expected 1 skipped, got %d (actions=%+v)", result.TotalSkipped, result.Actions)
	}
	if result.TotalOutdated != 0 {
		t.Fatalf("expected 0 outdated, got %d", result.TotalOutdated)
	}

	fresh, err := store.Get(kept.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if memory.LifecycleStatusOf(fresh) == memory.LifecycleOutdated {
		t.Fatalf("keep-tagged memory was incorrectly marked outdated")
	}
}

func TestSweep_IdempotentSecondRun(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-D")

	seedWorkingMemory(t, store, "task-D", "low", 0.3, nil, nil)
	hi := seedWorkingMemory(t, store, "task-D", "high", 0.8, nil, nil)

	sw := NewSweeper(store)
	cfg := ArchiveSweepConfig{Roots: []string{root}}

	first, err := sw.SweepArchive(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first SweepArchive: %v", err)
	}
	if first.TotalOutdated != 1 || first.TotalPromotionCand != 1 {
		t.Fatalf("first run: expected 1 outdated + 1 promotion, got outdated=%d promo=%d", first.TotalOutdated, first.TotalPromotionCand)
	}

	second, err := sw.SweepArchive(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second SweepArchive: %v", err)
	}
	if second.TotalOutdated != 0 {
		t.Fatalf("second run should mark 0 outdated, got %d (actions=%+v)", second.TotalOutdated, second.Actions)
	}

	// Only ONE review-queue item for the high-importance memory, despite two runs.
	if n := countReviewQueueItemsForTarget(t, store, hi.ID); n != 1 {
		t.Fatalf("expected exactly 1 review-queue item after re-run, got %d", n)
	}
}

func TestSweep_DryRunDoesNotModify(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-E")

	lo := seedWorkingMemory(t, store, "task-E", "low", 0.3, nil, nil)
	hi := seedWorkingMemory(t, store, "task-E", "high", 0.9, nil, nil)

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{
		Roots:  []string{root},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}
	if result.TotalOutdated != 1 || result.TotalPromotionCand != 1 {
		t.Fatalf("dry run counts wrong: outdated=%d promo=%d", result.TotalOutdated, result.TotalPromotionCand)
	}

	// Neither memory should be modified.
	for _, id := range []string{lo.ID, hi.ID} {
		m, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if memory.LifecycleStatusOf(m) == memory.LifecycleOutdated {
			t.Fatalf("dry run modified %s", id)
		}
	}

	// No review-queue items created.
	if n := countReviewQueueItemsForTarget(t, store, hi.ID); n != 0 {
		t.Fatalf("dry run created %d review-queue items", n)
	}
}

func TestSweep_NonExistentDir_Skipped(t *testing.T) {
	store := newTestStore(t)
	// Do NOT create the directory — pass a bogus path.
	bogus := filepath.Join(t.TempDir(), "does-not-exist")

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{Roots: []string{bogus}})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}
	if result.TotalOutdated != 0 || result.TotalPromotionCand != 0 {
		t.Fatalf("expected 0 actions for nonexistent root, got outdated=%d promo=%d", result.TotalOutdated, result.TotalPromotionCand)
	}
}

func TestEndTask_ValidatesSlugUnderRoot(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "known-slug")

	seedWorkingMemory(t, store, "unknown-slug", "some note", 0.3, nil, nil)

	sw := NewSweeper(store)
	_, err := sw.EndTask(context.Background(), "unknown-slug", ArchiveSweepConfig{Roots: []string{root}})
	if err == nil {
		t.Fatal("expected error for slug not under any root, got nil")
	}

	// And the memory must remain untouched.
	memories, _ := store.List(context.Background(), memory.Filters{Context: "unknown-slug", Type: memory.TypeWorking}, 0)
	for _, m := range memories {
		if memory.LifecycleStatusOf(m) == memory.LifecycleOutdated {
			t.Fatalf("memory under unvalidated slug was touched (id=%s)", m.ID)
		}
	}
}

func TestEndTask_ValidSlug_Sweeps(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "valid-slug")

	seedWorkingMemory(t, store, "valid-slug", "note 1", 0.3, nil, nil)
	seedWorkingMemory(t, store, "valid-slug", "note 2", 0.3, nil, nil)

	sw := NewSweeper(store)
	result, err := sw.EndTask(context.Background(), "valid-slug", ArchiveSweepConfig{Roots: []string{root}})
	if err != nil {
		t.Fatalf("EndTask: %v", err)
	}
	if result.Slug != "valid-slug" {
		t.Fatalf("expected Slug=valid-slug, got %q", result.Slug)
	}
	if result.TotalOutdated != 2 {
		t.Fatalf("expected 2 outdated, got %d", result.TotalOutdated)
	}
}

func TestSweep_NonWorkingTypeIgnored(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-mixed")

	// Working memory — should be swept.
	working := seedWorkingMemory(t, store, "task-mixed", "working note", 0.3, nil, nil)

	// Semantic memory with same context — must NOT be touched.
	semantic := &memory.Memory{
		Title:      "semantic fact",
		Content:    "a durable fact about the system",
		Type:       memory.TypeSemantic,
		Context:    "task-mixed",
		Importance: 0.3,
	}
	if err := store.Store(context.Background(), semantic); err != nil {
		t.Fatalf("Store semantic: %v", err)
	}

	// Episodic memory with same context — also must NOT be touched.
	episodic := &memory.Memory{
		Title:      "episodic event",
		Content:    "something happened",
		Type:       memory.TypeEpisodic,
		Context:    "task-mixed",
		Importance: 0.3,
	}
	if err := store.Store(context.Background(), episodic); err != nil {
		t.Fatalf("Store episodic: %v", err)
	}

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{Roots: []string{root}})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}
	if result.TotalOutdated != 1 {
		t.Fatalf("expected 1 outdated (the working memory), got %d (actions=%+v)", result.TotalOutdated, result.Actions)
	}

	// Verify semantic and episodic are untouched.
	for _, id := range []string{semantic.ID, episodic.ID} {
		m, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if memory.LifecycleStatusOf(m) == memory.LifecycleOutdated {
			t.Fatalf("non-working memory %s was swept", id)
		}
	}

	// And the working memory IS outdated.
	fresh, _ := store.Get(working.ID)
	if memory.LifecycleStatusOf(fresh) != memory.LifecycleOutdated {
		t.Fatalf("working memory not outdated: %s", memory.LifecycleStatusOf(fresh))
	}
}

func TestSweep_SlugPatternFilter(t *testing.T) {
	store := newTestStore(t)
	root := seedTempArchive(t, "task-001", "random", "task-002")

	seedWorkingMemory(t, store, "task-001", "one", 0.3, nil, nil)
	seedWorkingMemory(t, store, "random", "ignored", 0.3, nil, nil)
	seedWorkingMemory(t, store, "task-002", "two", 0.3, nil, nil)

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{
		Roots:       []string{root},
		SlugPattern: regexp.MustCompile(`^task-\d+$`),
	})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}
	if result.TotalOutdated != 2 {
		t.Fatalf("expected 2 outdated (task-001 + task-002), got %d (actions=%+v)", result.TotalOutdated, result.Actions)
	}
}

func TestSweep_IntegrationMultiSlug(t *testing.T) {
	// 10 working memories across 2 slugs:
	// slug-alpha: 5 memories (2 keep-tag, 3 low importance)
	// slug-beta:  5 memories (1 keep-tag, 2 low importance, 2 high importance)
	store := newTestStore(t)
	root := seedTempArchive(t, "slug-alpha", "slug-beta")

	for i := 0; i < 3; i++ {
		seedWorkingMemory(t, store, "slug-alpha", "alpha-low", 0.3, nil, nil)
	}
	for i := 0; i < 2; i++ {
		seedWorkingMemory(t, store, "slug-alpha", "alpha-keep", 0.3, []string{KeepAfterArchiveTag}, nil)
	}

	for i := 0; i < 2; i++ {
		seedWorkingMemory(t, store, "slug-beta", "beta-low", 0.3, nil, nil)
	}
	seedWorkingMemory(t, store, "slug-beta", "beta-keep", 0.3, []string{KeepAfterArchiveTag}, nil)
	for i := 0; i < 2; i++ {
		seedWorkingMemory(t, store, "slug-beta", "beta-high", 0.85, nil, nil)
	}

	sw := NewSweeper(store)
	result, err := sw.SweepArchive(context.Background(), ArchiveSweepConfig{Roots: []string{root}})
	if err != nil {
		t.Fatalf("SweepArchive: %v", err)
	}

	// Expected: outdated=3+2=5, skipped=2+1=3, promotion=2.
	if result.TotalOutdated != 5 {
		t.Fatalf("expected 5 outdated, got %d", result.TotalOutdated)
	}
	if result.TotalSkipped != 3 {
		t.Fatalf("expected 3 skipped, got %d", result.TotalSkipped)
	}
	if result.TotalPromotionCand != 2 {
		t.Fatalf("expected 2 promotion candidates, got %d", result.TotalPromotionCand)
	}
}
