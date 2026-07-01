package steward

import (
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func TestLifecycleInvalidationConflict(t *testing.T) {
	cases := []struct {
		name string
		a, b memory.LifecycleStatus
		want bool
	}{
		{"active vs superseded", memory.LifecycleActive, memory.LifecycleSuperseded, true},
		{"outdated vs canonical", memory.LifecycleOutdated, memory.LifecycleCanonical, true},
		{"draft vs outdated", memory.LifecycleDraft, memory.LifecycleOutdated, true},
		// Dual-encoding / maturation: all live statuses, never a conflict.
		{"draft vs active", memory.LifecycleDraft, memory.LifecycleActive, false},
		{"active vs canonical", memory.LifecycleActive, memory.LifecycleCanonical, false},
		{"draft vs canonical", memory.LifecycleDraft, memory.LifecycleCanonical, false},
		{"identical active", memory.LifecycleActive, memory.LifecycleActive, false},
		// Two invalidated entries are not a live-vs-dead conflict.
		{"outdated vs superseded", memory.LifecycleOutdated, memory.LifecycleSuperseded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lifecycleInvalidationConflict(tc.a, tc.b); got != tc.want {
				t.Fatalf("lifecycleInvalidationConflict(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// Symmetric.
			if got := lifecycleInvalidationConflict(tc.b, tc.a); got != tc.want {
				t.Fatalf("lifecycleInvalidationConflict(%s, %s) [swapped] = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// TestHasContradictionSignals_DualEncoding asserts the T60 fix: a raw session
// summary and the extracted/canonical entity for the same subject (different
// lifecycle, same content meaning) are not flagged, while a same-layer
// invalidation or content disagreement still is.
func TestHasContradictionSignals_DualEncoding(t *testing.T) {
	rawSummary := &memory.Memory{
		ID:      "raw-de",
		Title:   "Session close / deploy-pipeline",
		Content: "Wrapped up the deploy pipeline work for this session.",
		Type:    memory.TypeWorking, // → LifecycleDraft
		Context: "deploy-pipeline",
	}
	canonicalEntity := &memory.Memory{
		ID:       "canon-de",
		Title:    "Task complete: deploy-pipeline",
		Content:  "Deploy pipeline shipped: CI builds and pushes the image.",
		Type:     memory.TypeSemantic,
		Context:  "deploy-pipeline",
		Metadata: map[string]string{"knowledge_layer": "canonical"}, // → LifecycleCanonical
	}
	if hasContradictionSignals(rawSummary, canonicalEntity) {
		t.Fatal("dual-encoding pair (raw summary vs canonical entity) must NOT be a contradiction")
	}

	// Same-layer genuine invalidation: an active entry vs an explicitly
	// outdated one on the same subject is still a conflict.
	active := &memory.Memory{
		ID:      "auth-active",
		Title:   "Auth uses JWT",
		Content: "Authentication is handled with JWT access tokens.",
		Type:    memory.TypeSemantic,
		Context: "auth",
	}
	outdated := &memory.Memory{
		ID:       "auth-outdated",
		Title:    "Auth uses sessions",
		Content:  "Authentication is handled with server-side sessions.",
		Type:     memory.TypeSemantic,
		Context:  "auth",
		Metadata: map[string]string{"lifecycle_status": "outdated"},
	}
	if !hasContradictionSignals(active, outdated) {
		t.Fatal("active vs outdated on same subject must be a contradiction")
	}

	// Same-layer content disagreement (both live) is still flagged via keywords.
	superseding := &memory.Memory{
		ID:      "transport-grpc",
		Title:   "Switched to gRPC",
		Content: "The service switched to gRPC instead of REST.",
		Type:    memory.TypeSemantic,
		Context: "transport",
	}
	other := &memory.Memory{
		ID:      "transport-rest",
		Title:   "REST transport",
		Content: "The service exposes a REST API.",
		Type:    memory.TypeSemantic,
		Context: "transport",
	}
	if !hasContradictionSignals(superseding, other) {
		t.Fatal("content disagreement keyword (switched to) must still flag a contradiction")
	}
}

// TestScanSemanticConflicts_SkipsDualEncoding drives the full scan path: a
// dual-encoding pair with identical embeddings produces no contradiction
// action, while a same-subject invalidation does.
func TestScanSemanticConflicts_SkipsDualEncoding(t *testing.T) {
	emb := []float32{0.1, 0.2, 0.3, 0.4}

	dualA := &memory.Memory{
		ID: "raw-1", Title: "Session close / billing", Content: "Closed out billing work.",
		Type: memory.TypeWorking, Context: "billing", Embedding: emb,
	}
	dualB := &memory.Memory{
		ID: "canon-1", Title: "Task complete: billing", Content: "Billing service finished.",
		Type: memory.TypeSemantic, Context: "billing", Embedding: emb,
		Metadata: map[string]string{"knowledge_layer": "canonical"},
	}

	res := &ScanResult{}
	scanSemanticConflicts([]*memory.Memory{dualA, dualB}, DefaultPolicy(), res)
	if n := countContradictions(res); n != 0 {
		t.Fatalf("dual-encoding pair produced %d contradiction(s), want 0", n)
	}

	liveC := &memory.Memory{
		ID: "live-1", Title: "Cache TTL is 60s", Content: "Cache entries expire after 60 seconds.",
		Type: memory.TypeSemantic, Context: "cache", Embedding: emb,
	}
	deadC := &memory.Memory{
		ID: "dead-1", Title: "Cache TTL is 5m", Content: "Cache entries expire after 5 minutes.",
		Type: memory.TypeSemantic, Context: "cache", Embedding: emb,
		Metadata: map[string]string{"lifecycle_status": "superseded"},
	}
	res2 := &ScanResult{}
	scanSemanticConflicts([]*memory.Memory{liveC, deadC}, DefaultPolicy(), res2)
	if n := countContradictions(res2); n != 1 {
		t.Fatalf("active vs superseded pair produced %d contradiction(s), want 1", n)
	}
}

func countContradictions(res *ScanResult) int {
	n := 0
	for _, a := range res.Actions {
		if a.Kind == ActionFlagContradiction {
			n++
		}
	}
	return n
}

// TestHasContradictionSignals_SuppressesTerminalPair asserts the T72 fix: a
// legacy "Task complete: X" ↔ "Session close / X" pair is suppressed even when
// its content carries a contradiction keyword ("removed", "switched to"), which
// the T60 fix alone would not catch because those keywords trip the content
// signal. A non-terminal pair with the same keyword still flags.
func TestHasContradictionSignals_SuppressesTerminalPair(t *testing.T) {
	taskComplete := &memory.Memory{
		ID:      "tc-1",
		Title:   "Task complete: cache-refactor",
		Content: "Removed the old cache layer and switched to the new store.",
		Type:    memory.TypeEpisodic,
		Context: "cache-refactor",
	}
	sessionClose := &memory.Memory{
		ID:       "sc-1",
		Title:    "Session close / cache-refactor",
		Content:  "Wrapped the cache refactor; the previous approach was removed.",
		Type:     memory.TypeWorking,
		Context:  "cache-refactor",
		Metadata: map[string]string{memory.MetadataRecordKind: memory.RecordKindSessionSummary},
	}
	if hasContradictionSignals(taskComplete, sessionClose) {
		t.Fatal("terminal Task complete ↔ Session close pair must NOT be flagged as a contradiction (T72)")
	}

	// Sanity: the same contradiction keyword between two non-terminal memories
	// still flags, so the guard is scoped to the terminal dual-write class.
	liveNote := &memory.Memory{
		ID:      "note-live",
		Title:   "Cache uses Redis",
		Content: "The cache uses Redis.",
		Type:    memory.TypeSemantic,
		Context: "cache-refactor",
	}
	changedNote := &memory.Memory{
		ID:      "note-changed",
		Title:   "Cache store",
		Content: "The cache switched to an in-memory store instead of Redis.",
		Type:    memory.TypeSemantic,
		Context: "cache-refactor",
	}
	if !hasContradictionSignals(liveNote, changedNote) {
		t.Fatal("non-terminal pair with a contradiction keyword must still flag")
	}
}

// TestScanStale_AutoApplyGuards covers the T72 auto-mark classification: past
// the auto-threshold a low-importance non-canonical entry is safe_auto_apply,
// while high-importance and canonical entries stay review_required.
func TestScanStale_AutoApplyGuards(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -90) // well past StaleDays and AutoMarkStaleBeyondDays

	policy := DefaultPolicy() // StaleDays=30, AutoMarkStaleBeyondDays=30, cutoff=0.6

	lowImp := &memory.Memory{ID: "stale-low", Title: "Old note", Type: memory.TypeSemantic, Importance: 0.4, UpdatedAt: old}
	highImp := &memory.Memory{ID: "stale-high", Title: "Old important", Type: memory.TypeSemantic, Importance: 0.9, UpdatedAt: old}
	canon := &memory.Memory{
		ID: "stale-canon", Title: "Old canonical", Type: memory.TypeSemantic, Importance: 0.4, UpdatedAt: old,
		Metadata: map[string]string{"knowledge_layer": "canonical"},
	}

	res := &ScanResult{}
	scanStale([]*memory.Memory{lowImp, highImp, canon}, policy, now, res)

	handling := map[string]ActionHandling{}
	for _, a := range res.Actions {
		if a.Kind == ActionMarkStale && len(a.TargetIDs) == 1 {
			handling[a.TargetIDs[0]] = a.Handling
		}
	}

	if got := handling["stale-low"]; got != HandlingSafeAutoApply {
		t.Errorf("low-importance stale: handling = %q, want safe_auto_apply", got)
	}
	if got := handling["stale-high"]; got != HandlingReviewRequired {
		t.Errorf("high-importance stale: handling = %q, want review_required", got)
	}
	if got := handling["stale-canon"]; got != HandlingReviewRequired {
		t.Errorf("canonical stale: handling = %q, want review_required", got)
	}
}
