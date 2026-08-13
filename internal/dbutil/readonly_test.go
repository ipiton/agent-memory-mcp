package dbutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// T89 M11. The hooks context-inject path built its DSN by hand as
// `path + "?_journal_mode=WAL&mode=ro"`. modernc.org/sqlite's knob is `_pragma`,
// so `_journal_mode` was ignored and — the part that matters — no busy_timeout
// was set at all: the reader returned SQLITE_BUSY the instant it met a writer,
// which is the incident this package exists to prevent.
func TestReadOnlyDSNCarriesBusyTimeout(t *testing.T) {
	dsn := buildReadOnlyDSN("/tmp/example.db")

	if !strings.HasPrefix(dsn, "file:") {
		t.Errorf("dsn = %q, want a file: URI so mode=ro is honoured", dsn)
	}
	if !strings.Contains(dsn, "mode=ro") {
		t.Errorf("dsn = %q, want mode=ro", dsn)
	}
	if !strings.Contains(dsn, "busy_timeout") {
		t.Errorf("dsn = %q, want a busy_timeout pragma", dsn)
	}
	if strings.Contains(dsn, "_journal_mode") {
		t.Errorf("dsn = %q still uses the _journal_mode spelling this driver ignores", dsn)
	}
}

func TestOpenSQLiteReadOnlyReadsAndRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	rw, err := OpenSQLite(path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := rw.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO t (v) VALUES ('hello')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	ro, err := OpenSQLiteReadOnly(path)
	if err != nil {
		t.Fatalf("OpenSQLiteReadOnly: %v", err)
	}
	defer func() { _ = ro.Close() }()

	var v string
	if err := ro.QueryRow(`SELECT v FROM t LIMIT 1`).Scan(&v); err != nil {
		t.Fatalf("read через read-only connection: %v", err)
	}
	if v != "hello" {
		t.Fatalf("v = %q, want hello", v)
	}

	if _, err := ro.Exec(`INSERT INTO t (v) VALUES ('nope')`); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
}

func TestOpenSQLiteReadOnlyFailsOnMissingPath(t *testing.T) {
	if _, err := OpenSQLiteReadOnly(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Fatal("OpenSQLiteReadOnly succeeded on a non-existent database")
	}
}
