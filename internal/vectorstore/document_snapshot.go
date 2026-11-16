package vectorstore

import (
	"context"
	"database/sql"
	"fmt"
)

// SourcePathMetadataPrefix stores the resolved physical source for a logical
// document path. Resolve once at indexing time, never when reading provenance.
const SourcePathMetadataPrefix = "source_path:"

// DocumentSnapshot binds indexed text and vectors to the provenance and model
// metadata from the same SQLite read transaction.
type DocumentSnapshot struct {
	Chunks   []Chunk
	Metadata map[string]string
}

func (s *SQLiteStore) ReadDocumentSnapshot(ctx context.Context, docPath string) (*DocumentSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin document snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	metadata, err := tx.QueryContext(ctx, "SELECT key,value FROM index_metadata WHERE key IN ('index_root','index_state','embedding_model','keyword_only',?)", SourcePathMetadataPrefix+docPath)
	if err != nil {
		return nil, err
	}
	result := &DocumentSnapshot{Metadata: map[string]string{}}
	for metadata.Next() {
		var key, value string
		if err := metadata.Scan(&key, &value); err != nil {
			_ = metadata.Close()
			return nil, err
		}
		result.Metadata[key] = value
	}
	readErr, closeErr := metadata.Err(), metadata.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, doc_path, content, title, last_modified, embedding FROM chunks WHERE doc_path = ?`, docPath)
	if err != nil {
		return nil, err
	}
	result.Chunks, readErr = s.scanChunks(rows)
	closeErr = rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
