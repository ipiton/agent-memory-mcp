package steward

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// TestDriftScanSourceChangedJudgesContent pins T150: a file rewritten with the
// same bytes (git checkout, touch) is not drift, a file whose bytes changed is.
func TestDriftScanSourceChangedJudgesContent(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	root := t.TempDir()
	src := filepath.Join(root, "docs", "timeout.md")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("timeout: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mem := &memory.Memory{
		Content:    "Timeout is set in ./docs/timeout.md to 30s",
		Type:       memory.TypeSemantic,
		Title:      "Timeout config",
		Importance: 0.7,
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatal(err)
	}
	if err := svc.VerifyEntry(ctx, VerifyParams{
		MemoryID: mem.ID, Method: VerifySourceCheck, Status: StatusVerified, RootPath: root,
	}); err != nil {
		t.Fatal(err)
	}

	later := time.Now().Add(time.Hour)
	sourceChanged := func() bool {
		t.Helper()
		if err := os.Chtimes(src, later, later); err != nil {
			t.Fatal(err)
		}
		result, err := svc.DriftScan(ctx, DriftScanParams{RootPath: root})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range result.Findings {
			if f.MemoryID == mem.ID && f.DriftType == DriftSourceChanged {
				return true
			}
		}
		return false
	}

	if sourceChanged() {
		t.Error("same content with a newer mtime reported as source_changed")
	}

	if err := os.WriteFile(src, []byte("timeout: 60s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !sourceChanged() {
		t.Error("changed content with a newer mtime not reported as source_changed")
	}
}

// TestDriftScanSourceChangedWithoutHashesKeepsMtime: a record verified before
// T150 carries no hashes, so a newer mtime still reports drift.
func TestDriftScanSourceChangedWithoutHashesKeepsMtime(t *testing.T) {
	store := newTestStore(t)
	svc := newTestService(t, store)
	ctx := context.Background()

	root := t.TempDir()
	src := filepath.Join(root, "docs", "timeout.md")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("timeout: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, later, later); err != nil {
		t.Fatal(err)
	}

	mem := &memory.Memory{
		Content:    "Timeout is set in ./docs/timeout.md to 30s",
		Type:       memory.TypeSemantic,
		Title:      "Timeout config",
		Importance: 0.7,
	}
	if err := store.Store(ctx, mem); err != nil {
		t.Fatal(err)
	}

	result, err := svc.DriftScan(ctx, DriftScanParams{RootPath: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range result.Findings {
		if f.MemoryID == mem.ID && f.DriftType == DriftSourceChanged {
			return
		}
	}
	t.Error("record without hashes and a newer mtime not reported as source_changed")
}
