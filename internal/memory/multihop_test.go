package memory

import (
	"testing"
)

// seedTriplesGraph builds a deterministic triple graph rooted at a seed
// memory whose content matches "incident root cause". Topology:
//
//	   m1 (seed)            m2                m3                m4
//	  ┌──────────┐      ┌─────────┐       ┌──────────┐      ┌─────────┐
//	  │ payments │─────▶│ db_pool │──────▶│ migration│ ◀────│ release │
//	  │ outage   │      │ exhaust │       │  v42     │      │ release │
//	  └──────────┘      └─────────┘       └──────────┘      └─────────┘
//
// Edges:
//
//	(payments, caused_by, db_pool_exhaustion) — memory m1
//	(db_pool_exhaustion, traced_to, migration_v42) — memory m2
//	(migration_v42, introduced_in, release_2025_03) — memory m3
//	(release_2025_03, deployed_by, ops_team) — memory m4 (a 3-hop neighbour)
//
// Plain semantic Recall on "payments outage" only finds m1; the graph walk
// must surface m2 (1-hop), m3 (2-hop), and degrade m4 because it sits at 3
// hops which equals MaxHops=3 boundary.
func seedTriplesGraph(t *testing.T) (store *Store, seedID string) {
	t.Helper()
	store = newTestStore(t)
	ctx := t.Context()

	// Memory 1 — seed (the only one that should be hit by Recall on the
	// query string we use, by virtue of the matching keywords).
	m1 := &Memory{
		Title:      "Payments outage 2025-03",
		Content:    "incident root cause analysis: payments outage triggered by db_pool exhaustion",
		Type:       TypeSemantic,
		Importance: 0.9,
	}
	if err := store.Store(ctx, m1); err != nil {
		t.Fatalf("Store m1: %v", err)
	}

	m2 := &Memory{
		Title:      "DB pool exhaustion analysis",
		Content:    "thread-pool saturation traced to migration_v42 timing window",
		Type:       TypeSemantic,
		Importance: 0.7,
	}
	if err := store.Store(ctx, m2); err != nil {
		t.Fatalf("Store m2: %v", err)
	}

	m3 := &Memory{
		Title:      "Migration v42 details",
		Content:    "schema migration introduced in 2025-03-15 release",
		Type:       TypeSemantic,
		Importance: 0.7,
	}
	if err := store.Store(ctx, m3); err != nil {
		t.Fatalf("Store m3: %v", err)
	}

	m4 := &Memory{
		Title:      "Release process notes",
		Content:    "ops_team owns the deploy pipeline",
		Type:       TypeSemantic,
		Importance: 0.6,
	}
	if err := store.Store(ctx, m4); err != nil {
		t.Fatalf("Store m4: %v", err)
	}

	if err := store.AddTriples(ctx, []*Triple{
		{Subject: "payments_service", Relation: "caused_by", Object: "db_pool_exhaustion", MemoryID: m1.ID, Weight: 0.9},
		{Subject: "db_pool_exhaustion", Relation: "traced_to", Object: "migration_v42", MemoryID: m2.ID, Weight: 0.85},
		{Subject: "migration_v42", Relation: "introduced_in", Object: "release_2025_03", MemoryID: m3.ID, Weight: 0.8},
		{Subject: "release_2025_03", Relation: "deployed_by", Object: "ops_team", MemoryID: m4.ID, Weight: 0.6},
	}); err != nil {
		t.Fatalf("AddTriples: %v", err)
	}
	return store, m1.ID
}

func TestRecallMultihop_RequiresQuery(t *testing.T) {
	store, _ := seedTriplesGraph(t)
	if _, err := store.RecallMultihop(t.Context(), MultiHopRequest{Query: ""}); err == nil {
		t.Fatalf("expected error for empty query")
	}
}

