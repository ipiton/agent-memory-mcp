package memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// TestStoreReadAfterWriteRecall is the T75 happy path: a stored memory is
// immediately queryable via the consumer read paths (Get + Recall), proving the
// write actually landed rather than being trusted on the call's success status.
func TestStoreReadAfterWriteRecall(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "m.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := &Memory{Content: "deploy rollback checklist", Type: TypeProcedural, Importance: 0.8}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get after write: %v", err)
	}
	if got.Content != m.Content {
		t.Fatalf("read-back content = %q, want %q", got.Content, m.Content)
	}

	results, err := store.Recall(context.Background(), "deploy rollback", Filters{}, 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("immediate recall returned nothing after a successful write")
	}
}

// TestVerifyMemoryPersistedVeto covers the read-after-write veto directly: a
// persisted id verifies clean, a missing id is rejected (the guard that turns a
// silent write loss into an explicit error).
func TestVerifyMemoryPersistedVeto(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "m.db"), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := &Memory{Content: "x", Type: TypeSemantic, Importance: 0.5}
	if err := store.Store(context.Background(), m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if err := verifyMemoryPersisted(store.db, m.ID); err != nil {
		t.Fatalf("verify persisted id: %v", err)
	}
	if err := verifyMemoryPersisted(store.db, "does-not-exist"); err == nil {
		t.Fatal("verifyMemoryPersisted must error for a missing id")
	}
}

// zeroRowsExecutor simulates a driver that reports success but writes no rows
// (e.g. an INSERT OR IGNORE that silently drops on collision).
type zeroRowsExecutor struct{}

func (zeroRowsExecutor) Exec(string, ...any) (sql.Result, error) { return zeroRowsResult{}, nil }

type zeroRowsResult struct{}

func (zeroRowsResult) LastInsertId() (int64, error) { return 0, nil }
func (zeroRowsResult) RowsAffected() (int64, error) { return 0, nil }

// TestInsertMemoryRowVetoesSilentNoOp is the "artificial write-fail" case (T75):
// when Exec succeeds but affects zero rows, insertMemoryRow must return an error
// instead of reporting a false success.
func TestInsertMemoryRowVetoesSilentNoOp(t *testing.T) {
	m := &Memory{ID: "abc", Content: "c", Type: TypeSemantic, Importance: 0.5}
	if err := insertMemoryRow(zeroRowsExecutor{}, m); err == nil {
		t.Fatal("insertMemoryRow must error when the write affects 0 rows (silent no-op)")
	}
}
