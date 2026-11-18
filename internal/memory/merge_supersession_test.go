package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	mergeTestCreated = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mergeTestAt      = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
)

const (
	mergeTestPrimaryText   = "routing uses chi router"
	mergeTestDuplicateText = "routing needs a timeout middleware"
)

// storeForMerge stores a record at mergeTestCreated and returns its id.
func storeForMerge(t *testing.T, store *Store, content string) string {
	t.Helper()
	store.SetClock(func() time.Time { return mergeTestCreated })
	m := &Memory{Content: content, Type: TypeSemantic, Importance: 0.8}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	return m.ID
}

// mergeForTest stores a primary and a duplicate and merges them through the real
// MergeDuplicates at mergeTestAt.
func mergeForTest(t *testing.T, store *Store) (primaryID, duplicateID string) {
	t.Helper()
	primaryID = storeForMerge(t, store, mergeTestPrimaryText)
	duplicateID = storeForMerge(t, store, mergeTestDuplicateText)
	store.SetClock(func() time.Time { return mergeTestAt })
	if _, err := store.MergeDuplicates(context.Background(), primaryID, []string{duplicateID}); err != nil {
		t.Fatalf("MergeDuplicates: %v", err)
	}
	return primaryID, duplicateID
}

func recallAsOfByID(t *testing.T, store *Store, asOf time.Time) map[string]*Memory {
	t.Helper()
	results, err := store.RecallAsOf(context.Background(), "routing", asOf, Filters{}, 10)
	if err != nil {
		t.Fatalf("RecallAsOf: %v", err)
	}
	byID := make(map[string]*Memory, len(results))
	for _, r := range results {
		byID[r.Memory.ID] = r.Memory
	}
	return byID
}

