package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

var (
	t191Created    = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t191Superseded = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
)

// supersedeForTest stores two records and retires the first through the real
// MarkOutdated at t191Superseded, so the test sees the exact row state the tool
// produces rather than hand-set temporal fields.
func supersedeForTest(t *testing.T, store *Store, memContext string) (oldID, newID string) {
	t.Helper()
	ctx := context.Background()
	store.SetClock(func() time.Time { return t191Created })

	predecessor := &Memory{Content: "routing uses gorilla mux", Title: "Routing v1", Type: TypeSemantic, Context: memContext, Importance: 0.8}
	if err := store.Store(ctx, predecessor); err != nil {
		t.Fatalf("Store predecessor: %v", err)
	}
	successor := &Memory{Content: "routing uses chi router", Title: "Routing v2", Type: TypeSemantic, Context: memContext, Importance: 0.8}
	if err := store.Store(ctx, successor); err != nil {
		t.Fatalf("Store successor: %v", err)
	}

	store.SetClock(func() time.Time { return t191Superseded })
	if _, err := store.MarkOutdated(ctx, predecessor.ID, "replaced", successor.ID); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}
	return predecessor.ID, successor.ID
}

func resultIDs(results []*SearchResult) map[string]bool {
	ids := make(map[string]bool, len(results))
	for _, r := range results {
		ids[r.Memory.ID] = true
	}
	return ids
}

// T191: the question supersession history exists to answer. Before the switch
// the predecessor was the valid record, and hiding it left the answer empty.
func TestRecallAsOf_FollowsSupersession(t *testing.T) {
	store := newTemporalTestStore(t)
	oldID, newID := supersedeForTest(t, store, "")

	cases := []struct {
		name     string
		asOf     time.Time
		wantOld  bool
		wantNew  bool
		wantNote string
	}{
		{"before supersession", t191Superseded.Add(-time.Hour), true, false, "the predecessor was current"},
		{"at the moment of supersession", t191Superseded, false, true, "valid_until is exclusive, valid_from inclusive"},
		{"after supersession", t191Superseded.Add(time.Hour), false, true, "the successor is current"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := store.RecallAsOf(context.Background(), "routing", tc.asOf, Filters{}, 10)
			if err != nil {
				t.Fatalf("RecallAsOf: %v", err)
			}
			ids := resultIDs(results)
			if ids[oldID] != tc.wantOld {
				t.Errorf("predecessor returned = %v, want %v (%s)", ids[oldID], tc.wantOld, tc.wantNote)
			}
			if ids[newID] != tc.wantNew {
				t.Errorf("successor returned = %v, want %v (%s)", ids[newID], tc.wantNew, tc.wantNote)
			}
		})
	}
}

// T191: the timeline is the chain itself, so both ends must be on it, oldest
// first and still linked to each other.
func TestKnowledgeTimeline_IncludesSupersededEntries(t *testing.T) {
	store := newTemporalTestStore(t)
	oldID, newID := supersedeForTest(t, store, "")

	entries, err := store.KnowledgeTimeline(context.Background(), "routing", "", "")
	if err != nil {
		t.Fatalf("KnowledgeTimeline: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("timeline has %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].MemoryID != oldID || entries[1].MemoryID != newID {
		t.Fatalf("timeline order = [%s, %s], want [%s, %s]", entries[0].MemoryID, entries[1].MemoryID, oldID, newID)
	}
	if entries[0].SupersededBy != newID {
		t.Errorf("predecessor superseded_by = %q, want %q", entries[0].SupersededBy, newID)
	}
	if entries[0].Status != string(LifecycleSuperseded) {
		t.Errorf("predecessor status = %q, want %q", entries[0].Status, LifecycleSuperseded)
	}
	if entries[1].Replaces != oldID {
		t.Errorf("successor replaces = %q, want %q", entries[1].Replaces, oldID)
	}
}

// T191 guard: history is a property of the two temporal tools only. Ordinary
// recall keeps answering with current knowledge.
func TestRecall_StillHidesSupersededEntries(t *testing.T) {
	store := newTemporalTestStore(t)
	oldID, newID := supersedeForTest(t, store, "")

	results, err := store.Recall(context.Background(), "routing", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	ids := resultIDs(results)
	if ids[oldID] {
		t.Error("Recall returned the superseded entry")
	}
	if !ids[newID] {
		t.Error("Recall did not return the successor")
	}
}

// T191: validity is decided before the result budget is spent. MarkOutdated caps
// a retired record's importance, so when the window was applied after ranking,
// records that did not exist yet could take every slot and push it out.
func TestRecallAsOf_NotCrowdedOutByLaterRecords(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()
	oldID, _ := supersedeForTest(t, store, "")

	validFrom := t191Superseded.Add(time.Minute)
	for i := 0; i < 210; i++ {
		later := &Memory{
			Content:    fmt.Sprintf("routing note number %d", i),
			Type:       TypeSemantic,
			Importance: 1,
			ValidFrom:  &validFrom,
		}
		if err := store.Store(ctx, later); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}

	results, err := store.RecallAsOf(ctx, "routing", t191Superseded.Add(-time.Hour), Filters{}, 5)
	if err != nil {
		t.Fatalf("RecallAsOf: %v", err)
	}
	if !resultIDs(results)[oldID] {
		t.Errorf("predecessor missing from %d results", len(results))
	}
}

// T191: only the supersession filter is lifted. The surface-layer gate keeps
// session scratch state inside its own context on the history path too.
func TestHistoryRecall_KeepsSurfaceLayerGate(t *testing.T) {
	store := newTemporalTestStore(t)
	store.SetSedimentEnabled(true)
	ctx := context.Background()
	oldID, _ := supersedeForTest(t, store, "session-a")
	asOf := t191Superseded.Add(-time.Hour)

	outside, err := store.RecallAsOf(ctx, "routing", asOf, Filters{}, 10)
	if err != nil {
		t.Fatalf("RecallAsOf outside context: %v", err)
	}
	if resultIDs(outside)[oldID] {
		t.Error("surface record leaked outside its context")
	}

	inside, err := store.RecallAsOf(ctx, "routing", asOf, Filters{Context: "session-a"}, 10)
	if err != nil {
		t.Fatalf("RecallAsOf inside context: %v", err)
	}
	if !resultIDs(inside)[oldID] {
		t.Error("surface record missing inside its own context")
	}
}

// T191: looking a retired record up in history says nothing about its current
// usefulness, and the sediment gate reads this counter to promote records.
func TestHistoryRecall_DoesNotCountAsTargetedAccess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	var oldID string
	reopened := storeAndClose(t, dbPath, func(s *Store) {
		oldID, _ = supersedeForTest(t, s, "")
		results, err := s.RecallAsOf(ctx, "routing", t191Superseded.Add(-time.Hour), Filters{}, 10)
		if err != nil {
			t.Fatalf("RecallAsOf: %v", err)
		}
		if !resultIDs(results)[oldID] {
			t.Fatal("predecessor missing from RecallAsOf")
		}
		if _, err := s.KnowledgeTimeline(ctx, "routing", "", ""); err != nil {
			t.Fatalf("KnowledgeTimeline: %v", err)
		}
	})

	m, err := reopened.Get(oldID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if m.TargetedAccessCount != 0 {
		t.Errorf("TargetedAccessCount = %d, want 0", m.TargetedAccessCount)
	}
}
