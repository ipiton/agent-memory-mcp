// Package dbutil centralises the SQLite Open + PRAGMA setup shared between
// memory store and vector store. The connection-string PRAGMAs of
// modernc.org/sqlite are unreliable (see 06-planning/2026-05-05-sqlite-busy-incident.md):
// vectors.db-journal was observed on disk despite ?_journal_mode=WAL in the DSN,
// meaning rollback-journal mode silently kept the database under exclusive
// write lock and caused a 25h SQLITE_BUSY stall on index_documents.
//
// OpenSQLite applies the pragmas explicitly via PRAGMA statements after Open
// and verifies that journal_mode actually switched to WAL.
package dbutil

import (
	"database/sql"
	"fmt"

	"go.uber.org/zap"
	_ "modernc.org/sqlite" // SQLite driver
)

// OpenSQLite opens a SQLite database at dbPath and applies the standard
// pragmas: busy_timeout=5000, journal_mode=WAL, synchronous=NORMAL.
// Returns an error if the database cannot be opened or pragmas fail.
// Logs a warning (logger may be nil) if WAL mode was requested but the
// driver fell back to a different journal mode.
func OpenSQLite(dbPath string, logger *zap.Logger) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	if err := ApplyPragmas(db, logger); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ApplyPragmas runs the standard pragmas on an already-opened database.
// Exposed for tests that need a custom Open path.
func ApplyPragmas(db *sql.DB, logger *zap.Logger) error {
	// busy_timeout: queue concurrent writers up to 5s instead of returning
	// SQLITE_BUSY immediately on contention.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}

	// journal_mode=WAL: concurrent reader+writer; rollback journal locks
	// the entire database under any write transaction.
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return fmt.Errorf("set journal_mode=WAL: %w", err)
	}
	if mode != "wal" && logger != nil {
		logger.Warn("SQLite did not switch to WAL journal_mode",
			zap.String("requested", "wal"),
			zap.String("actual", mode),
		)
	}

	// synchronous=NORMAL: balanced durability when paired with WAL.
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		return fmt.Errorf("set synchronous=NORMAL: %w", err)
	}

	return nil
}
