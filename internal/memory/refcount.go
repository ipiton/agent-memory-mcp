package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// IncrementReferencedByCount atomically bumps the MetadataReferencedByCount
// counter on the target memory by 1. Used when another memory creates a
// reference (e.g. avoided_dead_end_id, supersession). Pure increment; never
// decrements. The counter feeds the T48 semantic→character "by refs"
// transition rule.
//
// Thread-safety: writeMu → mu lock order. Atomic UPDATE in SQLite + cache
// mutation under mu. If the target id doesn't exist, returns nil (no-op);
// callers that need strict "must exist" semantics should Get first.
//
// Contract: never fails the originating Store call. Callers log Warn on
// error and continue — this is a best-effort observability counter, not
// a correctness invariant.
func (ms *Store) IncrementReferencedByCount(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()
	return ms.incrementReferencedByCountLocked(ctx, id)
}

// incrementReferencedByCountLocked performs the increment assuming the caller
// already holds ms.writeMu. Kept separate so future callers that hold the
// lock for a larger operation can avoid re-entering it.
func (ms *Store) incrementReferencedByCountLocked(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}

	var metadataJSON sql.NullString
	err := ms.db.QueryRowContext(ctx,
		"SELECT metadata FROM memories WHERE id = ?", id,
	).Scan(&metadataJSON)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}

	metadata := map[string]string{}
	if metadataJSON.Valid && metadataJSON.String != "" && metadataJSON.String != "null" {
		if err := json.Unmarshal([]byte(metadataJSON.String), &metadata); err != nil {
			return fmt.Errorf("unmarshal metadata: %w", err)
		}
	}
	// json.Unmarshal of "null" on a map pointer resets the map to nil; guard
	// against a nil map leaking past this point in any case.
	if metadata == nil {
		metadata = map[string]string{}
	}

	current := referencedByCountFromMetadata(metadata)
	newCount := current + 1
	metadata[MetadataReferencedByCount] = strconv.Itoa(newCount)

	newJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	if _, err := ms.db.ExecContext(ctx,
		"UPDATE memories SET metadata = ?, updated_at = ? WHERE id = ?",
		string(newJSON), time.Now(), id,
	); err != nil {
		return fmt.Errorf("update metadata: %w", err)
	}

	// Cache: cachedMemory does not store the raw Metadata map, so nothing
	// to mutate here. Get() always re-reads from DB, and Recall()-side
	// sediment rule consumption goes through Decide(m, policy) on a fresh
	// *Memory struct. Refresh UpdatedAt under mu so cached timestamp
	// matches DB for callers that snapshot via the cache.
	ms.mu.Lock()
	if cm, ok := ms.memories[id]; ok && cm != nil {
		cm.UpdatedAt = time.Now()
	}
	ms.mu.Unlock()

	return nil
}

