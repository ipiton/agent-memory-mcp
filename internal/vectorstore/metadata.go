package vectorstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrMetadataNotFound is returned by GetMetadata when the key does not exist.
var ErrMetadataNotFound = errors.New("metadata key not found")

// === Metadata Methods ===

// GetMetadata retrieves a metadata value by key from the index_metadata table.
// Returns ErrMetadataNotFound if the key does not exist.
func (s *SQLiteStore) GetMetadata(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM index_metadata WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", ErrMetadataNotFound
	}
	return value, err
}

// SetMetadata stores a key-value pair in the index_metadata table.
func (s *SQLiteStore) SetMetadata(key, value string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO index_metadata (key, value) VALUES (?, ?)
	`, key, value)
	return err
}

// CommitIndexState applies metadata and indexed file changes atomically.
func (s *SQLiteStore) CommitIndexState(update IndexStateUpdate) error {
	for _, info := range update.UpsertFiles {
		if info == nil {
			return fmt.Errorf("indexed file update contains nil entry")
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin index state transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for key, value := range update.Metadata {
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO index_metadata (key, value) VALUES (?, ?)
		`, key, value); err != nil {
			return fmt.Errorf("failed to persist metadata %q: %w", key, err)
		}
	}

	for _, filePath := range update.DeleteFilePaths {
		if _, err := tx.Exec("DELETE FROM indexed_files WHERE file_path = ?", filePath); err != nil {
			return fmt.Errorf("failed to delete indexed file metadata for %q: %w", filePath, err)
		}
	}

	for _, info := range update.UpsertFiles {
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO indexed_files (file_path, hash, mod_time, size, chunk_count)
			VALUES (?, ?, ?, ?, ?)
		`, info.FilePath, info.Hash, info.ModTime, info.Size, info.ChunkCount); err != nil {
			return fmt.Errorf("failed to upsert indexed file metadata for %q: %w", info.FilePath, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit index state transaction: %w", err)
	}

	return nil
}

// === Indexed Files Methods ===

// GetIndexedFile retrieves indexing metadata for a file, or nil if not indexed.
func (s *SQLiteStore) GetIndexedFile(filePath string) (*IndexedFileInfo, error) {
	var info IndexedFileInfo
	var modTime sql.NullTime
	err := s.db.QueryRow(`
		SELECT file_path, hash, mod_time, size, chunk_count
		FROM indexed_files WHERE file_path = ?
	`, filePath).Scan(&info.FilePath, &info.Hash, &modTime, &info.Size, &info.ChunkCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if modTime.Valid {
		info.ModTime = modTime.Time
	}
	return &info, nil
}

// SetIndexedFile stores or updates indexing metadata for a file.
func (s *SQLiteStore) SetIndexedFile(info *IndexedFileInfo) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO indexed_files (file_path, hash, mod_time, size, chunk_count)
		VALUES (?, ?, ?, ?, ?)
	`, info.FilePath, info.Hash, info.ModTime, info.Size, info.ChunkCount)
	return err
}

// DeleteIndexedFile removes indexing metadata for a file.
func (s *SQLiteStore) DeleteIndexedFile(filePath string) error {
	_, err := s.db.Exec("DELETE FROM indexed_files WHERE file_path = ?", filePath)
	return err
}

// GetAllIndexedFiles returns indexing metadata for all tracked files.
func (s *SQLiteStore) GetAllIndexedFiles() (map[string]*IndexedFileInfo, error) {
	rows, err := s.db.Query("SELECT file_path, hash, mod_time, size, chunk_count FROM indexed_files")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]*IndexedFileInfo)
	for rows.Next() {
		var info IndexedFileInfo
		var modTime sql.NullTime
		if err := rows.Scan(&info.FilePath, &info.Hash, &modTime, &info.Size, &info.ChunkCount); err != nil {
			continue
		}
		if modTime.Valid {
			info.ModTime = modTime.Time
		}
		result[info.FilePath] = &info
	}
	return result, rows.Err()
}
