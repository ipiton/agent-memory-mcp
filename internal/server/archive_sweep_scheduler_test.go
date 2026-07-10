package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// seedArchivedWorkingMemory creates <root>/tasks/archive/<slug>/ on disk and a
// low-importance working memory bound to that slug, so a sweep should mark it
// outdated. Returns the slug.
func seedArchivedWorkingMemory(t *testing.T, s *MCPServer, slug string) {
	t.Helper()
	archiveDir := filepath.Join(s.config.RootPath, "tasks", "archive", slug)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("mkdir archive dir: %v", err)
	}
	m := &memory.Memory{
		Content:    "scratch note for " + slug,
		Type:       memory.TypeWorking,
		Context:    slug,
		Importance: 0.1, // below promotion threshold -> outdated
	}
	if err := s.memoryStore.Store(context.Background(), m); err != nil {
		t.Fatalf("store working memory: %v", err)
	}
}

func TestResolveArchiveSweepRoots(t *testing.T) {
	s := newTestServer(t, "")

	// No explicit roots -> convention <root>/tasks/archive.
	got := s.resolveArchiveSweepRoots()
	want := filepath.Join(s.config.RootPath, "tasks", "archive")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("convention roots = %v, want [%s]", got, want)
	}

	// Explicit config wins.
	s.config.Lifecycle.TaskArchiveRoots = []string{"/tmp/explicit-a", "/tmp/explicit-b"}
	got = s.resolveArchiveSweepRoots()
	if len(got) != 2 || got[0] != "/tmp/explicit-a" {
		t.Fatalf("explicit roots = %v, want the configured pair", got)
	}
}

func TestNewArchiveSweepScheduler_NilGuards(t *testing.T) {
	s := newMemoryTestServer(t)
	if newArchiveSweepScheduler(s, false, time.Hour) != nil {
		t.Error("disabled scheduler must be nil")
	}
	if newArchiveSweepScheduler(s, true, 0) != nil {
		t.Error("zero-interval scheduler must be nil")
	}
	noStore := newTestServer(t, "") // memoryStore is nil
	if newArchiveSweepScheduler(noStore, true, time.Hour) != nil {
		t.Error("scheduler without a memory store must be nil")
	}
	if newArchiveSweepScheduler(s, true, time.Hour) == nil {
		t.Error("enabled scheduler with store and interval must be non-nil")
	}
}

func TestRunArchiveSweepOnce_ConsolidatesArchivedSlug(t *testing.T) {
	s := newMemoryTestServer(t)
	seedArchivedWorkingMemory(t, s, "closed-task-1")

	result, err := s.runArchiveSweepOnce(context.Background())
	if err != nil {
		t.Fatalf("runArchiveSweepOnce: %v", err)
	}
	if result == nil {
		t.Fatal("expected a sweep result, got nil")
	}
	if result.TotalOutdated != 1 {
		t.Fatalf("TotalOutdated = %d, want 1 (the archived working memory)", result.TotalOutdated)
	}
}

func TestRunArchiveSweepOnce_NoopWithoutArchiveDir(t *testing.T) {
	s := newMemoryTestServer(t)
	// No tasks/archive directory created — convention root does not exist.
	result, err := s.runArchiveSweepOnce(context.Background())
	if err != nil {
		t.Fatalf("runArchiveSweepOnce should no-op, got err: %v", err)
	}
	if result == nil {
		return // acceptable: nil result
	}
	if result.TotalOutdated != 0 || result.TotalPromoted != 0 {
		t.Fatalf("expected no consolidation without an archive dir, got %+v", result)
	}
}

func TestRunArchiveSweepOnce_NoopWithoutStore(t *testing.T) {
	s := newTestServer(t, "") // no memory store
	result, err := s.runArchiveSweepOnce(context.Background())
	if err != nil || result != nil {
		t.Fatalf("expected nil,nil without a store; got result=%v err=%v", result, err)
	}
}

// TestArchiveSweepScheduler_FiresInitialRun proves the background loop performs
// the delayed initial sweep and consolidates the archived slug.
func TestArchiveSweepScheduler_FiresInitialRun(t *testing.T) {
	prev := archiveSweepInitialDelay
	archiveSweepInitialDelay = 10 * time.Millisecond
	t.Cleanup(func() { archiveSweepInitialDelay = prev })

	s := newMemoryTestServer(t)
	seedArchivedWorkingMemory(t, s, "closed-task-bg")

	sched := newArchiveSweepScheduler(s, true, time.Hour)
	if sched == nil {
		t.Fatal("scheduler must be non-nil")
	}
	sched.Start()
	t.Cleanup(sched.Close)

	// Poll until the initial sweep marks the memory outdated.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mems, err := s.memoryStore.List(context.Background(), memory.Filters{Context: "closed-task-bg", Type: memory.TypeWorking}, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(mems) == 1 && memory.LifecycleStatusOf(mems[0]) == memory.LifecycleOutdated {
			return // scheduler ran and consolidated
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial sweep did not consolidate within deadline (memories=%d)", len(mems))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBuildSweepConfigDefaultsAutoPromoteTrue(t *testing.T) {
	s := newMemoryTestServer(t)

	// Absent auto_promote -> defaults to true (T63 zero-ops).
	cfg, rErr := s.buildSweepConfigFromArgs(map[string]any{}, false)
	if rErr != nil {
		t.Fatalf("buildSweepConfigFromArgs: %+v", rErr)
	}
	if !cfg.AutoPromote {
		t.Error("auto_promote must default to true")
	}
	// Explicit false is honored.
	cfg, rErr = s.buildSweepConfigFromArgs(map[string]any{"auto_promote": false}, false)
	if rErr != nil {
		t.Fatalf("buildSweepConfigFromArgs: %+v", rErr)
	}
	if cfg.AutoPromote {
		t.Error("explicit auto_promote=false must be honored")
	}
	// Roots fall back to the convention when unset.
	if len(cfg.Roots) != 1 || cfg.Roots[0] != filepath.Join(s.config.RootPath, "tasks", "archive") {
		t.Errorf("roots = %v, want the convention fallback", cfg.Roots)
	}
}
