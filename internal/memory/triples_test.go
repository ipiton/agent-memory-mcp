package memory

import (
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func seedMemoryForTriples(t *testing.T, store *Store, content string) *Memory {
	t.Helper()
	mem := &Memory{
		Content:    content,
		Type:       TypeSemantic,
		Importance: 0.7,
	}
	if err := store.Store(t.Context(), mem); err != nil {
		t.Fatalf("seed Store: %v", err)
	}
	return mem
}

func TestAddTriple_AssignsIDAndCreatedAt(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "preflight blocks agent_spawn on failure")
	ctx := t.Context()

	tr := &Triple{
		Subject:  "forge_baseline_preflight",
		Relation: "blocks",
		Object:   "agent_spawn_on_failure",
		MemoryID: mem.ID,
	}
	if err := store.AddTriple(ctx, tr); err != nil {
		t.Fatalf("AddTriple: %v", err)
	}
	if tr.ID == "" {
		t.Fatalf("AddTriple did not assign ID")
	}
	if tr.CreatedAt.IsZero() {
		t.Fatalf("AddTriple did not assign CreatedAt")
	}
	if tr.LinkType != LinkTypeExtracted {
		t.Fatalf("LinkType default = %q, want %q", tr.LinkType, LinkTypeExtracted)
	}
	if tr.Weight != 1 {
		t.Fatalf("Weight default = %v, want 1", tr.Weight)
	}
}

func TestAddTriple_RejectsEmptyFields(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "test")
	ctx := t.Context()

	cases := []struct {
		name string
		t    Triple
	}{
		{"empty_subj", Triple{Subject: "", Relation: "rel", Object: "obj", MemoryID: mem.ID}},
		{"empty_rel", Triple{Subject: "subj", Relation: "", Object: "obj", MemoryID: mem.ID}},
		{"empty_obj", Triple{Subject: "subj", Relation: "rel", Object: "", MemoryID: mem.ID}},
		{"empty_memory_id", Triple{Subject: "subj", Relation: "rel", Object: "obj", MemoryID: ""}},
		{"all_whitespace", Triple{Subject: "  ", Relation: "rel", Object: "obj", MemoryID: mem.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := tc.t
			if err := store.AddTriple(ctx, &tr); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestAddTriple_ClampsWeightOutOfRange(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "weight clamp test")
	ctx := t.Context()

	t1 := &Triple{Subject: "a", Relation: "r", Object: "b", MemoryID: mem.ID, Weight: -0.5}
	if err := store.AddTriple(ctx, t1); err != nil {
		t.Fatalf("AddTriple negative weight: %v", err)
	}
	if t1.Weight != 1 { // clamped to 0 then defaulted to 1
		t.Errorf("negative weight got clamp+default = %v, want 1", t1.Weight)
	}

	t2 := &Triple{Subject: "a", Relation: "r", Object: "c", MemoryID: mem.ID, Weight: 5}
	if err := store.AddTriple(ctx, t2); err != nil {
		t.Fatalf("AddTriple oversize weight: %v", err)
	}
	if t2.Weight != 1 {
		t.Errorf("oversize weight clamp = %v, want 1", t2.Weight)
	}

	t3 := &Triple{Subject: "a", Relation: "r", Object: "d", MemoryID: mem.ID, Weight: 0.5}
	if err := store.AddTriple(ctx, t3); err != nil {
		t.Fatalf("AddTriple in-range: %v", err)
	}
	if t3.Weight != 0.5 {
		t.Errorf("in-range weight modified = %v, want 0.5", t3.Weight)
	}
}

func TestAddTriples_BatchTransaction(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "batch insert test")
	ctx := t.Context()

	batch := []*Triple{
		{Subject: "a", Relation: "r1", Object: "x", MemoryID: mem.ID},
		{Subject: "a", Relation: "r2", Object: "y", MemoryID: mem.ID},
		{Subject: "b", Relation: "r1", Object: "z", MemoryID: mem.ID},
	}
	if err := store.AddTriples(ctx, batch); err != nil {
		t.Fatalf("AddTriples: %v", err)
	}
	got, err := store.TriplesForMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("TriplesForMemory: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 triples, got %d", len(got))
	}
}

func TestAddTriples_RollsBackOnError(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "rollback test")
	ctx := t.Context()

	batch := []*Triple{
		{Subject: "a", Relation: "r", Object: "x", MemoryID: mem.ID},
		{Subject: "", Relation: "r", Object: "y", MemoryID: mem.ID}, // invalid → reject
	}
	if err := store.AddTriples(ctx, batch); err == nil {
		t.Fatalf("expected error from invalid triple, got nil")
	}
	got, err := store.TriplesForMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("TriplesForMemory: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("rollback failed: got %d triples, expected 0", len(got))
	}
}

