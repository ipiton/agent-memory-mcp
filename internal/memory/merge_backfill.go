package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// mergedDuplicateBackfillVersion is the PRAGMA user_version stamped once the
// merged-duplicate backfill has run. It follows utf8RepairSchemaVersion.
const mergedDuplicateBackfillVersion = 2

type mergedDuplicateBackfill struct {
	updated       int
	skippedCycles int
}

// backfillMergedDuplicatesOnce gives duplicates merged by earlier versions the
// columns MergeDuplicates now writes. Those versions recorded the merge only in
// metadata, so the rows kept answering Recall and every recall_as_of date. The
// metadata still identifies them, and without this they would stay visible for
// good. Gated on user_version so the full scan runs once; rerunning after a crash
// before the stamp is harmless.
func backfillMergedDuplicatesOnce(db *sql.DB) (mergedDuplicateBackfill, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return mergedDuplicateBackfill{}, err
	}
	if version >= mergedDuplicateBackfillVersion {
		return mergedDuplicateBackfill{}, nil
	}
	result, err := backfillMergedDuplicates(db)
	if err != nil {
		return mergedDuplicateBackfill{}, err
	}
	// PRAGMA does not accept bound parameters; the value is a trusted constant.
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, mergedDuplicateBackfillVersion)); err != nil {
		return mergedDuplicateBackfill{}, err
	}
	return result, nil
}

type mergedDuplicateRow struct {
	id           string
	supersededBy string
	validUntil   time.Time
	changed      bool
}

// backfillMergedDuplicates sets superseded_by and valid_until on merged
// duplicates. updated_at is left alone: freshness scoring reads it.
//
// The merge time is not stored as such. The merge wrote it to both updated_at
// and last_verified_at, and either can only move forward afterwards, so the
// earlier of the two is the closest bound. Values already in the columns win: a
// pointer there is a later MarkOutdated, and an earlier valid_until means the
// record had stopped being valid before the merge. Records promoted to canonical
// after their merge are live again and are skipped.
//
// Old versions could merge two records into each other. Hiding both would remove
// that knowledge from recall, so the members of such a cycle stay as they are.
func backfillMergedDuplicates(db *sql.DB) (mergedDuplicateBackfill, error) {
	rows, err := db.Query(`SELECT id, metadata, updated_at, valid_until, superseded_by FROM memories`)
	if err != nil {
		return mergedDuplicateBackfill{}, err
	}
	next := make(map[string]string)
	var candidates []mergedDuplicateRow
	for rows.Next() {
		var id string
		var metadataJSON, supersededBy sql.NullString
		var updatedAt, validUntil sql.NullTime
		if err := rows.Scan(&id, &metadataJSON, &updatedAt, &validUntil, &supersededBy); err != nil {
			_ = rows.Close()
			return mergedDuplicateBackfill{}, err
		}
		current := strings.TrimSpace(supersededBy.String)
		if current != "" {
			next[id] = current
		}
		metadata, err := parseMetadataJSON(metadataJSON)
		if err != nil {
			continue
		}
		mergedInto := strings.TrimSpace(metadata["merged_into"])
		if mergedInto == "" || LifecycleStatusOf(&Memory{Metadata: metadata}) != LifecycleSuperseded {
			continue
		}

		row := mergedDuplicateRow{id: id, supersededBy: current}
		if row.supersededBy == "" {
			row.supersededBy = mergedInto
			row.changed = true
			next[id] = mergedInto
		}
		mergedAt, known := earliestMergeTime(updatedAt, metadata["last_verified_at"])
		switch {
		case validUntil.Valid && (!known || !validUntil.Time.After(mergedAt)):
			row.validUntil = validUntil.Time
		case known:
			row.validUntil = mergedAt
			row.changed = true
		}
		if row.changed {
			candidates = append(candidates, row)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return mergedDuplicateBackfill{}, err
	}
	// modernc SQLite cannot UPDATE while a SELECT cursor is open on the connection.
	if err := rows.Close(); err != nil {
		return mergedDuplicateBackfill{}, err
	}

	var result mergedDuplicateBackfill
	var pending []mergedDuplicateRow
	for _, row := range candidates {
		if inSupersessionCycle(row.id, next) {
			result.skippedCycles++
			continue
		}
		pending = append(pending, row)
	}
	if len(pending) == 0 {
		return result, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return mergedDuplicateBackfill{}, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range pending {
		validUntil := row.validUntil
		var until *time.Time
		if !validUntil.IsZero() {
			until = &validUntil
		}
		if _, err := tx.Exec(`UPDATE memories SET superseded_by = ?, valid_until = ? WHERE id = ?`,
			row.supersededBy, nullTime(until), row.id); err != nil {
			return mergedDuplicateBackfill{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return mergedDuplicateBackfill{}, err
	}
	result.updated = len(pending)
	return result, nil
}

func earliestMergeTime(updatedAt sql.NullTime, lastVerifiedAt string) (time.Time, bool) {
	verifiedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(lastVerifiedAt))
	switch {
	case updatedAt.Valid && err == nil && verifiedAt.Before(updatedAt.Time):
		return verifiedAt.UTC(), true
	case updatedAt.Valid:
		return updatedAt.Time.UTC(), true
	case err == nil:
		return verifiedAt.UTC(), true
	}
	return time.Time{}, false
}

// inSupersessionCycle reports whether following the pointers from id returns to id.
func inSupersessionCycle(id string, next map[string]string) bool {
	seen := map[string]struct{}{id: {}}
	for current := next[id]; current != ""; current = next[current] {
		if current == id {
			return true
		}
		if _, ok := seen[current]; ok {
			return false
		}
		seen[current] = struct{}{}
	}
	return false
}
