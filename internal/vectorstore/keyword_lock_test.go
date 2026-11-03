package vectorstore

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/dbutil"
	"go.uber.org/zap"
)

const lockHolderEnv = "AMM_TEST_HOLD_WRITE_LOCK"

// TestMain doubles as the second process of the lock tests: a test re-executes
// this binary with the env var set, and it holds the write lock until killed.
func TestMain(m *testing.M) {
	if path := os.Getenv(lockHolderEnv); path != "" {
		holdWriteLock(path)
		return
	}
	os.Exit(m.Run())
}

func holdWriteLock(path string) {
	db, err := dbutil.OpenSQLite(path, zap.NewNop())
	if err != nil {
		panic(err)
	}
	tx, err := db.Begin()
	if err != nil {
		panic(err)
	}
	defer func() { _ = tx.Rollback() }()
	os.Stdout.WriteString("locked\n")
	// Exits with the parent's pipe, so a failed test leaves no process behind.
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// startLockHolder returns once another process owns the write lock of path. The
// returned func releases it by ending that process.
func startLockHolder(t *testing.T, path string) (release func()) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0])
	cmd.Env = append(os.Environ(), lockHolderEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("lock holder did not start: %q, %v", line, err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		_ = stdin.Close()
		_ = cmd.Wait()
	}
	t.Cleanup(release)
	return release
}

// A database as an upgrade finds it: chunks present, keyword table not filled.
func newUnindexedDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vectors.db")
	s, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Upsert([]Chunk{
		{ID: "a", DocPath: "a.md", Content: "rollback ingress safely", Embedding: []float32{1, 0, 0}, LastModified: time.Now()},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM ` + ftsTable); err != nil {
		t.Fatalf("clear keyword index: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM index_metadata WHERE key = ?`, keywordIndexVersionKey); err != nil {
		t.Fatalf("clear version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// T174. Ten attempts of busy_timeout each came to 50 s. Another process's build
// took longer on a loaded machine, and the store then refused every keyword search
// until a restart, though the index it waited for was complete by then.
func TestKeywordBuildOutlastsForeignWriteLock(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a lock past the 50 s the build used to give up after")
	}
	t.Parallel()
	path := newUnindexedDatabase(t)
	release := startLockHolder(t, path)

	s, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open while another process holds the write lock: %v", err)
	}
	defer func() { _ = s.Close() }()

	// The search answers at once and does not pass the wait off as an empty result.
	began := time.Now()
	if _, err := s.KeywordSearch("ingress", 10); !errors.Is(err, ErrKeywordIndexBuilding) {
		t.Fatalf("search during the build: err = %v, want ErrKeywordIndexBuilding", err)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("search during the build took %v", took)
	}

	select {
	case <-s.ftsReady:
		t.Fatalf("build ended while the lock was held: %v", s.ftsErr)
	case <-time.After(55 * time.Second):
	}

	release()
	select {
	case <-s.ftsReady:
	case <-time.After(30 * time.Second):
		t.Fatal("build did not finish after the lock was released")
	}
	if ids := keywordIDs(t, s, "ingress"); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("keyword search after the lock was released = %v, want [a]", ids)
	}
}

// Close must not sit out a lock it may never get: the service has seconds to stop.
func TestCloseStopsKeywordBuildWaitingForLock(t *testing.T) {
	t.Parallel()
	path := newUnindexedDatabase(t)
	startLockHolder(t, path)

	s, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	began := time.Now()
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// One busy_timeout at most, the attempt in progress finishes it.
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("close took %v", took)
	}
	if !errors.Is(s.waitKeywordIndex(), errKeywordBuildStopped) {
		t.Fatalf("waiting writers got %v, want errKeywordBuildStopped", s.ftsErr)
	}
}

// Anything but a held lock will not cure itself, so it must not be retried forever.
func TestKeywordBuildGivesUpOnOtherErrors(t *testing.T) {
	path := newUnindexedDatabase(t)
	s, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	<-s.ftsReady
	_ = s.db.Close()

	s.ftsReady = make(chan struct{})
	s.backfill.Add(1)
	s.runKeywordBackfill()
	if _, err := s.KeywordSearch("ingress", 10); err == nil || errors.Is(err, ErrKeywordIndexBuilding) {
		t.Fatalf("search after a failed build: err = %v, want the failure", err)
	}
}
