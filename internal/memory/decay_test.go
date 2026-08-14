package memory

import (
	"testing"
	"time"
)

// T121: the type axis turned out to dominate the rate axis — at the same
// 30-day half-life, decaying every type scored Hit@5 0.1942 against 0.7043 for
// decaying `working` alone. These tests pin the mechanism that difference
// rests on.
func TestRecallDecayTypesLimitsWhatAges(t *testing.T) {
	store := newTestStore(t)
	store.SetRecallHalfLife(30)
	now := time.Now()

	aged := func(typ Type) *cachedMemory {
		return &cachedMemory{Type: typ, CreatedAt: now.AddDate(0, 0, -90)}
	}

	// Default: every type decays, which is what T68 shipped.
	for _, typ := range []Type{TypeWorking, TypeEpisodic, TypeProcedural, TypeSemantic} {
		if got := store.recallDecayMultiplier(aged(typ), now); got >= 1.0 {
			t.Errorf("default policy: %s multiplier = %.4f, want < 1", typ, got)
		}
	}

	store.SetRecallDecayTypes([]Type{TypeWorking})
	if got := store.recallDecayMultiplier(aged(TypeWorking), now); got >= 1.0 {
		t.Errorf("working multiplier = %.4f, want < 1 — it is in the decay set", got)
	}
	for _, typ := range []Type{TypeEpisodic, TypeProcedural, TypeSemantic} {
		if got := store.recallDecayMultiplier(aged(typ), now); got != 1.0 {
			t.Errorf("%s multiplier = %.4f, want exactly 1 — it is outside the decay set", typ, got)
		}
	}

	// An empty list is "no restriction", not "nothing decays" — otherwise a
	// cleared setting would silently switch decay off for everything.
	store.SetRecallDecayTypes(nil)
	if got := store.recallDecayMultiplier(aged(TypeSemantic), now); got >= 1.0 {
		t.Errorf("after clearing the list, semantic multiplier = %.4f, want < 1", got)
	}
}

// Evergreen entries were exempt before the type axis existed and stay exempt
// regardless of it — otherwise narrowing the decay set could accidentally
// widen what ages.
func TestEvergreenNeverDecaysWhateverTheTypeSet(t *testing.T) {
	store := newTestStore(t)
	store.SetRecallHalfLife(30)
	store.SetRecallDecayTypes([]Type{TypeWorking, TypeEpisodic, TypeProcedural, TypeSemantic})
	now := time.Now()

	cases := map[string]*cachedMemory{
		"canonical lifecycle": {Type: TypeWorking, Lifecycle: LifecycleCanonical, CreatedAt: now.AddDate(-1, 0, 0)},
		"canonical layer":     {Type: TypeWorking, KnowledgeLayer: "canonical", CreatedAt: now.AddDate(-1, 0, 0)},
		"character layer":     {Type: TypeWorking, SedimentLayer: LayerCharacter, CreatedAt: now.AddDate(-1, 0, 0)},
	}
	for name, m := range cases {
		if got := store.recallDecayMultiplier(m, now); got != 1.0 {
			t.Errorf("%s: multiplier = %.4f, want exactly 1", name, got)
		}
	}
}

func TestTypesFromStringsDropsBlanks(t *testing.T) {
	got := TypesFromStrings([]string{"working", " ", "episodic", ""})
	if len(got) != 2 || got[0] != TypeWorking || got[1] != TypeEpisodic {
		t.Errorf("TypesFromStrings = %v, want [working episodic]", got)
	}
}
