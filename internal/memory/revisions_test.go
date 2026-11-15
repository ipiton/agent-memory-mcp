package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// T155: a content update keeps the replaced text as a revision with its
// source; an update that leaves the text alone keeps nothing.
func TestUpdateRecordsRevisionOnlyForContentChange(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	m := &Memory{Title: "retention", Content: "log retention is 96 h", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Tags and metadata only: no revision.
	if err := store.Update(ctx, m.ID, Update{Tags: []string{"ops"}, Metadata: map[string]string{"owner": "sre"}, Source: RevisionSourceUpdateMemory}); err != nil {
		t.Fatalf("Update tags: %v", err)
	}
	// Same text (modulo surrounding space): no revision.
	if err := store.Update(ctx, m.ID, Update{Content: "  log retention is 96 h ", Source: RevisionSourceUpdateMemory}); err != nil {
		t.Fatalf("Update same content: %v", err)
	}
	revs, err := store.Revisions(ctx, m.ID)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after non-content updates = %d, want 0: %#v", len(revs), revs)
	}

	if err := store.Update(ctx, m.ID, Update{Content: "log retention is 30 days", Title: "retention v2", Source: RevisionSourceSessionConsolidate}); err != nil {
		t.Fatalf("Update content: %v", err)
	}
	if err := store.Update(ctx, m.ID, Update{Content: "log retention is 60 days"}); err != nil {
		t.Fatalf("Update content again: %v", err)
	}

	revs, err = store.Revisions(ctx, m.ID)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2: %#v", len(revs), revs)
	}
	first, second := revs[0], revs[1]
	if first.Revision != 1 || first.Content != "log retention is 96 h" || first.Title != "retention" || first.ReplacedByPath != RevisionSourceSessionConsolidate {
		t.Fatalf("first revision = %#v", first)
	}
	if second.Revision != 2 || second.Content != "log retention is 30 days" || second.Title != "retention v2" || second.ReplacedByPath != revisionSourceUnspecified {
		t.Fatalf("second revision = %#v", second)
	}
	if first.ReplacedAt.IsZero() {
		t.Fatal("replaced_at not recorded")
	}

	counts, err := store.CountRevisionsByPath(ctx)
	if err != nil {
		t.Fatalf("CountRevisionsByPath: %v", err)
	}
	if counts[RevisionSourceSessionConsolidate] != 1 || counts[revisionSourceUnspecified] != 1 {
		t.Fatalf("counts = %#v", counts)
	}

	if err := store.Delete(ctx, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if revs, _ := store.Revisions(ctx, m.ID); len(revs) != 0 {
		t.Fatalf("revisions after delete = %d, want 0", len(revs))
	}
}

// T155: recall_as_of on a date before a rewrite answers with the text of that
// date; after the rewrite, with the current one.
func TestRecallAsOfReturnsTextValidAtDate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	t0 := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(24 * time.Hour)
	t2 := t0.Add(48 * time.Hour)
	t3 := t0.Add(72 * time.Hour)

	store.now = func() time.Time { return t0 }
	m := &Memory{Title: "retention", Content: "log retention window is 96 hours", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	store.now = func() time.Time { return t2 }
	if err := store.Update(ctx, m.ID, Update{Content: "log retention window is 30 days", Source: RevisionSourceUpdateMemory}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	recallOne := func(at time.Time) *SearchResult {
		t.Helper()
		results, err := store.RecallAsOf(ctx, "log retention window", at, Filters{}, 5)
		if err != nil {
			t.Fatalf("RecallAsOf(%s): %v", at, err)
		}
		for _, r := range results {
			if r.Memory.ID == m.ID {
				return r
			}
		}
		t.Fatalf("RecallAsOf(%s) did not return the record: %#v", at, results)
		return nil
	}

	past := recallOne(t1)
	if past.Memory.Content != "log retention window is 96 hours" {
		t.Fatalf("RecallAsOf(t1) content = %q, want the t0 text", past.Memory.Content)
	}
	if past.Memory.Metadata[MetadataContentAsOfRevision] != "1" {
		t.Fatalf("RecallAsOf(t1) marker = %q, want revision 1", past.Memory.Metadata[MetadataContentAsOfRevision])
	}

	now := recallOne(t3)
	if now.Memory.Content != "log retention window is 30 days" {
		t.Fatalf("RecallAsOf(t3) content = %q, want the t2 text", now.Memory.Content)
	}
	if _, marked := now.Memory.Metadata[MetadataContentAsOfRevision]; marked {
		t.Fatal("RecallAsOf(t3) marked as a revision")
	}

	// The swap must not leak into the stored record.
	current, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.Content != "log retention window is 30 days" || current.Metadata[MetadataContentAsOfRevision] != "" {
		t.Fatalf("stored record changed by RecallAsOf: %q %#v", current.Content, current.Metadata)
	}
}

// T155: merge_duplicates rewrites the primary's text and keeps the old one.
func TestMergeDuplicatesRecordsPrimaryRevision(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	primary := &Memory{Content: "primary fact about the cache", Type: TypeSemantic}
	dup := &Memory{Content: "a different detail about eviction", Type: TypeSemantic}
	for _, m := range []*Memory{primary, dup} {
		if err := store.Store(ctx, m); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	if _, err := store.MergeDuplicates(ctx, primary.ID, []string{dup.ID}); err != nil {
		t.Fatalf("MergeDuplicates: %v", err)
	}
	merged, err := store.Get(primary.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if merged.Content == "primary fact about the cache" {
		t.Fatal("merge left the primary text unchanged; the test needs a rewriting merge")
	}
	revs, err := store.Revisions(ctx, primary.ID)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revs) != 1 || revs[0].Content != "primary fact about the cache" || revs[0].ReplacedByPath != RevisionSourceMergeDuplicates {
		t.Fatalf("revisions = %#v", revs)
	}
}

// T155: a bank created before memory_revisions existed gains the table on the
// next open, keeps its records untouched, and the migration is idempotent.
func TestNewStoreMigratesRevisionsTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	store, err := NewStore(dbPath, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("initial NewStore: %v", err)
	}
	ctx := context.Background()
	m := &Memory{Title: "legacy", Content: "legacy text", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// Simulate a bank from before T155.
	if _, err := store.db.Exec(`DROP TABLE memory_revisions`); err != nil {
		t.Fatalf("remove revisions table: %v", err)
	}
	var before string
	if err := store.db.QueryRow(`SELECT content || '|' || updated_at FROM memories WHERE id = ?`, m.ID).Scan(&before); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for i := 0; i < 2; i++ {
		reopened, err := NewStore(dbPath, nil, zap.NewNop())
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		var after string
		if err := reopened.db.QueryRow(`SELECT content || '|' || updated_at FROM memories WHERE id = ?`, m.ID).Scan(&after); err != nil {
			t.Fatalf("read migrated row: %v", err)
		}
		if after != before {
			t.Fatalf("migration touched the record: %q -> %q", before, after)
		}
		if i == 1 {
			if err := reopened.Update(ctx, m.ID, Update{Content: "new text"}); err != nil {
				t.Fatalf("Update after migration: %v", err)
			}
			if revs, _ := reopened.Revisions(ctx, m.ID); len(revs) != 1 || revs[0].Content != "legacy text" {
				t.Fatalf("revisions after update = %#v", revs)
			}
		} else if revs, err := reopened.Revisions(ctx, m.ID); err != nil || len(revs) != 0 {
			t.Fatalf("revisions after migration = %v, %v; want none", revs, err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("close reopened: %v", err)
		}
	}
}
