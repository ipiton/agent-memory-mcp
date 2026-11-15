package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Revision sources — who replaced a record's text (T155). The value lands in
// memory_revisions.replaced_by_path, so memory_stats can show how many
// rewrites came from an agent and how many from a heuristic nobody reviewed.
const (
	RevisionSourceUpdateMemory       = "update_memory"
	RevisionSourceSessionNearExact   = "sessionclose.near_exact"
	RevisionSourceSessionConsolidate = "sessionclose.consolidate"
	RevisionSourceSessionFold        = "sessionclose.session_fold"
	RevisionSourceMergeDuplicates    = "merge_duplicates"
	revisionSourceUnspecified        = "unspecified"
)

// MetadataContentAsOfRevision is set on a RecallAsOf result whose content and
// title were taken from a revision: the text as of the date, which differs
// from the current one. Set on the returned copy only, never stored.
const MetadataContentAsOfRevision = "content_as_of_revision"

// Revision is the text a record carried before a content update replaced it.
type Revision struct {
	MemoryID       string
	Revision       int
	Content        string
	Title          string
	ReplacedAt     time.Time
	ReplacedByPath string
}

// insertRevision records prior's content and title as the next revision of
// the record. It runs in the caller's transaction, before the row is
// overwritten, so the old text and the new one commit together or not at all.
func insertRevision(tx *sql.Tx, prior *Memory, replacedAt time.Time, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		source = revisionSourceUnspecified
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_revisions (memory_id, revision, content, title, replaced_at, replaced_by_path)
		SELECT ?, COALESCE(MAX(revision), 0) + 1, ?, ?, ?, ? FROM memory_revisions WHERE memory_id = ?`,
		prior.ID, prior.Content, nullStr(prior.Title), replacedAt.UTC(), source, prior.ID,
	); err != nil {
		return fmt.Errorf("failed to record memory revision: %w", err)
	}
	return nil
}

// Revisions returns the record's replaced texts, oldest first.
func (ms *Store) Revisions(ctx context.Context, id string) ([]Revision, error) {
	rows, err := ms.db.QueryContext(ctx, `
		SELECT memory_id, revision, content, title, replaced_at, replaced_by_path
		FROM memory_revisions WHERE memory_id = ? ORDER BY revision`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to read memory revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Revision
	for rows.Next() {
		var r Revision
		var title sql.NullString
		var replacedAt sql.NullTime
		if err := rows.Scan(&r.MemoryID, &r.Revision, &r.Content, &title, &replacedAt, &r.ReplacedByPath); err != nil {
			return nil, fmt.Errorf("failed to scan memory revision: %w", err)
		}
		r.Title = title.String
		r.ReplacedAt = replacedAt.Time
		out = append(out, r)
	}
	return out, rows.Err()
}

// revisionAsOf returns the revision whose text was current at asOf: the
// earliest one replaced after that moment. Nil means the current text was
// already in place at asOf. Times are compared in Go, not in SQL, because the
// driver's stored DATETIME text does not order reliably as a string.
func (ms *Store) revisionAsOf(ctx context.Context, id string, asOf time.Time) (*Revision, error) {
	revisions, err := ms.Revisions(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range revisions {
		if revisions[i].ReplacedAt.After(asOf) {
			return &revisions[i], nil
		}
	}
	return nil, nil
}

// CountRevisionsByPath reports how many content revisions each source wrote.
func (ms *Store) CountRevisionsByPath(ctx context.Context) (map[string]int, error) {
	rows, err := ms.db.QueryContext(ctx, `SELECT replaced_by_path, COUNT(*) FROM memory_revisions GROUP BY replaced_by_path`)
	if err != nil {
		return nil, fmt.Errorf("failed to count memory revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := make(map[string]int)
	for rows.Next() {
		var path string
		var n int
		if err := rows.Scan(&path, &n); err != nil {
			return nil, fmt.Errorf("failed to scan memory revision count: %w", err)
		}
		counts[path] = n
	}
	return counts, rows.Err()
}
