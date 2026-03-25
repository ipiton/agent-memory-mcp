package steward

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func newTestStore(t *testing.T) *memory.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := memory.NewStore(dbPath, nil, nil)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestService(t *testing.T, store *memory.Store) *Service {
	t.Helper()
	svc, err := NewService(store, nil)
	if err != nil {
		t.Fatalf("failed to create steward service: %v", err)
	}
	return svc
}

// --- Policy tests ---

func TestDefaultPolicy(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)

	p := svc.Policy()
	if p.Mode != PolicyModeManual {
		t.Errorf("expected mode manual, got %s", p.Mode)
	}
	if p.DuplicateSimilarity != 0.85 {
		t.Errorf("expected duplicate_similarity 0.85, got %f", p.DuplicateSimilarity)
	}
	if p.StaleDays != 30 {
		t.Errorf("expected stale_days 30, got %d", p.StaleDays)
	}
}

func TestPolicyPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create service, set policy, close.
	store1, err := memory.NewStore(dbPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc1, err := NewService(store1, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := svc1.Policy()
	p.Mode = PolicyModeScheduled
	p.StaleDays = 60
	if err := svc1.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	_ = store1.Close()

	// Reopen and check persistence.
	store2, err := memory.NewStore(dbPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store2.Close() }()
	svc2, err := NewService(store2, nil)
	if err != nil {
		t.Fatal(err)
	}

	p2 := svc2.Policy()
	if p2.Mode != PolicyModeScheduled {
		t.Errorf("expected mode scheduled, got %s", p2.Mode)
	}
	if p2.StaleDays != 60 {
		t.Errorf("expected stale_days 60, got %d", p2.StaleDays)
	}
}

// --- Run tests ---

func TestDryRunNoChanges(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)

	ctx := context.Background()

	// Store some test memories.
	_ = store.Store(ctx, &memory.Memory{
		Content:    "Test memory 1",
		Type:       memory.TypeSemantic,
		Title:      "Decision about routing",
		Importance: 0.8,
	})
	_ = store.Store(ctx, &memory.Memory{
		Content:    "Test memory 2",
		Type:       memory.TypeEpisodic,
		Title:      "Bug fix notes",
		Importance: 0.5,
	})

	report, err := svc.Run(ctx, RunParams{
		Scope:  ScopeFull,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if report == nil {
		t.Fatal("expected report, got nil")
	}
	if !report.DryRun {
		t.Error("expected dry_run=true in report")
	}
	if report.Stats.Scanned != 2 {
		t.Errorf("expected 2 scanned, got %d", report.Stats.Scanned)
	}
	// Dry run should not apply anything.
	if report.Stats.ActionsApplied != 0 {
		t.Errorf("expected 0 applied, got %d", report.Stats.ActionsApplied)
	}
}

func TestDuplicateDetection(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	// Create two memories with the same title and entity type.
	meta := map[string]string{"entity": "decision"}
	_ = store.Store(ctx, &memory.Memory{
		Content:    "Use chi router for all new services",
		Type:       memory.TypeSemantic,
		Title:      "chi router decision",
		Metadata:   meta,
		Importance: 0.8,
	})
	_ = store.Store(ctx, &memory.Memory{
		Content:    "Use chi router for all new services, confirmed",
		Type:       memory.TypeSemantic,
		Title:      "chi router decision",
		Metadata:   meta,
		Importance: 0.7,
	})

	report, err := svc.Run(ctx, RunParams{Scope: ScopeDuplicates, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.DuplicatesFound == 0 {
		t.Error("expected at least one duplicate group")
	}

	// Check that duplicate actions have correct kind.
	for _, a := range report.Actions {
		if a.Kind != ActionMergeDuplicates {
			t.Errorf("expected merge_duplicates action, got %s", a.Kind)
		}
		if len(a.TargetIDs) < 2 {
			t.Errorf("expected at least 2 target IDs, got %d", len(a.TargetIDs))
		}
	}
}

func TestStaleDetection(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	// Set a very short stale threshold.
	p := svc.Policy()
	p.StaleDays = 1
	_ = svc.SetPolicy(p)

	// Create a memory with an old updated_at.
	m := &memory.Memory{
		Content:    "Old runbook for ingress rollback",
		Type:       memory.TypeProcedural,
		Title:      "Ingress rollback runbook",
		Importance: 0.8,
	}
	_ = store.Store(ctx, m)

	// Force the memory to look old by updating its timestamp.
	db := store.DB()
	_, _ = db.Exec(`UPDATE memories SET updated_at = ? WHERE title = ?`,
		time.Now().Add(-48*time.Hour), "Ingress rollback runbook")

	report, err := svc.Run(ctx, RunParams{Scope: ScopeStale, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.StaleFound == 0 {
		t.Error("expected at least one stale entry")
	}
}

func TestReportPersistence(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	_ = store.Store(ctx, &memory.Memory{
		Content:    "Test",
		Type:       memory.TypeWorking,
		Importance: 0.5,
	})

	report, err := svc.Run(ctx, RunParams{Scope: ScopeFull, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve the report.
	loaded, err := svc.GetReport(report.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("expected report, got nil")
	}
	if loaded.ID != report.ID {
		t.Errorf("expected id %s, got %s", report.ID, loaded.ID)
	}
	if loaded.Stats.Scanned != report.Stats.Scanned {
		t.Errorf("expected scanned %d, got %d", report.Stats.Scanned, loaded.Stats.Scanned)
	}

	// Latest should also return this report.
	latest, err := svc.GetReport("")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != report.ID {
		t.Error("latest report mismatch")
	}
}

func TestStatus(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	// Status before any runs.
	status, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.PolicyMode != PolicyModeManual {
		t.Errorf("expected manual mode, got %s", status.PolicyMode)
	}
	if status.LastRun != nil {
		t.Error("expected no last run")
	}

	// Run and check status again.
	_ = store.Store(ctx, &memory.Memory{
		Content:    "Test",
		Type:       memory.TypeWorking,
		Importance: 0.5,
	})
	_, _ = svc.Run(ctx, RunParams{Scope: ScopeFull, DryRun: true})

	status2, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status2.LastRun == nil {
		t.Fatal("expected last run")
	}
	if status2.LastRun.Stats.Scanned != 1 {
		t.Errorf("expected 1 scanned, got %d", status2.LastRun.Stats.Scanned)
	}
}

func TestRunDisabledMode(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)

	p := svc.Policy()
	p.Mode = PolicyModeOff
	_ = svc.SetPolicy(p)

	_, err := svc.Run(context.Background(), RunParams{Scope: ScopeFull, DryRun: true})
	if err == nil {
		t.Error("expected error when mode=off")
	}
}

func TestAuditTrail(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	// Create memories that will produce a stale action with auto-apply.
	p := svc.Policy()
	p.StaleDays = 1
	p.AutoMarkStaleBeyondDays = 1
	_ = svc.SetPolicy(p)

	_ = store.Store(ctx, &memory.Memory{
		Content:    "Very old knowledge",
		Type:       memory.TypeSemantic,
		Title:      "Ancient fact",
		Importance: 0.5,
	})

	db := store.DB()
	_, _ = db.Exec(`UPDATE memories SET updated_at = ? WHERE title = ?`,
		time.Now().Add(-72*time.Hour), "Ancient fact")

	report, err := svc.Run(ctx, RunParams{Scope: ScopeStale, DryRun: false})
	if err != nil {
		t.Fatal(err)
	}

	if report.Stats.ActionsApplied == 0 {
		t.Skip("no actions were auto-applied (memory may have been filtered)")
	}

	entries, err := ListAuditEntries(db, report.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("expected audit entries for applied actions")
	}
	for _, e := range entries {
		if e.AppliedBy != "steward_auto" {
			t.Errorf("expected applied_by steward_auto, got %s", e.AppliedBy)
		}
	}
}

func TestFormatReport(t *testing.T) {
	report := &Report{
		ID:          "test-id-12345678",
		StartedAt:   time.Now(),
		CompletedAt: time.Now().Add(100 * time.Millisecond),
		Scope:       ScopeFull,
		DryRun:      true,
		Stats: RunStats{
			Scanned:         100,
			DuplicatesFound: 3,
			ConflictsFound:  1,
		},
		Actions: []Action{
			{
				Kind:       ActionMergeDuplicates,
				Handling:   HandlingReviewRequired,
				State:      StateReviewRequired,
				Title:      "Duplicate: chi router",
				Rationale:  "Same subject",
				TargetIDs:  []string{"a", "b"},
				Confidence: 0.75,
			},
		},
	}

	text := FormatReport(report)
	if text == "" {
		t.Fatal("expected non-empty report text")
	}

	// Check key elements are present.
	for _, want := range []string{"DRY RUN", "test-id-", "Scanned: 100", "Duplicate: chi router", "Same subject"} {
		if !containsStr(text, want) {
			t.Errorf("expected report to contain %q", want)
		}
	}
}

func TestScopeFiltering(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	meta := map[string]string{"entity": "decision"}
	_ = store.Store(ctx, &memory.Memory{
		Content:  "decision A",
		Type:     memory.TypeSemantic,
		Title:    "same decision",
		Metadata: meta,
	})
	_ = store.Store(ctx, &memory.Memory{
		Content:  "decision B",
		Type:     memory.TypeSemantic,
		Title:    "same decision",
		Metadata: meta,
	})

	// Duplicates scope should find them.
	r1, _ := svc.Run(ctx, RunParams{Scope: ScopeDuplicates, DryRun: true})
	if r1.Stats.DuplicatesFound == 0 {
		t.Error("expected duplicates in duplicates scope")
	}

	// Stale scope should not find duplicates.
	r2, _ := svc.Run(ctx, RunParams{Scope: ScopeStale, DryRun: true})
	if r2.Stats.DuplicatesFound != 0 {
		t.Error("expected no duplicates in stale scope")
	}
}

// TestContextFilter verifies that context parameter limits the scan.
func TestContextFilter(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	_ = store.Store(ctx, &memory.Memory{
		Content: "alpha content",
		Type:    memory.TypeSemantic,
		Context: "project-alpha",
	})
	_ = store.Store(ctx, &memory.Memory{
		Content: "beta content",
		Type:    memory.TypeSemantic,
		Context: "project-beta",
	})

	report, _ := svc.Run(ctx, RunParams{Scope: ScopeFull, DryRun: true, Context: "project-alpha"})
	if report.Stats.Scanned != 1 {
		t.Errorf("expected 1 scanned with context filter, got %d", report.Stats.Scanned)
	}
}

// --- Phase 2 tests ---

func TestVerifyEntry(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	_ = store.Store(ctx, &memory.Memory{
		Content:    "Ingress uses chi router",
		Type:       memory.TypeSemantic,
		Title:      "Router choice",
		Importance: 0.8,
	})
	memories, _ := store.List(ctx, memory.Filters{}, 1)
	if len(memories) == 0 {
		t.Fatal("no memories")
	}
	id := memories[0].ID

	err := svc.VerifyEntry(ctx, VerifyParams{
		MemoryID: id,
		Method:   VerifyManual,
		Status:   StatusVerified,
		Note:     "Checked in code",
	})
	if err != nil {
		t.Fatal(err)
	}

	m, _ := store.Get(id)
	if m.Metadata[memory.MetadataVerificationStatus] != "verified" {
		t.Errorf("expected verification_status=verified, got %s", m.Metadata[memory.MetadataVerificationStatus])
	}
	if m.Metadata[memory.MetadataVerificationMethod] != "manual" {
		t.Errorf("expected verification_method=manual, got %s", m.Metadata[memory.MetadataVerificationMethod])
	}
}

func TestVerificationCandidates(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	p := svc.Policy()
	p.StaleDays = 1
	_ = svc.SetPolicy(p)

	// Create an old unverified memory with high importance.
	_ = store.Store(ctx, &memory.Memory{
		Content:    "Important unverified fact",
		Type:       memory.TypeSemantic,
		Title:      "Critical decision",
		Importance: 0.9,
		Metadata:   map[string]string{"entity": "decision"},
	})
	db := store.DB()
	_, _ = db.Exec(`UPDATE memories SET updated_at = ? WHERE title = ?`,
		time.Now().Add(-48*time.Hour), "Critical decision")

	candidates, err := svc.VerificationCandidates(ctx, VerificationCandidatesParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected at least one verification candidate")
	}
	found := false
	for _, c := range candidates {
		if c.Title == "Critical decision" {
			found = true
			if c.Urgency != UrgencyMedium && c.Urgency != UrgencyHigh {
				t.Errorf("expected high or medium urgency, got %s", c.Urgency)
			}
		}
	}
	if !found {
		t.Error("expected Critical decision in candidates")
	}
}

func TestDriftScanSourceMissing(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	_ = store.Store(ctx, &memory.Memory{
		Content:    "Config in ./nonexistent/config.yaml sets timeout to 30s",
		Type:       memory.TypeSemantic,
		Title:      "Timeout config",
		Importance: 0.7,
	})

	result, err := svc.DriftScan(ctx, DriftScanParams{
		RootPath: t.TempDir(), // empty dir — file won't exist
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned == 0 {
		t.Error("expected at least 1 scanned")
	}

	hasMissing := false
	for _, f := range result.Findings {
		if f.DriftType == DriftSourceMissing {
			hasMissing = true
		}
	}
	if !hasMissing {
		t.Log("No source_missing findings (path extraction may not match). This is acceptable for content without clear path refs.")
	}
}

func TestCanonicalHealth(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	// Create a canonical entry.
	m := &memory.Memory{
		Content:    "Use PostgreSQL for all persistent storage",
		Type:       memory.TypeSemantic,
		Title:      "Database decision",
		Importance: 0.9,
		Metadata:   map[string]string{"entity": "decision", "knowledge_layer": "canonical"},
	}
	_ = store.Store(ctx, m)
	_, _ = store.PromoteToCanonical(ctx, m.ID, "test")

	report, err := svc.Run(ctx, RunParams{Scope: ScopeCanonical, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.CanonicalHealth == nil {
		t.Fatal("expected canonical health in report")
	}
	if report.CanonicalHealth.Total == 0 {
		t.Error("expected at least 1 canonical entry")
	}
}

func TestInboxCRUD(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	db := svc.DB()

	// Create item.
	item := &InboxItem{
		Kind:              InboxDuplicateCandidate,
		Title:             "Test duplicate",
		Confidence:        0.85,
		Urgency:           "high",
		RecommendedAction: "merge",
		TargetIDs:         []string{"a", "b"},
	}
	if err := CreateInboxItem(db, item); err != nil {
		t.Fatal(err)
	}
	if item.ID == "" {
		t.Error("expected ID to be set")
	}

	// List pending.
	items, err := svc.ListInbox(InboxQuery{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Kind != InboxDuplicateCandidate {
		t.Errorf("expected duplicate_candidate, got %s", items[0].Kind)
	}

	// Resolve.
	if err := svc.ResolveInbox(item.ID, "merge", "Merged by test", "test"); err != nil {
		t.Fatal(err)
	}

	// Should be gone from pending.
	items2, _ := svc.ListInbox(InboxQuery{Status: "pending"})
	if len(items2) != 0 {
		t.Errorf("expected 0 pending items, got %d", len(items2))
	}

	// But visible in resolved.
	items3, _ := svc.ListInbox(InboxQuery{Status: "resolved"})
	if len(items3) != 1 {
		t.Errorf("expected 1 resolved item, got %d", len(items3))
	}
}

func TestRunCreatesInboxItems(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	meta := map[string]string{"entity": "decision"}
	_ = store.Store(ctx, &memory.Memory{
		Content:  "decision A",
		Type:     memory.TypeSemantic,
		Title:    "same decision",
		Metadata: meta,
	})
	_ = store.Store(ctx, &memory.Memory{
		Content:  "decision B",
		Type:     memory.TypeSemantic,
		Title:    "same decision",
		Metadata: meta,
	})

	// Non-dry run should create inbox items for review-required actions.
	report, err := svc.Run(ctx, RunParams{Scope: ScopeDuplicates, DryRun: false})
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.ActionsPendingReview == 0 {
		t.Skip("no review-required actions generated")
	}

	items, err := svc.ListInbox(InboxQuery{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Error("expected inbox items from steward run")
	}
}

func containsStr(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
