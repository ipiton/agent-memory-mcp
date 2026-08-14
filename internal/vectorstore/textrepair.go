package vectorstore

import (
	"database/sql"
	"fmt"
	"unicode/utf8"
)

// T118: chunks byte-truncated mid-rune by the pre-fix splitter.
//
// This is T87's defect on the RAG side, but not T87's repair. A memory row
// could only be salvaged in place, so T87 rewrote the bytes with U+FFFD. A
// chunk has a source file behind it, so the correct text is still on disk and
// the honest repair is to re-chunk the document — sanitizing here would freeze
// a replacement character where a real letter belongs and make the chunk look
// healthy forever.
//
// So the repair invalidates the affected documents' stored hash. The indexer
// compares that hash against the file's (indexer.go: `indexed.Hash != fileHash`)
// and re-chunks on mismatch, with the rune-aware splitter.
//
// 🔴 Invalidating the hash, not deleting the indexed_files row. Deleting it
// makes the document unknown, and CleanOrphans — which runs right after this at
// startup — then drops every chunk whose file is not tracked. The first version
// of this repair did exactly that and took 746 documents out of search until
// someone happened to re-index. Keeping the row keeps the old chunks answering
// queries (sanitized by loadChunksToMemory) until the new ones replace them.
//
// Gated on PRAGMA user_version so it runs once.

// utf8ChunkRepairVersion is the PRAGMA user_version stamped once the T118 sweep
// has run. The vector store had no schema version before this (the live index
// read 0), so 1 is the first value it takes.
const utf8ChunkRepairVersion = 1

// markInvalidUTF8DocumentsForReindexOnce forces a re-chunk of every document
// holding an invalid-UTF-8 chunk, once. Returns the number of documents marked.
func markInvalidUTF8DocumentsForReindexOnce(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, err
	}
	if version >= utf8ChunkRepairVersion {
		return 0, nil
	}

	marked, err := markInvalidUTF8DocumentsForReindex(db)
	if err != nil {
		return 0, err
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, utf8ChunkRepairVersion)); err != nil {
		return marked, err
	}
	return marked, nil
}

// markInvalidUTF8DocumentsForReindex clears the stored hash of documents with at
// least one invalid-UTF-8 chunk, so the indexer sees a mismatch and re-chunks
// them. It collects the paths and closes the cursor before writing: modernc
// SQLite cannot write while a SELECT cursor is open on the same connection (the
// same constraint T87 hit).
func markInvalidUTF8DocumentsForReindex(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT doc_path, content, title FROM chunks`)
	if err != nil {
		return 0, err
	}
	affected := make(map[string]struct{})
	for rows.Next() {
		var docPath, content string
		var title sql.NullString
		if err := rows.Scan(&docPath, &content, &title); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if utf8.ValidString(content) && (!title.Valid || utf8.ValidString(title.String)) {
			continue
		}
		affected[docPath] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(affected) == 0 {
		return 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`UPDATE indexed_files SET hash = '' WHERE file_path = ?`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer func() { _ = stmt.Close() }()
	for docPath := range affected {
		if _, err := stmt.Exec(docPath); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(affected), nil
}