func recallIDsForMerge(t *testing.T, store *Store) map[string]bool {
	t.Helper()
	results, err := store.Recall(context.Background(), "routing", Filters{}, 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	return resultIDs(results)
}

// The merged text lives in the primary, so the duplicate must not answer next to it.
func TestMergeDuplicates_HidesDuplicateFromRecall(t *testing.T) {
	store := newTemporalTestStore(t)
	primaryID, duplicateID := mergeForTest(t, store)

	ids := recallIDsForMerge(t, store)
	if ids[duplicateID] {
		t.Error("Recall returned the merged duplicate")
	}
	if !ids[primaryID] {
		t.Error("Recall did not return the primary")
	}
}

// Before the merge both records existed and each carried its own text; from the
// merge on only the primary answers, with the combined text.
func TestRecallAsOf_FollowsMerge(t *testing.T) {
	store := newTemporalTestStore(t)
	primaryID, duplicateID := mergeForTest(t, store)

	before := recallAsOfByID(t, store, mergeTestAt.Add(-time.Hour))
	if before[duplicateID] == nil {
		t.Error("duplicate missing before the merge")
	}
	if p := before[primaryID]; p == nil {
		t.Error("primary missing before the merge: it existed all along")
	} else if p.Content != mergeTestPrimaryText {
		t.Errorf("primary text before the merge = %q, want the pre-merge text", p.Content)
	}

	for name, asOf := range map[string]time.Time{"at": mergeTestAt, "after": mergeTestAt.Add(time.Hour)} {
		t.Run(name, func(t *testing.T) {
			got := recallAsOfByID(t, store, asOf)
			if got[duplicateID] != nil {
				t.Error("duplicate returned from the merge on")
			}
			p := got[primaryID]
			if p == nil {
				t.Fatal("primary missing")
			}
			if !strings.Contains(p.Content, mergeTestDuplicateText) {
				t.Errorf("primary text = %q, want the merged text", p.Content)
			}
		})
	}
}

// The primary did not start at the merge, and its replaces pointer belongs to a
// real supersession chain, so the merge leaves its temporal fields alone.
func TestMergeDuplicates_KeepsPrimaryTemporalFields(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()

	predecessorID := storeForMerge(t, store, "routing uses gorilla mux")
	primaryID := storeForMerge(t, store, mergeTestPrimaryText)
	duplicateID := storeForMerge(t, store, mergeTestDuplicateText)

	replacedAt := mergeTestAt.Add(-24 * time.Hour)
	store.SetClock(func() time.Time { return replacedAt })
	if _, err := store.MarkOutdated(ctx, predecessorID, "replaced", primaryID); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}
	store.SetClock(func() time.Time { return mergeTestAt })
	if _, err := store.MergeDuplicates(ctx, primaryID, []string{duplicateID}); err != nil {
		t.Fatalf("MergeDuplicates: %v", err)
	}

	primary, err := store.Get(primaryID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if primary.ValidFrom == nil || !primary.ValidFrom.Equal(replacedAt) {
		t.Errorf("primary valid_from = %v, want %v", primary.ValidFrom, replacedAt)
	}
	if primary.ValidUntil != nil {
		t.Errorf("primary valid_until = %v, want nil", primary.ValidUntil)
	}
	if primary.Replaces != predecessorID {
		t.Errorf("primary replaces = %q, want %q", primary.Replaces, predecessorID)
	}
}

func TestKnowledgeTimeline_ShowsMergeLink(t *testing.T) {
	store := newTemporalTestStore(t)
	primaryID, duplicateID := mergeForTest(t, store)

	entries, err := store.KnowledgeTimeline(context.Background(), "routing", "", "")
	if err != nil {
		t.Fatalf("KnowledgeTimeline: %v", err)
	}
	byID := make(map[string]TimelineEntry, len(entries))
	for _, e := range entries {
		byID[e.MemoryID] = e
	}
	if _, ok := byID[primaryID]; !ok {
		t.Error("primary missing from the timeline")
	}
	dup, ok := byID[duplicateID]
	if !ok {
		t.Fatal("duplicate missing from the timeline")
	}
	if dup.SupersededBy != primaryID {
		t.Errorf("duplicate superseded_by = %q, want %q", dup.SupersededBy, primaryID)
	}
	if dup.ValidUntil == nil || !dup.ValidUntil.Equal(mergeTestAt) {
		t.Errorf("duplicate valid_until = %v, want %v", dup.ValidUntil, mergeTestAt)
	}
	if dup.Status != string(LifecycleSuperseded) {
		t.Errorf("duplicate status = %q, want %q", dup.Status, LifecycleSuperseded)
	}
}

// A record that had already stopped being valid must not look valid up to the
// merge, and repeating a merge must not move the date.
func TestMergeDuplicates_KeepsEarlierValidUntil(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()
	primaryID := storeForMerge(t, store, mergeTestPrimaryText)
	duplicateID := storeForMerge(t, store, mergeTestDuplicateText)

	outdatedAt := mergeTestAt.Add(-24 * time.Hour)
	store.SetClock(func() time.Time { return outdatedAt })
	if _, err := store.MarkOutdated(ctx, duplicateID, "stale", ""); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}
	for _, at := range []time.Time{mergeTestAt, mergeTestAt.Add(time.Hour)} {
		store.SetClock(func() time.Time { return at })
		if _, err := store.MergeDuplicates(ctx, primaryID, []string{duplicateID}); err != nil {
			t.Fatalf("MergeDuplicates: %v", err)
		}
	}

	dup, err := store.Get(duplicateID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dup.ValidUntil == nil || !dup.ValidUntil.Equal(outdatedAt) {
		t.Errorf("valid_until = %v, want %v", dup.ValidUntil, outdatedAt)
	}
	if dup.SupersededBy != primaryID {
		t.Errorf("superseded_by = %q, want %q", dup.SupersededBy, primaryID)
	}
}

// The duplicate's text survives only in the primary. A retired primary would
// take that text out of the answers together with itself.
func TestMergeDuplicates_RefusesRetiredPrimary(t *testing.T) {
	ctx := context.Background()

	t.Run("merged back into its own duplicate", func(t *testing.T) {
		store := newTemporalTestStore(t)
		primaryID, duplicateID := mergeForTest(t, store)
		_, err := store.MergeDuplicates(ctx, duplicateID, []string{primaryID})
		var validation *ErrValidation
		if !errors.As(err, &validation) {
			t.Fatalf("err = %v, want ErrValidation", err)
		}
		if !recallIDsForMerge(t, store)[primaryID] {
			t.Error("the refused merge hid the primary")
		}
	})

	t.Run("validity window closed", func(t *testing.T) {
		store := newTemporalTestStore(t)
		primaryID := storeForMerge(t, store, mergeTestPrimaryText)
		duplicateID := storeForMerge(t, store, mergeTestDuplicateText)
		if _, err := store.MarkOutdated(ctx, primaryID, "stale", ""); err != nil {
			t.Fatalf("MarkOutdated: %v", err)
		}
		store.SetClock(func() time.Time { return mergeTestAt })
		_, err := store.MergeDuplicates(ctx, primaryID, []string{duplicateID})
		var validation *ErrValidation
		if !errors.As(err, &validation) {
			t.Fatalf("err = %v, want ErrValidation", err)
		}
	})

	t.Run("successor deleted", func(t *testing.T) {
		store := newTemporalTestStore(t)
		primaryID := storeForMerge(t, store, mergeTestPrimaryText)
		duplicateID := storeForMerge(t, store, mergeTestDuplicateText)
		goneID := storeForMerge(t, store, "routing successor that gets deleted")
		if err := store.SetTemporalFields(ctx, primaryID, nil, nil, goneID, ""); err != nil {
			t.Fatalf("SetTemporalFields: %v", err)
		}
		if err := store.Delete(ctx, goneID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.MergeDuplicates(ctx, primaryID, []string{duplicateID}); err != nil {
			t.Errorf("a dangling pointer does not retire the primary: %v", err)
		}
	})
}

// A merge folds a copy into the primary. It is not another record pointing at
// the primary, so the column it now fills must not feed the promotion counter.
func TestRecountReferences_IgnoresMergedDuplicates(t *testing.T) {
	store := newTemporalTestStore(t)
	ctx := context.Background()
	primaryID, _ := mergeForTest(t, store)

	olderID := storeForMerge(t, store, "routing uses gorilla mux")
	if _, err := store.MarkOutdated(ctx, olderID, "replaced", primaryID); err != nil {
		t.Fatalf("MarkOutdated: %v", err)
	}
	if _, err := store.RecountReferences(ctx, false); err != nil {
		t.Fatalf("RecountReferences: %v", err)
	}
	if got := getCount(t, store, primaryID); got != 1 {
		t.Errorf("referenced_by_count = %d, want 1 (the real supersession only)", got)
	}
}

// makeLegacyMerge rewinds a merged duplicate to what MergeDuplicates used to
// leave behind: metadata only, both columns empty, migration not yet applied.
func makeLegacyMerge(t *testing.T, store *Store, duplicateID string) {
	t.Helper()
	if _, err := store.db.Exec(`UPDATE memories SET superseded_by = NULL, valid_until = NULL WHERE id = ?`, duplicateID); err != nil {
		t.Fatalf("rewind row: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
}

func setMetadataField(t *testing.T, store *Store, id, key, value string) {
	t.Helper()
	m, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	m.Metadata[key] = value
	if err := updateMemoryRow(store.db, m); err != nil {
		t.Fatalf("updateMemoryRow: %v", err)
	}
}

func TestNewStore_BackfillsLegacyMergedDuplicates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	var primaryID, duplicateID string
	store := storeAndClose(t, dbPath, func(s *Store) {
		primaryID, duplicateID = mergeForTest(t, s)
		// A later edit moves updated_at; last_verified_at still holds the merge.
		if _, err := s.db.Exec(`UPDATE memories SET updated_at = ? WHERE id = ?`, mergeTestAt.Add(48*time.Hour), duplicateID); err != nil {
			t.Fatalf("move updated_at: %v", err)
		}
		makeLegacyMerge(t, s, duplicateID)
	})

	dup, err := store.Get(duplicateID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dup.SupersededBy != primaryID {
		t.Errorf("superseded_by = %q, want %q", dup.SupersededBy, primaryID)
	}
	if dup.ValidUntil == nil || !dup.ValidUntil.Equal(mergeTestAt) {
		t.Errorf("valid_until = %v, want the merge time %v", dup.ValidUntil, mergeTestAt)
	}
	if !dup.UpdatedAt.Equal(mergeTestAt.Add(48 * time.Hour)) {
		t.Errorf("updated_at = %v, the migration must not touch it", dup.UpdatedAt)
	}
	if recallIDsForMerge(t, store)[duplicateID] {
		t.Error("Recall returned the migrated duplicate")
	}
	if recallAsOfByID(t, store, mergeTestAt.Add(-time.Hour))[duplicateID] == nil {
		t.Error("migrated duplicate missing before the merge")
	}

	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != 2 {
		t.Errorf("user_version = %d, want 2", version)
	}
}

// Records the migration must leave as they are, because hiding them would lose
// knowledge or rewrite a later decision.
func TestNewStore_BackfillLeavesOtherRecordsAlone(t *testing.T) {
	ctx := context.Background()

	t.Run("duplicate promoted to canonical after the merge", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "legacy.db")
		var duplicateID string
		store := storeAndClose(t, dbPath, func(s *Store) {
			_, duplicateID = mergeForTest(t, s)
			if _, err := s.PromoteToCanonical(ctx, duplicateID, "owner", true); err != nil {
				t.Fatalf("PromoteToCanonical: %v", err)
			}
			makeLegacyMerge(t, s, duplicateID)
		})
		if !recallIDsForMerge(t, store)[duplicateID] {
			t.Error("the migration hid a record promoted after its merge")
		}
	})

	t.Run("two records merged into each other", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "legacy.db")
		var aID, bID string
		store := storeAndClose(t, dbPath, func(s *Store) {
			aID, bID = mergeForTest(t, s)
			setMetadataField(t, s, aID, "merged_into", bID)
			setMetadataField(t, s, aID, "status", "merged")
			setMetadataField(t, s, aID, "archived", "true")
			makeLegacyMerge(t, s, aID)
			makeLegacyMerge(t, s, bID)
		})
		ids := recallIDsForMerge(t, store)
		if !ids[aID] || !ids[bID] {
			t.Errorf("merge cycle lost from Recall: a=%v b=%v", ids[aID], ids[bID])
		}
	})

	t.Run("later supersession and earlier end date win", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "legacy.db")
		endedAt := mergeTestAt.Add(-24 * time.Hour)
		var duplicateID, successorID string
		store := storeAndClose(t, dbPath, func(s *Store) {
			_, duplicateID = mergeForTest(t, s)
			successorID = storeForMerge(t, s, "routing successor")
			makeLegacyMerge(t, s, duplicateID)
			if _, err := s.db.Exec(`UPDATE memories SET superseded_by = ?, valid_until = ? WHERE id = ?`, successorID, endedAt, duplicateID); err != nil {
				t.Fatalf("set columns: %v", err)
			}
		})
		dup, err := store.Get(duplicateID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if dup.SupersededBy != successorID {
			t.Errorf("superseded_by = %q, want %q", dup.SupersededBy, successorID)
		}
		if dup.ValidUntil == nil || !dup.ValidUntil.Equal(endedAt) {
			t.Errorf("valid_until = %v, want %v", dup.ValidUntil, endedAt)
		}
	})
}
