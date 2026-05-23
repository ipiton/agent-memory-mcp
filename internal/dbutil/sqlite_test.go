package dbutil

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenSQLite_AppliesWALAndBusyTimeout(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := OpenSQLite(dbPath, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var timeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}

	var sync int
	if err := db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("query synchronous: %v", err)
	}
	// NORMAL == 1 in SQLite enum.
	if sync != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", sync)
	}
}

// TestOpenSQLite_BusyTimeoutPerConnection guards the regression that caused the
// SQLITE_BUSY recurrence: busy_timeout is a per-connection setting, so it must
// be applied to every pooled connection, not just the first one. We pin two
// connections open simultaneously to force the pool to hand out a second
// connection, then assert busy_timeout holds on it.
func TestOpenSQLite_BusyTimeoutPerConnection(t *testing.T) {
	dir := t.TempDir()
	db, err := OpenSQLite(filepath.Join(dir, "test.db"), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	c1, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn1: %v", err)
	}
	defer func() { _ = c1.Close() }()
	c2, err := db.Conn(ctx) // distinct connection while c1 is held
	if err != nil {
		t.Fatalf("conn2: %v", err)
	}
	defer func() { _ = c2.Close() }()

	for i, c := range []*sql.Conn{c1, c2} {
		var timeout int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatalf("conn%d busy_timeout: %v", i+1, err)
		}
		if timeout != 5000 {
			t.Errorf("conn%d busy_timeout = %d, want 5000", i+1, timeout)
		}
	}
}

func TestOpenSQLite_BadPath(t *testing.T) {
	// "/" is a directory — sql.Open succeeds (lazy), but the first PRAGMA exec must fail.
	_, err := OpenSQLite("/", nil)
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}
