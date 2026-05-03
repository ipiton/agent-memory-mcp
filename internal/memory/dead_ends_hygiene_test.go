package memory

import (
	"testing"
	"time"
)

// seedDeadEndAge inserts a dead_end-typed memory and back-dates its
// CreatedAt so the hygiene tests can assert on age-based filtering. We
// touch the row directly because Store.Store() always assigns CreatedAt =
// now, which would defeat the test's premise.
func seedDeadEndAge(t *testing.T, store *Store, title string, age time.Duration) *Memory {
	t.Helper()
	mem := &Memory{
		Title:      title,
		Content:    "abandoned approach: " + title,
		Type:       DefaultStorageTypeForEngineeringType(EngineeringTypeDeadEnd),
		Tags:       BuildEngineeringTags(EngineeringTypeDeadEnd, "", "", "", false, nil),
		Metadata:   BuildEngineeringMetadata(EngineeringTypeDeadEnd, "", "", "", false, nil),
		Importance: 0.6,
	}
	if err := store.Store(t.Context(), mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Back-date the row so age-based selection has something to find.
	old := time.Now().Add(-age).UTC()
	_, err := store.db.ExecContext(t.Context(),
		`UPDATE memories SET created_at = ?, updated_at = ? WHERE id = ?`,
		old, old, mem.ID)
	if err != nil {
		t.Fatalf("backdate exec: %v", err)
	}

	// Refresh the cache so subsequent List calls see the back-dated row.
	if err := store.loadMemoriesToCache(); err != nil {
		t.Fatalf("reload cache: %v", err)
	}
	mem.CreatedAt = old
	return mem
}

// seedNonDeadEnd creates a non-dead_end memory; StaleDeadEnds must skip it.
func seedNonDeadEnd(t *testing.T, store *Store, title string) {
	t.Helper()
	mem := &Memory{
		Title:      title,
		Content:    "regular memory entry " + title,
		Type:       TypeSemantic,
		Importance: 0.5,
	}
	if err := store.Store(t.Context(), mem); err != nil {
		t.Fatalf("Store non-dead_end: %v", err)
	}
}

func TestStaleDeadEnds_FiltersByAgeThreshold(t *testing.T) {
	store := newTestStore(t)
	old := seedDeadEndAge(t, store, "old-deadend", 14*30*24*time.Hour) // 14 months
	young := seedDeadEndAge(t, store, "young-deadend", 30*24*time.Hour) // 1 month
	seedNonDeadEnd(t, store, "decision")

	threshold := 12 * 30 * 24 * time.Hour // 12 months
	stale, err := store.StaleDeadEnds(t.Context(), threshold)
	if err != nil {
		t.Fatalf("StaleDeadEnds: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale dead_end, got %d", len(stale))
	}
	if stale[0].Memory.ID != old.ID {
		t.Errorf("expected old dead_end %s, got %s", old.ID, stale[0].Memory.ID)
	}
	if stale[0].Age < threshold {
		t.Errorf("stale entry age %s shorter than threshold %s", stale[0].Age, threshold)
	}
	_ = young // referenced for clarity; not in expected output
}

func TestStaleDeadEnds_ZeroThresholdReturnsEveryDeadEnd(t *testing.T) {
	store := newTestStore(t)
	a := seedDeadEndAge(t, store, "deadend-a", 60*24*time.Hour)
	b := seedDeadEndAge(t, store, "deadend-b", 5*24*time.Hour)
	seedNonDeadEnd(t, store, "decision")

	stale, err := store.StaleDeadEnds(t.Context(), 0)
	if err != nil {
		t.Fatalf("StaleDeadEnds(0): %v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("expected both dead_ends, got %d", len(stale))
	}
	// Sort order: oldest first.
	if stale[0].Memory.ID != a.ID || stale[1].Memory.ID != b.ID {
		t.Errorf("expected order [a, b] by age desc; got [%s, %s]", stale[0].Memory.ID, stale[1].Memory.ID)
	}
}

func TestStaleDeadEnds_ExcludesNonDeadEndTypes(t *testing.T) {
	store := newTestStore(t)
	seedNonDeadEnd(t, store, "decision-1")
	seedNonDeadEnd(t, store, "decision-2")

	stale, err := store.StaleDeadEnds(t.Context(), 0)
	if err != nil {
		t.Fatalf("StaleDeadEnds: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected zero results when only non-dead_end memories exist; got %d", len(stale))
	}
}

func TestStaleDeadEnds_EmptyStoreReturnsEmpty(t *testing.T) {
	store := newTestStore(t)
	stale, err := store.StaleDeadEnds(t.Context(), time.Hour)
	if err != nil {
		t.Fatalf("StaleDeadEnds: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected zero results on empty store; got %d", len(stale))
	}
}

func TestStaleDeadEnds_SortsOldestFirst(t *testing.T) {
	store := newTestStore(t)
	mid := seedDeadEndAge(t, store, "mid", 100*24*time.Hour)
	old := seedDeadEndAge(t, store, "old", 400*24*time.Hour)
	young := seedDeadEndAge(t, store, "young", 30*24*time.Hour)

	stale, err := store.StaleDeadEnds(t.Context(), 0)
	if err != nil {
		t.Fatalf("StaleDeadEnds: %v", err)
	}
	if len(stale) != 3 {
		t.Fatalf("got %d, want 3", len(stale))
	}
	if stale[0].Memory.ID != old.ID {
		t.Errorf("rank[0] = %s, want oldest %s", stale[0].Memory.ID, old.ID)
	}
	if stale[1].Memory.ID != mid.ID {
		t.Errorf("rank[1] = %s, want mid %s", stale[1].Memory.ID, mid.ID)
	}
	if stale[2].Memory.ID != young.ID {
		t.Errorf("rank[2] = %s, want young %s", stale[2].Memory.ID, young.ID)
	}
}
