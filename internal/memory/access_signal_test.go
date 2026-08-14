package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// storeAndClose runs body against a fresh store at dbPath, then closes it so
// the access-stats worker flushes deterministically (Close closes accessCh and
// waits for the worker). Returns a reopened store for assertions.
func storeAndClose(t *testing.T, dbPath string, body func(s *Store)) *Store {
	t.Helper()
	s, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	body(s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened
}

// T113: a sweep — Recall with no result budget, the shape RecallCanonical uses
// — marks every entry above minScore as accessed. That is a scan, not evidence
// that anyone wanted the entry, and it is what drove access_count on the live
// bank to a median of 110. The targeted counter must not move for it.
func TestSweepRecallDoesNotCountAsTargetedAccess(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sweep.db")

	var ids []string
	reopened := storeAndClose(t, dbPath, func(s *Store) {
		for _, content := range []string{
			"traefik ingress rollback notes",
			"traefik ingress certificate renewal",
			"traefik ingress rate limit middleware",
		} {
			m := &Memory{Content: content, Type: TypeSemantic, Context: "t113"}
			if err := s.Store(ctx, m); err != nil {
				t.Fatalf("Store: %v", err)
			}
			ids = append(ids, m.ID)
		}
		// limit = 0: exactly what RecallCanonical passes (projection.go).
		results, err := s.Recall(ctx, "traefik ingress", Filters{Context: "t113"}, 0)
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("sweep returned %d results, want 3 (the sweep must actually touch every entry)", len(results))
		}
	})

	for _, id := range ids {
		m, err := reopened.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if m.AccessCount == 0 {
			t.Errorf("%s: AccessCount = 0, want >0 — the sweep still records that the entry was returned", id)
		}
		if m.TargetedAccessCount != 0 {
			t.Errorf("%s: TargetedAccessCount = %d, want 0 — a sweep is not a targeted retrieval", id, m.TargetedAccessCount)
		}
	}
}

// The other half of the same rule: a bounded query is a question with a result
// budget, and the entries it returns did answer it.
func TestBoundedRecallCountsAsTargetedAccess(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "bounded.db")

	var ids []string
	reopened := storeAndClose(t, dbPath, func(s *Store) {
		for _, content := range []string{
			"traefik ingress rollback notes",
			"traefik ingress certificate renewal",
		} {
			m := &Memory{Content: content, Type: TypeSemantic, Context: "t113"}
			if err := s.Store(ctx, m); err != nil {
				t.Fatalf("Store: %v", err)
			}
			ids = append(ids, m.ID)
		}
		results, err := s.Recall(ctx, "traefik ingress", Filters{Context: "t113"}, 10)
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("bounded recall returned %d results, want 2", len(results))
		}
	})

	for _, id := range ids {
		m, err := reopened.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if m.TargetedAccessCount != 1 {
			t.Errorf("%s: TargetedAccessCount = %d, want 1", id, m.TargetedAccessCount)
		}
	}
}

// T113, second half: the sediment gate reads the honest counter. On the live
// bank 4046 of 4556 entries cleared EpisodicToSemanticMin=3 on the sweep-driven
// counter — a gate that admits 89% of the bank is not a gate.
func TestSedimentGateReadsTargetedCounter(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	aged := func(access, targeted int) *Memory {
		return &Memory{
			ID:                  "m1",
			CreatedAt:           now.Add(-60 * 24 * time.Hour),
			SedimentLayer:       string(LayerEpisodic),
			AccessCount:         access,
			TargetedAccessCount: targeted,
		}
	}
	policy := SedimentPolicy{Now: func() time.Time { return now }}

	if tr := Decide(aged(500, 0), policy); tr != nil {
		t.Errorf("sweep-inflated AccessCount=500 proposed %s→%s; the gate must read targeted accesses", tr.From, tr.To)
	}
	if tr := Decide(aged(0, 3), policy); tr == nil {
		t.Error("TargetedAccessCount=3 proposed no transition, want episodic→semantic")
	}
}