func TestTriplesBySubjectAndObject(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "lookup test")
	ctx := t.Context()

	triples := []*Triple{
		{Subject: "auth_service", Relation: "depends_on", Object: "postgres", MemoryID: mem.ID, Weight: 0.9},
		{Subject: "auth_service", Relation: "depends_on", Object: "redis", MemoryID: mem.ID, Weight: 0.8},
		{Subject: "ml_service", Relation: "depends_on", Object: "postgres", MemoryID: mem.ID, Weight: 0.5},
	}
	if err := store.AddTriples(ctx, triples); err != nil {
		t.Fatalf("AddTriples: %v", err)
	}

	bySubj, err := store.TriplesBySubject(ctx, "auth_service")
	if err != nil {
		t.Fatalf("TriplesBySubject: %v", err)
	}
	if len(bySubj) != 2 {
		t.Errorf("TriplesBySubject auth_service: got %d, want 2", len(bySubj))
	}
	// Highest weight (postgres, 0.9) first per ORDER BY weight DESC.
	if bySubj[0].Weight < bySubj[len(bySubj)-1].Weight {
		t.Errorf("expected weight DESC, got %v", bySubj)
	}

	byObj, err := store.TriplesByObject(ctx, "postgres")
	if err != nil {
		t.Fatalf("TriplesByObject: %v", err)
	}
	if len(byObj) != 2 {
		t.Errorf("TriplesByObject postgres: got %d, want 2", len(byObj))
	}
}

func TestDeleteTriplesForMemory_ExplicitCall(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "explicit delete test")
	ctx := t.Context()

	triples := []*Triple{
		{Subject: "x", Relation: "r", Object: "1", MemoryID: mem.ID},
		{Subject: "x", Relation: "r", Object: "2", MemoryID: mem.ID},
	}
	if err := store.AddTriples(ctx, triples); err != nil {
		t.Fatalf("AddTriples: %v", err)
	}

	n, err := store.DeleteTriplesForMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("DeleteTriplesForMemory: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2", n)
	}
	got, _ := store.TriplesForMemory(ctx, mem.ID)
	if len(got) != 0 {
		t.Errorf("after delete: got %d triples, want 0", len(got))
	}
}

func TestStoreDelete_CascadesTriples(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "cascade test")
	other := seedMemoryForTriples(t, store, "unrelated memory")
	ctx := t.Context()

	if err := store.AddTriples(ctx, []*Triple{
		{Subject: "a", Relation: "r", Object: "x", MemoryID: mem.ID},
		{Subject: "a", Relation: "r", Object: "y", MemoryID: mem.ID},
		{Subject: "b", Relation: "r", Object: "z", MemoryID: other.ID},
	}); err != nil {
		t.Fatalf("AddTriples: %v", err)
	}

	// Delete the first memory — its triples must vanish, the unrelated
	// memory's triple must remain untouched.
	if err := store.Delete(ctx, mem.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	gone, _ := store.TriplesForMemory(ctx, mem.ID)
	if len(gone) != 0 {
		t.Errorf("cascade failed: %d triples remain for deleted memory", len(gone))
	}
	intact, _ := store.TriplesForMemory(ctx, other.ID)
	if len(intact) != 1 {
		t.Errorf("unrelated memory's triples: got %d, want 1", len(intact))
	}
}

func TestEnsureMemoryTriplesSchema_Idempotent(t *testing.T) {
	// Calling NewStore twice on the same DB path must not error — the
	// schema migration runs each time and CREATE TABLE IF NOT EXISTS /
	// CREATE INDEX IF NOT EXISTS keep it harmless.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store1, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("first NewStore: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("re-open NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	// Insert a triple after re-open to confirm schema exists.
	mem := seedMemoryForTriples(t, store2, "post re-open test")
	if err := store2.AddTriple(t.Context(), &Triple{
		Subject:  "post",
		Relation: "after",
		Object:   "reopen",
		MemoryID: mem.ID,
	}); err != nil {
		t.Fatalf("AddTriple after re-open: %v", err)
	}
}

func TestTriplesForMemory_OrdersByCreatedAt(t *testing.T) {
	store := newTestStore(t)
	mem := seedMemoryForTriples(t, store, "ordering test")
	ctx := t.Context()

	earlier := time.Now().Add(-time.Hour).UTC()
	later := time.Now().UTC()
	if err := store.AddTriple(ctx, &Triple{
		Subject: "a", Relation: "r", Object: "x", MemoryID: mem.ID, CreatedAt: later,
	}); err != nil {
		t.Fatalf("AddTriple later: %v", err)
	}
	if err := store.AddTriple(ctx, &Triple{
		Subject: "a", Relation: "r", Object: "y", MemoryID: mem.ID, CreatedAt: earlier,
	}); err != nil {
		t.Fatalf("AddTriple earlier: %v", err)
	}

	got, err := store.TriplesForMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("TriplesForMemory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Object != "y" {
		t.Errorf("expected earlier triple first; got order: %v -> %v", got[0].Object, got[1].Object)
	}
}
