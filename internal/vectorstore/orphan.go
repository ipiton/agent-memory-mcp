package vectorstore

import (
	"fmt"

	"go.uber.org/zap"
)

// CleanOrphans removes chunks that are not tracked by indexed_files or whose index
// exceeds the expected chunk_count. This handles stale chunks left behind when
// removeDocument fails during re-indexing (the error is warn-only) and
// CommitIndexState marks the file as indexed with the new hash — subsequent runs
// never retry cleanup, leaving orphans permanently.
// Returns the number of removed chunks.
func (s *SQLiteStore) CleanOrphans() (int, error) {
	indexedFiles, err := s.GetAllIndexedFiles()
	if err != nil {
		return 0, fmt.Errorf("failed to get indexed files: %w", err)
	}
	if len(indexedFiles) == 0 {
		return 0, nil
	}

	// Build the set of chunk IDs that should exist.
	expectedIDs := make(map[string]struct{})
	for _, info := range indexedFiles {
		for i := range info.ChunkCount {
			expectedIDs[fmt.Sprintf("%s-%d", info.FilePath, i)] = struct{}{}
		}
	}

	// Collect orphans: present in memory but not in expectedIDs.
	s.mu.RLock()
	var orphanIDs []string
	for id := range s.chunks {
		if _, ok := expectedIDs[id]; !ok {
			orphanIDs = append(orphanIDs, id)
		}
	}
	s.mu.RUnlock()

	if len(orphanIDs) == 0 {
		return 0, nil
	}

	// Delete from DB in a single transaction.
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin orphan cleanup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare("DELETE FROM chunks WHERE id = ?")
	if err != nil {
		return 0, fmt.Errorf("failed to prepare orphan delete statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, id := range orphanIDs {
		if _, err := stmt.Exec(id); err != nil {
			return 0, fmt.Errorf("failed to delete orphan chunk %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit orphan cleanup: %w", err)
	}

	// Remove from in-memory caches.
	s.mu.Lock()
	for _, id := range orphanIDs {
		s.removeChunkKeywordsLocked(id)
		delete(s.chunks, id)
	}
	s.mu.Unlock()

	s.logger.Info("Cleaned orphan chunks", zap.Int("count", len(orphanIDs)))
	return len(orphanIDs), nil
}
