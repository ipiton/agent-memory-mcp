package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTemporalTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := NewStore(dbPath, nil, nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestTemporalColumnsMigration(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()

	// Store a memory — should work with new columns.
	now := time.Now().UTC()
	m := &Memory{
		Content:    "Test temporal",
		Type:       TypeSemantic,
		Title:      "Temporal test",
		Importance: 0.8,
		ValidFrom:  &now,
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatal(err)
	}

	// Get it back and check temporal fields.
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidFrom == nil {
		t.Error("expected valid_from to be set")
	}
	if got.ValidUntil != nil {
		t.Error("expected valid_until to be nil")
	}
}

func TestRecallAsOf(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	// Entry valid Jan-Jun 2025.
	m1 := &Memory{
		Content:    "Use gorilla/mux for routing",
		Type:       TypeSemantic,
		Title:      "Routing decision v1",
		Importance: 0.8,
		ValidFrom:  &t1,
		ValidUntil: &t2,
	}
	if err := store.Store(ctx, m1); err != nil {
		t.Fatal(err)
	}

	// Entry valid from Jun 2025 onward.
	m2 := &Memory{
		Content:    "Use chi router for routing",
		Type:       TypeSemantic,
		Title:      "Routing decision v2",
		Importance: 0.9,
		ValidFrom:  &t2,
		Replaces:   m1.ID,
	}
	if err := store.Store(ctx, m2); err != nil {
		t.Fatal(err)
	}

	// Query at March 2025 — should find v1 only.
	march := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	results, err := store.RecallAsOf(ctx, "routing", march, Filters{}, 10)
	if err != nil {
		t.Fatal(err)
	}

	foundV1, foundV2 := false, false
	for _, r := range results {
		if r.Memory.ID == m1.ID {
			foundV1 = true
		}
		if r.Memory.ID == m2.ID {
			foundV2 = true
		}
	}
	if !foundV1 {
		t.Error("expected v1 at March 2025")
	}
	if foundV2 {
		t.Error("did not expect v2 at March 2025")
	}

	// Query at October 2025 — should find v2 only.
	oct := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	results2, err := store.RecallAsOf(ctx, "routing", oct, Filters{}, 10)
	if err != nil {
		t.Fatal(err)
	}

	foundV1, foundV2 = false, false
	for _, r := range results2 {
		if r.Memory.ID == m1.ID {
			foundV1 = true
		}
		if r.Memory.ID == m2.ID {
			foundV2 = true
		}
	}
	if foundV1 {
		t.Error("did not expect v1 at October 2025")
	}
	if !foundV2 {
		t.Error("expected v2 at October 2025")
	}

	// Query at future — should find v2 (no valid_until).
	future := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	results3, _ := store.RecallAsOf(ctx, "routing", future, Filters{}, 10)
	foundV2 = false
	for _, r := range results3 {
		if r.Memory.ID == m2.ID {
			foundV2 = true
		}
	}
	if !foundV2 {
		t.Error("expected v2 in future")
	}
}

func TestSupersessionChain(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()

	// Create two entries — v1 and v2.
	m1 := &Memory{
		Content:    "Old approach",
		Type:       TypeSemantic,
		Title:      "Approach v1",
		Importance: 0.8,
	}
	if err := store.Store(ctx, m1); err != nil {
		t.Fatal(err)
	}

	m2 := &Memory{
		Content:    "New approach",
		Type:       TypeSemantic,
		Title:      "Approach v2",
		Importance: 0.9,
	}
	if err := store.Store(ctx, m2); err != nil {
		t.Fatal(err)
	}

	// Mark m1 as outdated, superseded by m2.
	_, err := store.MarkOutdated(ctx, m1.ID, "replaced by v2", m2.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Check m1 has valid_until and superseded_by.
	got1, _ := store.Get(m1.ID)
	if got1.ValidUntil == nil {
		t.Error("expected valid_until on superseded entry")
	}
	if got1.SupersededBy != m2.ID {
		t.Errorf("expected superseded_by=%s, got %s", m2.ID, got1.SupersededBy)
	}

	// Check m2 has valid_from and replaces.
	got2, _ := store.Get(m2.ID)
	if got2.ValidFrom == nil {
		t.Error("expected valid_from on superseding entry")
	}
	if got2.Replaces != m1.ID {
		t.Errorf("expected replaces=%s, got %s", m1.ID, got2.Replaces)
	}
}

func TestKnowledgeTimeline(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	_ = store.Store(ctx, &Memory{
		Content:    "First routing decision",
		Type:       TypeSemantic,
		Title:      "Routing v1",
		Importance: 0.8,
		ValidFrom:  &t1,
		ValidUntil: &t2,
	})
	_ = store.Store(ctx, &Memory{
		Content:    "Second routing decision",
		Type:       TypeSemantic,
		Title:      "Routing v2",
		Importance: 0.9,
		ValidFrom:  &t2,
	})

	entries, err := store.KnowledgeTimeline(ctx, "routing", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 timeline entries, got %d", len(entries))
	}

	// Should be sorted by valid_from (oldest first).
	if entries[0].Title != "Routing v1" {
		t.Errorf("expected first entry to be v1, got %s", entries[0].Title)
	}
	if entries[1].Title != "Routing v2" {
		t.Errorf("expected second entry to be v2, got %s", entries[1].Title)
	}
}

func TestSetTemporalFields(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()

	m := &Memory{
		Content:    "Temporal field test",
		Type:       TypeSemantic,
		Importance: 0.5,
	}
	_ = store.Store(ctx, m)

	now := time.Now().UTC()
	if err := store.SetTemporalFields(ctx, m.ID, &now, nil, "other-id", ""); err != nil {
		t.Fatal(err)
	}

	got, _ := store.Get(m.ID)
	if got.ValidFrom == nil {
		t.Error("expected valid_from")
	}
	if got.SupersededBy != "other-id" {
		t.Errorf("expected superseded_by=other-id, got %s", got.SupersededBy)
	}
}

func TestTemporalFieldsBackwardCompatible(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()

	// Store without temporal fields — should work fine.
	m := &Memory{
		Content:    "No temporal fields",
		Type:       TypeSemantic,
		Importance: 0.5,
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidFrom != nil {
		t.Error("expected nil valid_from")
	}
	if got.ValidUntil != nil {
		t.Error("expected nil valid_until")
	}
	if got.SupersededBy != "" {
		t.Error("expected empty superseded_by")
	}
	if got.Replaces != "" {
		t.Error("expected empty replaces")
	}
}
