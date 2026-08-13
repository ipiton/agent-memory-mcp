// Package dbutil centralises the SQLite Open + PRAGMA setup shared between
// memory store and vector store.
//
// History (see 06-planning/2026-05-05-sqlite-busy-incident.md): a 25h
// SQLITE_BUSY stall on index_documents was traced to rollback-journal mode
// silently keeping the DB under an exclusive write lock. The first fix applied
// the pragmas via explicit db.Exec after Open — but busy_timeout is a
// PER-CONNECTION setting, and a single db.Exec only configures whichever
// pooled connection happened to serve it. New connections the pool opened for
// concurrent operations defaulted to busy_timeout=0 and kept returning instant
// SQLITE_BUSY under writer contention (file_watcher index racing a foreground
// index_documents).
//
// OpenSQLite now passes the pragmas through the DSN _pragma parameter, which
// modernc.org/sqlite runs as "PRAGMA ..." on EVERY new pool connection
// (Driver.Open). _txlock=immediate makes write transactions acquire the write
// lock at BEGIN so busy_timeout actually covers writer contention — SQLite does
// not invoke the busy handler when a deferred transaction fails to upgrade a
// read lock to a write lock, it returns SQLITE_BUSY immediately.
package dbutil

import (
	"database/sql"
	"fmt"
	"net/url"

	"go.uber.org/zap"
	_ "modernc.org/sqlite" // SQLite driver
)

// buildDSN appends the standard PRAGMA + txlock query parameters to a plain
// SQLite file path. The parameters are applied per-connection by the driver,
// so every connection in the database/sql pool gets busy_timeout, WAL and
// synchronous=NORMAL — not just the one that served an initial Exec.
func buildDSN(dbPath string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	return dbPath + "?" + q.Encode()
}

// buildReadOnlyDSN is the reader's variant: busy_timeout still matters (a
// reader contends with the writer's checkpoint), but journal_mode is not set —
// switching the journal is a write, and a read-only connection reads a WAL
// database perfectly well without asking for it. The file: prefix is required
// for the driver to honour mode=ro.
func buildReadOnlyDSN(dbPath string) string {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + dbPath + "?" + q.Encode()
}

// OpenSQLite opens a SQLite database at dbPath with the standard pragmas
// (busy_timeout=5000, journal_mode=WAL, synchronous=NORMAL) applied to every
// pooled connection via the DSN, and BEGIN IMMEDIATE transaction locking.
// Returns an error if the database cannot be opened. Logs a warning (logger
// may be nil) if WAL mode was requested but the driver fell back to a
// different journal mode.
func OpenSQLite(dbPath string, logger *zap.Logger) (*sql.DB, error) {
	db, err := sql.Open("sqlite", buildDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}

	// Force a real connection (sql.Open is lazy) so an unusable path fails
	// here, and verify WAL actually engaged — rollback-journal mode is the
	// failure that caused the original incident.
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify journal_mode on %s: %w", dbPath, err)
	}
	if mode != "wal" && logger != nil {
		logger.Warn("SQLite did not switch to WAL journal_mode",
			zap.String("requested", "wal"),
			zap.String("actual", mode),
		)
	}

	return db, nil
}

// OpenSQLiteReadOnly opens dbPath for reading only, with busy_timeout applied
// per pooled connection.
//
// T89 M11: the hooks context-inject path used to build its DSN by string
// concatenation — `path + "?_journal_mode=WAL&mode=ro"`. modernc.org/sqlite
// does not recognise `_journal_mode` (its knob is the `_pragma` parameter), so
// that DSN set no journal mode and, more importantly, no busy_timeout: the
// reader returned SQLITE_BUSY instantly whenever it met a writer. That is the
// exact shape of the incident that produced this package.
func OpenSQLiteReadOnly(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", buildReadOnlyDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", dbPath, err)
	}
	// sql.Open is lazy; force a connection so a bad path fails here.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s read-only: %w", dbPath, err)
	}
	return db, nil
}
