package rag

import (
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

func TestCalculateFileHash(t *testing.T) {
	hash1 := calculateFileHash("hello world")
	hash2 := calculateFileHash("hello world")
	hash3 := calculateFileHash("different content")

	if hash1 != hash2 {
		t.Fatal("same content should produce same hash")
	}
	if hash1 == hash3 {
		t.Fatal("different content should produce different hash")
	}
	if len(hash1) != 64 {
		t.Fatalf("expected SHA-256 hex length 64, got %d", len(hash1))
	}
}

func TestCalculateIndexChanges_NewFiles(t *testing.T) {
	e := &Engine{}

	docs := []document{
		{ID: "1", Path: "a.md", Content: "aaa", FileHash: "h1", LastModified: time.Now()},
		{ID: "2", Path: "b.md", Content: "bbb", FileHash: "h2", LastModified: time.Now()},
	}
	indexedFiles := map[string]*vectorstore.IndexedFileInfo{}

	toAdd, toRemove := e.calculateIndexChanges(docs, indexedFiles, false)

	if len(toAdd) != 2 {
		t.Fatalf("expected 2 to add, got %d", len(toAdd))
	}
	if len(toRemove) != 0 {
		t.Fatalf("expected 0 to remove, got %d", len(toRemove))
	}
}

func TestCalculateIndexChanges_DeletedFiles(t *testing.T) {
	e := &Engine{}
	now := time.Now()

	docs := []document{
		{ID: "1", Path: "a.md", Content: "aaa", FileHash: "h1", LastModified: now},
	}
	indexedFiles := map[string]*vectorstore.IndexedFileInfo{
		"a.md": {FilePath: "a.md", Hash: "h1", ModTime: now}, // same hash and mod time
		"b.md": {FilePath: "b.md", Hash: "h2", ModTime: now}, // no longer in docs
	}

	toAdd, toRemove := e.calculateIndexChanges(docs, indexedFiles, false)

	// a.md is unchanged (same hash, same mod time), b.md was removed
	if len(toRemove) != 1 || toRemove[0] != "b.md" {
		t.Fatalf("expected to remove b.md, got %v", toRemove)
	}

	// a.md has same hash and mod time — should not be re-added
	for _, d := range toAdd {
		if d.Path == "a.md" {
			t.Fatal("unchanged a.md should not be in toAdd")
		}
	}
}

func TestCalculateIndexChanges_ChangedFiles(t *testing.T) {
	e := &Engine{}
	now := time.Now()

	docs := []document{
		{ID: "1", Path: "a.md", Content: "new content", FileHash: "new_hash", LastModified: now},
	}
	indexedFiles := map[string]*vectorstore.IndexedFileInfo{
		"a.md": {FilePath: "a.md", Hash: "old_hash", ModTime: now.Add(-time.Hour)},
	}

	toAdd, toRemove := e.calculateIndexChanges(docs, indexedFiles, false)

	if len(toAdd) != 1 {
		t.Fatalf("expected 1 changed file to add, got %d", len(toAdd))
	}
	if toAdd[0].Path != "a.md" {
		t.Fatalf("expected a.md, got %s", toAdd[0].Path)
	}
	if len(toRemove) != 0 {
		t.Fatalf("expected 0 to remove, got %d", len(toRemove))
	}
}

func TestCalculateIndexChanges_ForceRebuild(t *testing.T) {
	e := &Engine{}
	now := time.Now()

	docs := []document{
		{ID: "1", Path: "a.md", Content: "aaa", FileHash: "h1", LastModified: now},
		{ID: "2", Path: "b.md", Content: "bbb", FileHash: "h2", LastModified: now},
	}
	indexedFiles := map[string]*vectorstore.IndexedFileInfo{
		"a.md": {FilePath: "a.md", Hash: "h1", ModTime: now},
		"b.md": {FilePath: "b.md", Hash: "h2", ModTime: now},
	}

	// Without force — nothing should change since hashes match
	toAdd, toRemove := e.calculateIndexChanges(docs, indexedFiles, false)
	if len(toAdd) != 0 {
		t.Fatalf("without force: expected 0 to add, got %d", len(toAdd))
	}
	if len(toRemove) != 0 {
		t.Fatalf("without force: expected 0 to remove, got %d", len(toRemove))
	}

	// With force — everything should be re-added
	toAdd, toRemove = e.calculateIndexChanges(docs, indexedFiles, true)
	if len(toAdd) != 2 {
		t.Fatalf("with force: expected 2 to add, got %d", len(toAdd))
	}
}

func TestCalculateIndexChanges_UnchangedFiles(t *testing.T) {
	e := &Engine{}
	now := time.Now()

	docs := []document{
		{ID: "1", Path: "a.md", Content: "aaa", FileHash: "h1", LastModified: now},
	}
	indexedFiles := map[string]*vectorstore.IndexedFileInfo{
		"a.md": {FilePath: "a.md", Hash: "h1", ModTime: now},
	}

	toAdd, toRemove := e.calculateIndexChanges(docs, indexedFiles, false)

	if len(toAdd) != 0 {
		t.Fatalf("expected 0 to add for unchanged, got %d", len(toAdd))
	}
	if len(toRemove) != 0 {
		t.Fatalf("expected 0 to remove for unchanged, got %d", len(toRemove))
	}
}

func TestCalculateIndexChanges_MultipleChunksPerFile(t *testing.T) {
	e := &Engine{}
	now := time.Now()

	// Same file split into multiple chunks
	docs := []document{
		{ID: "1a", Path: "big.md", Content: "part1", FileHash: "h_big", LastModified: now},
		{ID: "1b", Path: "big.md", Content: "part2", FileHash: "h_big", LastModified: now},
		{ID: "1c", Path: "big.md", Content: "part3", FileHash: "h_big", LastModified: now},
	}
	indexedFiles := map[string]*vectorstore.IndexedFileInfo{}

	toAdd, toRemove := e.calculateIndexChanges(docs, indexedFiles, false)

	if len(toAdd) != 3 {
		t.Fatalf("expected 3 chunks to add, got %d", len(toAdd))
	}
	if len(toRemove) != 0 {
		t.Fatalf("expected 0 to remove, got %d", len(toRemove))
	}
}
