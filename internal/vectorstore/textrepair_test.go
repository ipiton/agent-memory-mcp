package vectorstore

import (
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

// T118: a chunk cut mid-rune must (1) not reach search as invalid UTF-8 and
// (2) get its document queued for re-chunking, because unlike a memory row the
// correct text is still on disk and sanitizing would freeze U+FFFD in place.
func TestInvalidUTF8ChunkQueuesDocumentForReindex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")

	store, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	// "значение" cut after the lead byte of its last rune — the exact shape the
	// live index carried 2622 times.
	broken := "[doc > section]\n\nзначени\xd0"
	if utf8.ValidString(broken) {
		t.Fatal("fixture is valid UTF-8; the test would prove nothing")
	}
	if err := store.Upsert([]Chunk{{
		ID:           "docs/architecture.md-0",
		DocPath:      "docs/architecture.md",
		Content:      broken,
		Title:        "Архитектура",
		LastModified: time.Now(),
		Embedding:    []float32{0.1, 0.2, 0.3},
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.SetIndexedFile(&IndexedFileInfo{
		FilePath:   "docs/architecture.md",
		Hash:       "deadbeef",
		ModTime:    time.Now(),
		Size:       100,
		ChunkCount: 1,
	}); err != nil {
		t.Fatalf("SetIndexedFile: %v", err)
	}
	// The fixture was written by this binary, so the sweep already stamped
	// user_version on the empty database. Reset it to what a store built before
	// the rune fix actually carries — the live index read 0. Same trick as the
	// T87 memory-side test, and for the same reason.
	if _, err := store.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if got := reopened.InvalidUTF8Chunks(); got != 1 {
		t.Errorf("InvalidUTF8Chunks() = %d, want 1", got)
	}
	chunks, err := reopened.ChunksByDocPath("docs/architecture.md")
	if err != nil {
		t.Fatalf("ChunksByDocPath: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	if !utf8.ValidString(chunks[0].Content) {
		t.Errorf("search still sees invalid UTF-8: %q", chunks[0].Content)
	}

	files, err := reopened.GetAllIndexedFiles()
	if err != nil {
		t.Fatalf("GetAllIndexedFiles: %v", err)
	}
	info, still := files["docs/architecture.md"]
	if !still {
		t.Fatal("document dropped from indexed_files — CleanOrphans would then take its chunks out of search until someone re-indexes")
	}
	if info.Hash != "" {
		t.Errorf("stored hash = %q, want cleared so the indexer re-chunks the document", info.Hash)
	}
}

// The sweep must run once: a healthy store reopened twice must not keep
// dropping bookkeeping.
func TestValidChunksLeaveIndexedFilesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")

	store, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.Upsert([]Chunk{{
		ID:        "docs/ok.md-0",
		DocPath:   "docs/ok.md",
		Content:   "[doc > section]\n\nполностью валидный текст",
		Title:     "Заголовок",
		Embedding: []float32{0.1, 0.2, 0.3},
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.SetIndexedFile(&IndexedFileInfo{
		FilePath: "docs/ok.md", Hash: "cafe", ModTime: time.Now(), Size: 10, ChunkCount: 1,
	}); err != nil {
		t.Fatalf("SetIndexedFile: %v", err)
	}
	_ = store.Close()

	reopened, err := NewSQLiteStore(path, 3, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if got := reopened.InvalidUTF8Chunks(); got != 0 {
		t.Errorf("InvalidUTF8Chunks() = %d, want 0", got)
	}
	files, err := reopened.GetAllIndexedFiles()
	if err != nil {
		t.Fatalf("GetAllIndexedFiles: %v", err)
	}
	if _, ok := files["docs/ok.md"]; !ok {
		t.Error("a healthy document lost its indexed_files row")
	}
}