// referencedByCountFromMetadata reads the optional referenced_by_count key.
// Missing/invalid values produce 0 so the counter is monotonic increment-safe.
func referencedByCountFromMetadata(metadata map[string]string) int {
	if len(metadata) == 0 {
		return 0
	}
	raw := strings.TrimSpace(metadata[MetadataReferencedByCount])
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// RecountReferencesResult is the outcome of a RecountReferences pass.
// Counts only contains entries whose stored value changed (or was missing
// when it should not be); idempotent re-runs yield Updated=0.
type RecountReferencesResult struct {
	Scanned int            `json:"scanned"`
	Updated int            `json:"updated"`
	Counts  map[string]int `json:"counts,omitempty"` // memoryID → new count (only changed ones)
	DryRun  bool           `json:"dry_run"`
}

// RecountReferences scans all memories, recomputes referenced_by_count from
// scratch (avoided_dead_end_id metadata + superseded_by column), and writes
// the updated counts to metadata. Run once per environment to bootstrap
// counters from existing data. Idempotent: re-running yields Updated=0 once
// counters match the derived tally.
//
// Edges counted (must stay in sync with the live-path increment triggers):
//   - metadata["avoided_dead_end_id"] → target dead-end memory
//   - memories.superseded_by column → target (successor) memory
//
// Edges explicitly NOT counted (documented as known limitations):
//   - memories.replaces column (inverse of superseded_by, would double-count)
//   - any other free-form cross-references in content/metadata
func (ms *Store) RecountReferences(ctx context.Context, dryRun bool) (*RecountReferencesResult, error) {
	// Phase 1: build tally without taking writeMu (SELECT only).
	rows, err := ms.db.QueryContext(ctx,
		`SELECT id, metadata, superseded_by FROM memories`,
	)
	if err != nil {
		return nil, fmt.Errorf("scan memories: %w", err)
	}

	type scanned struct {
		id           string
		metadata     map[string]string
		currentCount int
	}
	allRows := make([]scanned, 0)
	tally := make(map[string]int)

	for rows.Next() {
		var id string
		var metadataJSON sql.NullString
		var supersededBy sql.NullString
		if err := rows.Scan(&id, &metadataJSON, &supersededBy); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan row: %w", err)
		}
		metadata := map[string]string{}
		if metadataJSON.Valid && metadataJSON.String != "" && metadataJSON.String != "null" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &metadata); err != nil {
				// Skip unparseable metadata — it cannot contribute to tally
				// and we do not want to fail the whole recount.
				ms.logger.Warn("recount: skipping unparseable metadata",
					zap.String("id", id))
				continue
			}
		}
		if metadata == nil {
			metadata = map[string]string{}
		}
		allRows = append(allRows, scanned{id: id, metadata: metadata, currentCount: referencedByCountFromMetadata(metadata)})

		if avoided := strings.TrimSpace(metadata["avoided_dead_end_id"]); avoided != "" {
			tally[avoided]++
		}
		if supersededBy.Valid {
			if target := strings.TrimSpace(supersededBy.String); target != "" {
				tally[target]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	_ = rows.Close()

	// Phase 2: compare tally with stored values; collect deltas.
	// We write the tally for ALL memories that have a derived count > 0
	// AND stored count differs, plus memories with stored count > 0 that
	// are NOT in the tally (stale counters — zero out so the field stays
	// accurate). For the initial backfill this is equivalent to: any
	// memory whose stored count doesn't match the derived count gets
	// rewritten.
	//
	// We store "0" explicitly when clearing a stale counter so the key
	// round-trips as authoritative evidence that recount has run. Simpler
	// than removing the key.
	changes := make(map[string]int)
	for _, r := range allRows {
		derived := tally[r.id]
		if derived == r.currentCount {
			continue
		}
		changes[r.id] = derived
	}

	result := &RecountReferencesResult{
		Scanned: len(allRows),
		DryRun:  dryRun,
	}
	if len(changes) == 0 {
		return result, nil
	}

	if dryRun {
		result.Updated = len(changes)
		result.Counts = changes
		return result, nil
	}

	// Phase 3: write changes under writeMu, one row at a time, each under
	// a fresh SQL UPDATE. We intentionally do NOT batch in a transaction
	// because the set can be large and we prefer incremental progress.
	ms.writeMu.Lock()
	defer ms.writeMu.Unlock()

	applied := make(map[string]int, len(changes))
	for _, r := range allRows {
		newCount, ok := changes[r.id]
		if !ok {
			continue
		}
		updated := copyMetadata(r.metadata)
		if updated == nil {
			updated = make(map[string]string, 1)
		}
		if newCount == 0 {
			updated[MetadataReferencedByCount] = "0"
		} else {
			updated[MetadataReferencedByCount] = strconv.Itoa(newCount)
		}
		newJSON, err := json.Marshal(updated)
		if err != nil {
			ms.logger.Warn("recount: marshal failed",
				zap.String("id", r.id))
			continue
		}
		if _, err := ms.db.ExecContext(ctx,
			"UPDATE memories SET metadata = ?, updated_at = ? WHERE id = ?",
			string(newJSON), time.Now(), r.id,
		); err != nil {
			ms.logger.Warn("recount: update failed",
				zap.String("id", r.id))
			continue
		}
		applied[r.id] = newCount
	}

	// Cache: refresh UpdatedAt for each touched id. Metadata is not
	// cached, so no further cache mutation is required.
	if len(applied) > 0 {
		now := time.Now()
		ms.mu.Lock()
		for id := range applied {
			if cm, ok := ms.memories[id]; ok && cm != nil {
				cm.UpdatedAt = now
			}
		}
		ms.mu.Unlock()
	}

	result.Updated = len(applied)
	result.Counts = applied
	return result, nil
}
