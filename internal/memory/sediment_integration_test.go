package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	_ "modernc.org/sqlite"
)

// newSedimentTestStore returns a fresh Store on a temp DB, plus a cleanup.
func newSedimentTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memories.db")
	store, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, func() { _ = store.Close() }
}

func TestStore_DefaultsSedimentLayerToSurface(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	m := &Memory{
		Content: "hello",
		Type:    TypeSemantic,
	}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if m.SedimentLayer != string(LayerSurface) {
		t.Errorf("SedimentLayer=%q, want surface", m.SedimentLayer)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(LayerSurface) {
		t.Errorf("reloaded SedimentLayer=%q, want surface", got.SedimentLayer)
	}
}

func TestPromoteSediment_UpdatesLayer(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	m := &Memory{Content: "seed", Type: TypeSemantic}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	res, err := store.PromoteSediment(context.Background(), m.ID, LayerCharacter)
	if err != nil {
		t.Fatalf("PromoteSediment: %v", err)
	}
	if !res.Affected {
		t.Errorf("expected Affected=true")
	}
	if res.To != LayerCharacter {
		t.Errorf("To=%q, want character", res.To)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(LayerCharacter) {
		t.Errorf("reloaded layer=%q, want character", got.SedimentLayer)
	}
}

func TestPromoteSediment_NoOpWhenUnchanged(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	m := &Memory{Content: "seed", Type: TypeSemantic}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	res, err := store.PromoteSediment(context.Background(), m.ID, LayerSurface)
	if err != nil {
		t.Fatalf("PromoteSediment: %v", err)
	}
	if res.Affected {
		t.Errorf("Affected should be false for no-op promote")
	}
}

func TestPromoteSediment_RejectsInvalidLayer(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	m := &Memory{Content: "seed", Type: TypeSemantic}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if _, err := store.PromoteSediment(context.Background(), m.ID, "garbage"); err == nil {
		t.Error("expected error for invalid layer")
	}
}

func TestDemoteSediment_OneStep(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	m := &Memory{Content: "seed", Type: TypeSemantic}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := store.PromoteSediment(context.Background(), m.ID, LayerCharacter); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	res, err := store.DemoteSediment(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("DemoteSediment: %v", err)
	}
	if res.To != LayerSemantic {
		t.Errorf("DemoteSediment from character went to %q, want semantic", res.To)
	}
	if !res.Affected {
		t.Errorf("expected Affected=true")
	}
}

func TestDemoteSediment_NoopAtSurface(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	m := &Memory{Content: "seed", Type: TypeSemantic}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// default layer is surface
	res, err := store.DemoteSediment(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("DemoteSediment: %v", err)
	}
	if res.Affected {
		t.Errorf("Affected should be false at surface")
	}
}

// TestEnsureSchema_BackfillsExistingRows seeds a legacy DB (no sediment_layer
// column) and verifies that ensureMemorySchema adds the column and backfills
// each row with the correct derived layer.
func TestEnsureSchema_BackfillsExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// 1. Build a pre-T48 DB: create the memories table WITHOUT sediment_layer.
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	legacySchema := `
	CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY,
		content TEXT NOT NULL,
		type TEXT NOT NULL,
		title TEXT,
		tags TEXT,
		context TEXT,
		importance REAL DEFAULT 0.5,
		metadata TEXT,
		embedding BLOB,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		accessed_at DATETIME NOT NULL,
		access_count INTEGER DEFAULT 0,
		embedding_model TEXT,
		valid_from DATETIME,
		valid_until DATETIME,
		superseded_by TEXT,
		replaces TEXT,
		observed_at DATETIME
	);`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}

	now := time.Now()
	type row struct {
		id       string
		memType  Type
		metadata map[string]string
		wantLyr  SedimentLayer
	}
	seeds := []row{
		{"w1", TypeWorking, nil, LayerSurface},
		{"e1", TypeEpisodic, nil, LayerEpisodic},
		{"s1", TypeSemantic, nil, LayerSemantic},
		{"p1", TypeProcedural, nil, LayerSemantic},
		{"c1", TypeSemantic, map[string]string{MetadataKnowledgeLayer: "canonical"}, LayerCharacter},
	}
	for _, s := range seeds {
		mdBytes, _ := json.Marshal(s.metadata)
		if _, err := db.Exec(
			`INSERT INTO memories (id, content, type, title, tags, context, importance, metadata, embedding, created_at, updated_at, accessed_at, access_count) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.id, "content-"+s.id, string(s.memType), "title", "[]", "", 0.5, string(mdBytes), nil, now, now, now, 0,
		); err != nil {
			t.Fatalf("insert %s: %v", s.id, err)
		}
	}
	_ = db.Close()

	// 2. Open via NewStore (runs ensureMemorySchema → backfill).
	store, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore on legacy DB: %v", err)
	}
	defer func() { _ = store.Close() }()

	// 3. Verify each seeded row got the expected layer.
	for _, s := range seeds {
		got, err := store.Get(s.id)
		if err != nil {
			t.Fatalf("Get %s: %v", s.id, err)
		}
		if got.SedimentLayer != string(s.wantLyr) {
			t.Errorf("id=%s: SedimentLayer=%q, want %q", s.id, got.SedimentLayer, s.wantLyr)
		}
	}
}

// TestEnsureSchema_IdempotentMigration runs NewStore twice against the same
// DB and verifies no errors and no double-backfill (layers don't flip).
func TestEnsureSchema_IdempotentMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memories.db")
	store, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore #1: %v", err)
	}
	m := &Memory{Content: "hello", Type: TypeSemantic}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Manually promote to character to verify it survives re-open.
	if _, err := store.PromoteSediment(context.Background(), m.ID, LayerCharacter); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	_ = store.Close()

	store2, err := NewStore(path, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	defer func() { _ = store2.Close() }()

	got, err := store2.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(LayerCharacter) {
		t.Errorf("after re-open layer=%q, want character (idempotent migration must not overwrite)", got.SedimentLayer)
	}
}

// TestRunSedimentCycle_AutoAppliesSurfaceAging inserts an aged surface
// memory with AccessCount>=1 and verifies RunSedimentCycle promotes it.
func TestRunSedimentCycle_AutoAppliesSurfaceAging(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	ctx := context.Background()
	m := &Memory{Content: "aged", Type: TypeWorking, Context: "task-1", AccessCount: 5}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Force CreatedAt backwards and lift AccessCount in SQLite.
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	if _, err := store.db.Exec(
		`UPDATE memories SET created_at = ?, access_count = ? WHERE id = ?`,
		oldTime, 5, m.ID,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// Refresh the cache since we bypassed the write path.
	if err := store.loadMemoriesToCache(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	result, err := store.RunSedimentCycle(ctx, SedimentCycleConfig{})
	if err != nil {
		t.Fatalf("RunSedimentCycle: %v", err)
	}
	if result.AutoApplied != 1 {
		t.Errorf("AutoApplied=%d, want 1; transitions=%+v", result.AutoApplied, result.Transitions)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(LayerEpisodic) {
		t.Errorf("after cycle layer=%q, want episodic", got.SedimentLayer)
	}
}

// TestRunSedimentCycle_DryRunNoMutation verifies --dry-run produces
// transition proposals but no store writes.
func TestRunSedimentCycle_DryRunNoMutation(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	ctx := context.Background()
	m := &Memory{Content: "aged", Type: TypeWorking}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	if _, err := store.db.Exec(`UPDATE memories SET created_at = ?, access_count = ? WHERE id = ?`,
		oldTime, 5, m.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if err := store.loadMemoriesToCache(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	result, err := store.RunSedimentCycle(ctx, SedimentCycleConfig{DryRun: true})
	if err != nil {
		t.Fatalf("RunSedimentCycle: %v", err)
	}
	if result.AutoApplied != 1 {
		t.Errorf("AutoApplied=%d, want 1 proposal", result.AutoApplied)
	}
	if len(result.Transitions) == 0 {
		t.Errorf("expected transitions in dry-run, got none")
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(LayerSurface) {
		t.Errorf("dry-run mutated layer to %q (want surface)", got.SedimentLayer)
	}
}

// TestRunSedimentCycle_Idempotent runs the cycle twice and verifies the
// second run yields zero additional auto-applied/review-queued work.
func TestRunSedimentCycle_Idempotent(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	ctx := context.Background()
	m := &Memory{Content: "aged", Type: TypeWorking}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	oldTime := time.Now().Add(-10 * 24 * time.Hour)
	if _, err := store.db.Exec(`UPDATE memories SET created_at = ?, access_count = ? WHERE id = ?`,
		oldTime, 5, m.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if err := store.loadMemoriesToCache(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	res1, err := store.RunSedimentCycle(ctx, SedimentCycleConfig{})
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if res1.AutoApplied != 1 {
		t.Fatalf("cycle 1 AutoApplied=%d, want 1", res1.AutoApplied)
	}
	res2, err := store.RunSedimentCycle(ctx, SedimentCycleConfig{})
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if res2.AutoApplied != 0 || res2.ReviewQueued != 0 {
		t.Errorf("cycle 2 should be noop: AutoApplied=%d, ReviewQueued=%d", res2.AutoApplied, res2.ReviewQueued)
	}
}

// TestRunSedimentCycle_QueuesReviewForSemanticCanonical puts a semantic
// memory with canonical metadata into the store; the cycle should emit a
// review-queue item for semantic→character (non-auto).
func TestRunSedimentCycle_QueuesReviewForSemanticCanonical(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()

	ctx := context.Background()
	m := &Memory{
		Content: "knowledge",
		Type:    TypeSemantic,
		Context: "proj",
		Metadata: map[string]string{
			MetadataKnowledgeLayer: "canonical",
		},
	}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Canonical metadata causes BackfillSedimentLayer to emit character on
	// a fresh row. Since Store() defaults to surface (from Validate), we
	// first force the layer to semantic to trigger the rule.
	if _, err := store.PromoteSediment(ctx, m.ID, LayerSemantic); err != nil {
		t.Fatalf("pre-promote: %v", err)
	}

	result, err := store.RunSedimentCycle(ctx, SedimentCycleConfig{})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if result.ReviewQueued != 1 {
		t.Errorf("ReviewQueued=%d, want 1; transitions=%+v", result.ReviewQueued, result.Transitions)
	}
	// Target memory must not have been auto-promoted.
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SedimentLayer != string(LayerSemantic) {
		t.Errorf("non-auto transition must not mutate target; got %q", got.SedimentLayer)
	}

	// Ensure the queue item exists and is discoverable.
	items, err := store.List(ctx, Filters{Context: "proj", Type: TypeWorking}, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, it := range items {
		if IsReviewQueueMemory(it) && it.Metadata[MetadataReviewSource] == ReviewSourceSedimentCycle {
			found = true
			if it.Metadata[MetadataReviewTargetMemoryID] != m.ID {
				t.Errorf("review_target_memory_id=%q, want %q", it.Metadata[MetadataReviewTargetMemoryID], m.ID)
			}
			if it.Metadata[MetadataReviewTargetLayer] != string(LayerCharacter) {
				t.Errorf("review_target_layer=%q, want character", it.Metadata[MetadataReviewTargetLayer])
			}
		}
	}
	if !found {
		t.Errorf("no review-queue item found for sediment cycle")
	}
}

// TestRecallSedimentBoost_CharacterAlwaysIncluded verifies that a character
// memory surfaces in Recall even when the query is unrelated — when the
// feature flag is ON.
func TestRecallSedimentBoost_CharacterAlwaysIncluded(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()
	store.SetSedimentEnabled(true)

	ctx := context.Background()
	char := &Memory{Content: "identity fact X", Type: TypeSemantic, Title: "identity"}
	if err := store.Store(ctx, char); err != nil {
		t.Fatalf("Store char: %v", err)
	}
	if _, err := store.PromoteSediment(ctx, char.ID, LayerCharacter); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Some semantic neighbors that don't match "telephone"
	for i := 0; i < 3; i++ {
		m := &Memory{Content: "unrelated filler content", Type: TypeSemantic}
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store filler: %v", err)
		}
		if _, err := store.PromoteSediment(ctx, m.ID, LayerSemantic); err != nil {
			t.Fatalf("Promote filler: %v", err)
		}
	}

	results, err := store.Recall(ctx, "telephone directory", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Memory.ID == char.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("character memory absent from Recall results (flag=on); got %d results", len(results))
	}
}

// TestRecallSedimentBoost_SurfaceExcludedOutsideContext verifies surface
// memories are excluded when filters.Context differs from their Context.
func TestRecallSedimentBoost_SurfaceExcludedOutsideContext(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()
	store.SetSedimentEnabled(true)

	ctx := context.Background()
	surfaceMem := &Memory{Content: "scratch note keyword", Type: TypeWorking, Context: "task-A"}
	if err := store.Store(ctx, surfaceMem); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Call recall from a DIFFERENT context — surface should be excluded.
	results, err := store.Recall(ctx, "scratch", Filters{Context: "task-B"}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, r := range results {
		if r.Memory.ID == surfaceMem.ID {
			t.Errorf("surface memory leaked across Context boundaries (flag=on)")
		}
	}

	// And empty filter.Context: still excluded when flag is on.
	results2, err := store.Recall(ctx, "scratch", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	for _, r := range results2 {
		if r.Memory.ID == surfaceMem.ID {
			t.Errorf("surface memory leaked with empty filter.Context (flag=on)")
		}
	}

	// But inside its own Context it's visible.
	results3, err := store.Recall(ctx, "scratch", Filters{Context: "task-A"}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	found := false
	for _, r := range results3 {
		if r.Memory.ID == surfaceMem.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("surface memory not visible within its own Context (flag=on)")
	}
}

// TestRecallSedimentBoost_DisabledFlagPreservesOldBehaviour verifies that
// when SedimentEnabled=false (the default), neither layer gating nor layer
// boosts apply — the old Recall behaviour is preserved byte-for-byte.
func TestRecallSedimentBoost_DisabledFlagPreservesOldBehaviour(t *testing.T) {
	store, cleanup := newSedimentTestStore(t)
	defer cleanup()
	// Flag OFF (default).

	ctx := context.Background()
	surfaceMem := &Memory{Content: "scratch note", Type: TypeWorking, Context: "task-A"}
	if err := store.Store(ctx, surfaceMem); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// With flag off, surface is reachable even from a different Context.
	results, err := store.Recall(ctx, "scratch", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Memory.ID == surfaceMem.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("with flag OFF, surface memory must be reachable (old behaviour)")
	}
}
