package memory

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// T68 unit: the decay multiplier follows e^(-ln2/halflife * ageDays) with the
// expected boundary values, monotonicity, and evergreen/off exemptions. The
// method only reads an atomic, so a zero-value Store needs no DB.
func TestRecallDecayMultiplier(t *testing.T) {
	ms := &Store{}
	ms.SetRecallHalfLife(30)
	now := time.Date(2026, time.June, 4, 12, 0, 0, 0, time.UTC)

	aged := func(days float64, opts ...func(*cachedMemory)) *cachedMemory {
		cm := &cachedMemory{CreatedAt: now.Add(-time.Duration(days * 24 * float64(time.Hour)))}
		for _, o := range opts {
			o(cm)
		}
		return cm
	}

	if got := ms.recallDecayMultiplier(aged(0), now); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("age 0 multiplier = %v, want 1.0", got)
	}
	if got := ms.recallDecayMultiplier(aged(30), now); math.Abs(got-0.5) > 1e-3 {
		t.Fatalf("one half-life multiplier = %v, want ≈0.5", got)
	}

	// Strictly monotone: older → smaller.
	young := ms.recallDecayMultiplier(aged(10), now)
	old := ms.recallDecayMultiplier(aged(60), now)
	if !(old < young) {
		t.Fatalf("expected older (60d=%v) < younger (10d=%v)", old, young)
	}

	// Very old: bounded in (0,1], never NaN/negative.
	v := ms.recallDecayMultiplier(aged(100000), now)
	if math.IsNaN(v) || v < 0 || v > 1 {
		t.Fatalf("very old multiplier = %v, want in (0,1]", v)
	}

	// Future-dated (ageDays<0) clamps to 1.0, not >1.
	if got := ms.recallDecayMultiplier(aged(-5), now); got != 1.0 {
		t.Fatalf("future-dated multiplier = %v, want 1.0", got)
	}
}

// T68 unit: evergreen entries (canonical knowledge, character layer) and the
// off mode (half-life <= 0) never decay regardless of age.
func TestRecallDecayMultiplier_Exemptions(t *testing.T) {
	ms := &Store{}
	ms.SetRecallHalfLife(30)
	now := time.Date(2026, time.June, 4, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(-1, 0, 0)

	cases := map[string]*cachedMemory{
		"lifecycle canonical":       {CreatedAt: old, Lifecycle: LifecycleCanonical},
		"knowledge-layer canonical": {CreatedAt: old, KnowledgeLayer: "canonical"},
		"character layer":           {CreatedAt: old, SedimentLayer: LayerCharacter},
	}
	for name, cm := range cases {
		if got := ms.recallDecayMultiplier(cm, now); got != 1.0 {
			t.Errorf("%s: multiplier = %v, want 1.0 (evergreen)", name, got)
		}
	}

	off := &Store{}
	off.SetRecallHalfLife(0)
	if got := off.recallDecayMultiplier(&cachedMemory{CreatedAt: old}, now); got != 1.0 {
		t.Fatalf("decay off: multiplier = %v, want 1.0", got)
	}
}

// T68 integration: with decay enabled, a fresh memory ranks above an old one of
// equal relevance and importance; the default (no SetRecallHalfLife) is unchanged.
func TestRecall_TemporalDecay_FresherRanksHigher(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "memory.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.SetRecallHalfLife(30)
	ctx := context.Background()

	oldClock := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	newClock := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	const content = "deploy pipeline rollback runbook"

	store.SetClock(func() time.Time { return oldClock })
	if err := store.Store(ctx, &Memory{ID: "old", Content: content, Type: TypeSemantic, Importance: 0.5}); err != nil {
		t.Fatalf("store old: %v", err)
	}
	store.SetClock(func() time.Time { return newClock })
	if err := store.Store(ctx, &Memory{ID: "new", Content: content, Type: TypeSemantic, Importance: 0.5}); err != nil {
		t.Fatalf("store new: %v", err)
	}

	res, err := store.Recall(ctx, content, Filters{}, 0)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected at least one result")
	}
	if res[0].Memory.ID != "new" {
		t.Fatalf("expected fresh memory ranked first, got %s", res[0].Memory.ID)
	}
}
