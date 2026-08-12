package steward

import (
	"encoding/json"
	"testing"
)

// T103. A one-field `steward_policy set` used to reset every other field to its
// zero value, because Policy's fields cannot distinguish "absent" from "zero".
// This is the exact shape that wiped the T72/T73 operator decisions on a live
// store.
func TestPatchPolicyLeavesUnsentFieldsAlone(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)

	base := DefaultPolicy()
	base.WorkingDeleteImportanceCutoff = 0.6
	base.AutoMarkStaleImportanceCutoff = 0.6
	base.AutoDeleteExpiredWorking = true
	base.AutoMergeDuplicateMinConfidence = 0.95
	base.WorkingMemoryTTLDays = 14
	if err := svc.SetPolicy(base); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	// The real-world call: switch the mode, say nothing about anything else.
	var patch PolicyPatch
	if err := json.Unmarshal([]byte(`{"mode":"scheduled"}`), &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	got, err := svc.PatchPolicy(patch)
	if err != nil {
		t.Fatalf("PatchPolicy: %v", err)
	}

	if got.Mode != PolicyModeScheduled {
		t.Fatalf("Mode = %q, want scheduled", got.Mode)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"working_delete_importance_cutoff", got.WorkingDeleteImportanceCutoff, 0.6},
		{"auto_mark_stale_importance_cutoff", got.AutoMarkStaleImportanceCutoff, 0.6},
		{"auto_delete_expired_working", got.AutoDeleteExpiredWorking, true},
		{"auto_merge_duplicate_min_confidence", got.AutoMergeDuplicateMinConfidence, 0.95},
		{"working_memory_ttl_days", got.WorkingMemoryTTLDays, 14},
		{"stale_days", got.StaleDays, 30},
		{"duplicate_similarity", got.DuplicateSimilarity, 0.85},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v after a mode-only patch, want %v (unsent fields must not move)", c.name, c.got, c.want)
		}
	}

	// And the persisted copy agrees — the defect was observed through a later
	// steward_policy get, not in memory.
	reloaded, err := LoadPolicy(store.DB())
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if reloaded.AutoMergeDuplicateMinConfidence != 0.95 {
		t.Fatalf("persisted auto_merge_duplicate_min_confidence = %v, want 0.95", reloaded.AutoMergeDuplicateMinConfidence)
	}
	if !reloaded.AutoDeleteExpiredWorking {
		t.Fatal("persisted auto_delete_expired_working = false, want true")
	}
}

// An explicitly sent zero or false is a real instruction and must be applied —
// otherwise the patch type would trade one silent failure for another.
func TestPatchPolicyAppliesExplicitZeroes(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)

	if err := svc.SetPolicy(DefaultPolicy()); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	var patch PolicyPatch
	raw := `{"auto_delete_expired_working":false,"auto_merge_duplicate_min_confidence":0,"stale_days":0}`
	if err := json.Unmarshal([]byte(raw), &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	got, err := svc.PatchPolicy(patch)
	if err != nil {
		t.Fatalf("PatchPolicy: %v", err)
	}

	if got.AutoDeleteExpiredWorking {
		t.Error("auto_delete_expired_working = true, want false (explicitly sent)")
	}
	if got.AutoMergeDuplicateMinConfidence != 0 {
		t.Errorf("auto_merge_duplicate_min_confidence = %v, want 0 (explicitly sent)", got.AutoMergeDuplicateMinConfidence)
	}
	if got.StaleDays != 0 {
		t.Errorf("stale_days = %d, want 0 (explicitly sent)", got.StaleDays)
	}
	// Untouched neighbours still stand.
	if got.WorkingMemoryTTLDays != 14 {
		t.Errorf("working_memory_ttl_days = %d, want 14", got.WorkingMemoryTTLDays)
	}
}

// T106. The field cannot disable the TTL, whatever its comment used to say.
func TestEffectiveWorkingTTLHasNoDisableValue(t *testing.T) {
	for _, v := range []int{0, -1, -14} {
		p := Policy{WorkingMemoryTTLDays: v}
		if got := p.EffectiveWorkingTTLDays(); got != 14 {
			t.Errorf("EffectiveWorkingTTLDays(%d) = %d, want 14", v, got)
		}
	}
	if got := (Policy{WorkingMemoryTTLDays: 7}).EffectiveWorkingTTLDays(); got != 7 {
		t.Errorf("EffectiveWorkingTTLDays(7) = %d, want 7", got)
	}
}