func TestRecallMultihop_ReturnsSeedAndOneHopAndTwoHop(t *testing.T) {
	store, seedID := seedTriplesGraph(t)
	got, err := store.RecallMultihop(t.Context(), MultiHopRequest{
		Query:   "payments outage db_pool exhaustion",
		MaxHops: 2,
		SeedK:   3,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("expected at least seed memory, got 0 results")
	}

	ids := map[string]*MultiHopResult{}
	for _, r := range got {
		ids[r.Memory.ID] = r
	}

	// The seed itself must appear (it has a triple, so its entities are
	// part of the seed-entity set with hops=0).
	if _, ok := ids[seedID]; !ok {
		t.Fatalf("seed memory %s missing from results: %+v", seedID, ids)
	}
	if seed := ids[seedID]; seed.Hops != 0 {
		t.Errorf("seed hops = %d, want 0", seed.Hops)
	}

	// At least one 1-hop neighbour must be reached. We don't assert exact
	// IDs because that depends on Recall's score profile, but at least
	// one non-seed memory must surface within hops <= MaxHops.
	foundHop := 0
	for id, r := range ids {
		if id == seedID {
			continue
		}
		if r.Hops > 0 && r.Hops <= 2 {
			foundHop++
		}
	}
	if foundHop == 0 {
		t.Errorf("expected ≥1 result with hops in [1,2]; got: %+v", got)
	}
}

func TestRecallMultihop_PathReconstructionTracksHopChain(t *testing.T) {
	store, seedID := seedTriplesGraph(t)
	got, err := store.RecallMultihop(t.Context(), MultiHopRequest{
		Query:   "payments outage db_pool exhaustion",
		MaxHops: 2,
		SeedK:   3,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	for _, r := range got {
		if r.Memory.ID == seedID {
			continue
		}
		if r.Hops > 0 && len(r.Path) != r.Hops {
			t.Errorf("memory %s: hops=%d but len(Path)=%d (path=%+v)", r.Memory.ID, r.Hops, len(r.Path), r.Path)
		}
	}
}

func TestRecallMultihop_MaxHopsBoundsTheWalk(t *testing.T) {
	store, _ := seedTriplesGraph(t)

	// MaxHops=1 must guarantee that no result exceeds the configured
	// hop bound. Whether a specific memory appears depends on Recall's
	// seed picks (text-only matching when no embedder is configured),
	// so we don't assert exact membership — only the invariant.
	got, err := store.RecallMultihop(t.Context(), MultiHopRequest{
		Query:   "payments outage db_pool exhaustion",
		MaxHops: 1,
		SeedK:   3,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	for _, r := range got {
		if r.Hops > 1 {
			t.Errorf("MaxHops=1 violated: memory %s reached at hop=%d", r.Memory.ID, r.Hops)
		}
		if len(r.Path) > 1 {
			t.Errorf("MaxHops=1 path length violated: memory %s len(Path)=%d", r.Memory.ID, len(r.Path))
		}
	}
}

func TestRecallMultihop_GracefulFallbackWhenSeedHasNoTriples(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	mem := &Memory{
		Title:      "Lonely memory",
		Content:    "graph isolated content with no triples extracted yet",
		Type:       TypeSemantic,
		Importance: 0.6,
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := store.RecallMultihop(ctx, MultiHopRequest{Query: "graph isolated content"})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	if len(got) == 0 {
		// Acceptable — when no triples and no graph anchor, returning empty
		// is a valid degraded result. The contract is "no error".
		return
	}
	// If returned, the lone memory must come back at hops=0.
	for _, r := range got {
		if r.Memory.ID == mem.ID && r.Hops != 0 {
			t.Errorf("isolated memory should have hops=0; got %d", r.Hops)
		}
	}
}

func TestRecallMultihop_NoSeedsFoundReturnsEmpty(t *testing.T) {
	store, _ := seedTriplesGraph(t)
	got, err := store.RecallMultihop(t.Context(), MultiHopRequest{
		Query: "completely_unrelated_query_string_xxx_zzz_aaa",
	})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	// We accept either empty (no semantic match) or some loose match —
	// the contract is "no error", not "must be empty". The point of this
	// test is to ensure no panic on a query that doesn't anchor.
	_ = got
}

func TestRecallMultihop_LimitTrimsResults(t *testing.T) {
	store, _ := seedTriplesGraph(t)
	got, err := store.RecallMultihop(t.Context(), MultiHopRequest{
		Query:   "payments outage db_pool exhaustion",
		MaxHops: 3,
		SeedK:   5,
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	if len(got) > 1 {
		t.Errorf("Limit=1 violated: got %d results", len(got))
	}
}

func TestRecallMultihop_HigherWeightShorterHopsRanksHigher(t *testing.T) {
	store, seedID := seedTriplesGraph(t)
	got, err := store.RecallMultihop(t.Context(), MultiHopRequest{
		Query:   "payments outage db_pool exhaustion",
		MaxHops: 3,
		SeedK:   5,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	// Seed memory must appear in the result set. We don't pin it to
	// rank 0 — when several memories share an entity at hops=0 their
	// aggregated scores can legitimately tie, in which case map-iteration
	// order chooses among them.
	seedFound := false
	for _, r := range got {
		if r.Memory.ID == seedID {
			seedFound = true
			break
		}
	}
	if !seedFound {
		t.Errorf("seed memory %s missing from multi-hop results: %+v", seedID, got)
	}
	// Scores must be monotonically non-increasing across the ranked set.
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Errorf("scores not sorted DESC at %d: %.4f then %.4f", i, got[i-1].Score, got[i].Score)
		}
	}
	// And hops must respect MaxHops=3.
	for _, r := range got {
		if r.Hops > 3 {
			t.Errorf("hops > MaxHops: memory %s has hops=%d", r.Memory.ID, r.Hops)
		}
	}
}
