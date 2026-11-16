package rag

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

func TestIndexDiscoveryFailurePreservesPublishedIndex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		missing bool
	}{
		{name: "missing directory", path: "docs", missing: true},
		{name: "missing configured file", path: "README.md", missing: true},
		{name: "unreadable directory", path: "notes"},
		{name: "unreadable discovered file", path: "docs/a.md"},
		{name: "unreadable configured file", path: "README.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newBatchEmbeddingServer(t)
			t.Cleanup(srv.Close)
			root := newRootProject(t, "a.md", "b.md")
			writeCorpusFile(t, root, "notes/c.md", "# Notes\n\nretained knowledge\n")
			writeCorpusFile(t, root, "README.md", "# Project\n\nproject overview\n")
			engine := newRootEngine(t, root, t.TempDir(), srv.URL)
			engine.docService.config.IndexDirs = []string{"docs", "notes", "README.md"}
			if err := engine.IndexDocuments(context.Background()); err != nil {
				t.Fatalf("initial index: %v", err)
			}
			store := engine.vecService.store
			filesBefore, err := store.GetAllIndexedFiles()
			if err != nil {
				t.Fatal(err)
			}
			chunksBefore := discoveryTestChunks(t, store)
			metadataBefore := discoveryTestMetadata(t, store)

			path := filepath.Join(root, tc.path)
			var restore func()
			if tc.missing {
				moved := filepath.Join(t.TempDir(), "unavailable")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Rename(moved, path); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Chmod(path, info.Mode()); err != nil {
						t.Fatal(err)
					}
				}
				defer restore()
				var readErr error
				if info.IsDir() {
					_, readErr = os.ReadDir(path)
				} else {
					_, readErr = os.ReadFile(path)
				}
				if readErr == nil {
					t.Skip("filesystem permissions do not prevent reading")
				}
			}

			if err := engine.IndexDocuments(context.Background()); err != nil {
				t.Fatalf("index with unavailable source: %v", err)
			}
			if engine.needsIndexing() {
				t.Error("unavailable source alone triggered an index update")
			}

			filesAfter, err := store.GetAllIndexedFiles()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(filesBefore, filesAfter) {
				t.Error("discovery failure changed indexed-file records")
			}
			if !reflect.DeepEqual(chunksBefore, discoveryTestChunks(t, store)) {
				t.Error("discovery failure changed published chunks")
			}
			if !reflect.DeepEqual(metadataBefore, discoveryTestMetadata(t, store)) {
				t.Error("discovery failure changed index metadata")
			}
			restore()
			if err := engine.IndexDocuments(context.Background()); err != nil {
				t.Fatalf("index after restoring source: %v", err)
			}
			assertIndexed(t, engine, "README.md", "docs/a.md", "docs/b.md", "notes/c.md")
		})
	}
}

func discoveryTestChunks(t *testing.T, store vectorstore.Store) []vectorstore.Chunk {
	t.Helper()
	chunks, err := store.AllChunks()
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].ID < chunks[j].ID })
	return chunks
}

func discoveryTestMetadata(t *testing.T, store vectorstore.Store) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for _, key := range []string{indexStateMetadataKey, indexStartedAtKey, indexRootMetadataKey, "index_generation", "last_indexed", "embedding_model", "chunker_version"} {
		value, err := store.GetMetadata(key)
		if err != nil {
			t.Fatal(err)
		}
		values[key] = value
	}
	return values
}

func TestIndexMissingOptionalSourceDoesNotBlockHealthyUpdates(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a.md")
	writeCorpusFile(t, root, "README.md", "# Project\n\noverview\n")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	engine.docService.config.IndexDirs = []string{"docs", "README.md", "CHANGELOG.md"}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "README.md", "docs/a.md")
	writeCorpusFile(t, root, "docs/b.md", "# New\n\nnew material\n")
	if !engine.needsIndexing() {
		t.Fatal("healthy addition not detected")
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "README.md", "docs/a.md", "docs/b.md")
}

func TestIndexUnavailableSubdirectoryDoesNotBlockSiblings(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a.md", "z-deleted.md")
	writeCorpusFile(t, root, "docs/hidden/kept.md", "# Kept\n\nprotected material\n")
	writeCorpusFile(t, root, "docs/hidden-other/deleted.md", "# Removed\n\nold material\n")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := engine.vecService.store
	before, err := store.ChunksByDocPath("docs/hidden/kept.md")
	if err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(root, "docs/hidden")
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(hidden, 0o755); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.ReadDir(hidden); err == nil {
		t.Skip("filesystem permits reading")
	}
	for _, path := range []string{"docs/z-deleted.md", "docs/hidden-other/deleted.md"} {
		if err := os.Remove(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	writeCorpusFile(t, root, "docs/a.md", "# Updated\n\nchanged material\n")
	writeCorpusFile(t, root, "docs/z-added.md", "# Added\n\nnew material\n")
	if !engine.needsIndexing() {
		t.Fatal("healthy updates not detected")
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "docs/a.md", "docs/hidden/kept.md", "docs/z-added.md")
	after, err := store.ChunksByDocPath("docs/hidden/kept.md")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unavailable chunks changed")
	}
	chunks, err := store.ChunksByDocPath("docs/a.md")
	if err != nil || len(chunks) == 0 || !strings.Contains(chunks[0].Content, "changed material") {
		t.Fatalf("healthy file not updated: %v, %v", chunks, err)
	}
	if engine.needsIndexing() {
		t.Fatal("unavailable subtree alone triggers repeated updates")
	}
	if err := os.Chmod(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCorpusFile(t, root, "docs/hidden/kept.md", "# Recovered\n\nrecovered material\n")
	if !engine.needsIndexing() {
		t.Fatal("restored changed source not detected")
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIndexRebuildWaitsForUnavailableIndexedSource(t *testing.T) {
	for _, metadata := range []string{"chunker_version", "embedding_model"} {
		t.Run(metadata, func(t *testing.T) {
			srv := newBatchEmbeddingServer(t)
			t.Cleanup(srv.Close)
			root := newRootProject(t, "a.md")
			writeCorpusFile(t, root, "README.md", "# Project\n\noverview\n")
			engine := newRootEngine(t, root, t.TempDir(), srv.URL)
			engine.docService.config.IndexDirs = []string{"docs", "README.md"}
			if err := engine.IndexDocuments(context.Background()); err != nil {
				t.Fatal(err)
			}
			store := engine.vecService.store
			if err := store.SetMetadata(metadata, "previous-version"); err != nil {
				t.Fatal(err)
			}
			before := discoveryTestChunks(t, store)
			beforeMetadata := discoveryTestMetadata(t, store)
			moved := filepath.Join(t.TempDir(), "unavailable")
			if err := os.Rename(filepath.Join(root, "docs"), moved); err != nil {
				t.Fatal(err)
			}
			if err := engine.IndexDocuments(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot rebuild") {
				t.Fatalf("rebuild error = %v", err)
			}
			if !reflect.DeepEqual(before, discoveryTestChunks(t, store)) || !reflect.DeepEqual(beforeMetadata, discoveryTestMetadata(t, store)) {
				t.Fatal("blocked rebuild changed published index")
			}
			if err := os.Rename(moved, filepath.Join(root, "docs")); err != nil {
				t.Fatal(err)
			}
			if err := engine.IndexDocuments(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDocumentScanUnavailablePathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		prefix, path string
		want         bool
	}{
		{"docs", "docs/a.md", true}, {"docs", "docs", true},
		{"docs", "docs-other/a.md", false}, {"docs/a.md", "docs/a.md.backup", false},
		{".", "README.md", true}, {".", "../shared/a.md", false}, {".", "..", false}, {"..", "docs/a.md", true}, {"../..", "docs/a.md", true}, {"../notes", "../notes/a.md", true},
	} {
		if got := (documentScan{repoRoot: t.TempDir(), unavailablePaths: []string{tc.prefix}}).isUnavailable(tc.path); got != tc.want {
			t.Errorf("%q / %q = %v, want %v", tc.prefix, tc.path, got, tc.want)
		}
	}
}

func TestIndexFailedFileReadPreservesOnlyThatFile(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a.md", "b.md")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := engine.vecService.store
	before, err := store.ChunksByDocPath("docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	unavailable := filepath.Join(root, "docs/a.md")
	moved := filepath.Join(t.TempDir(), "unavailable")
	if err := os.Rename(unavailable, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing.md"), unavailable); err != nil {
		t.Fatal(err)
	}
	writeCorpusFile(t, root, "docs/b.md", "# Updated\n\nhealthy changed material\n")
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "docs/a.md", "docs/b.md")
	after, err := store.ChunksByDocPath("docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed read replaced the old chunks")
	}
	if engine.needsIndexing() {
		t.Fatal("failed read alone triggered indexing")
	}
	// Once the directory scan can establish absence, normal pruning resumes.
	if err := os.Remove(unavailable); err != nil {
		t.Fatal(err)
	}
	if !engine.needsIndexing() {
		t.Fatal("confirmed deletion not detected")
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "docs/b.md")
}

func TestIndexRetiredConfiguredSourceIsPruned(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a.md")
	writeCorpusFile(t, root, "README.md", "# Project\n\noverview\n")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	engine.docService.config.IndexDirs = []string{"docs", "README.md"}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.docService.config.IndexDirs = []string{"docs", "CHANGELOG.md"}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "docs/a.md")
}

func TestIndexPartialScanDoesNotMarkDirtyIndexReady(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a.md")
	writeCorpusFile(t, root, "README.md", "# Project\n\noverview\n")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	engine.docService.config.IndexDirs = []string{"docs", "README.md"}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := engine.vecService.store
	if err := store.SetMetadata(indexStateMetadataKey, indexStateDirty); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "unavailable")
	if err := os.Rename(filepath.Join(root, "docs"), moved); err != nil {
		t.Fatal(err)
	}
	writeCorpusFile(t, root, "README.md", "# Updated\n\nhealthy material\n")
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetMetadata(indexStateMetadataKey)
	if err != nil || state != indexStateDirty {
		t.Fatalf("state = %q, error = %v", state, err)
	}
	if err := os.Rename(moved, filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = store.GetMetadata(indexStateMetadataKey)
	if err != nil || state != indexStateReady {
		t.Fatalf("recovered state = %q, error = %v", state, err)
	}
}

func TestIndexDirtyRecoveryWithMissingOptionalSource(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a.md")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	engine.docService.config.IndexDirs = []string{"docs", "CHANGELOG.md"}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := engine.vecService.store
	if err := store.SetMetadata(indexStateMetadataKey, indexStateDirty); err != nil {
		t.Fatal(err)
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetMetadata(indexStateMetadataKey)
	if err != nil || state != indexStateReady {
		t.Fatalf("recovered state = %q, error = %v", state, err)
	}
	if engine.needsIndexing() {
		t.Fatal("recovered index still requires indexing")
	}
}

func TestIndexUnavailableRootDoesNotBlockExternalSource(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a.md")
	external := t.TempDir()
	writeCorpusFile(t, external, "a.md", "# External\n\nold material\n")
	writeCorpusFile(t, external, "b.md", "# Removed\n\nold material\n")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	engine.docService.config.IndexDirs = []string{".", external}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := engine.vecService.store.ChunksByDocPath("docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0o755); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.ReadDir(root); err == nil {
		t.Skip("filesystem permits reading")
	}
	writeCorpusFile(t, external, "a.md", "# Updated\n\nnew external material\n")
	writeCorpusFile(t, external, "c.md", "# Added\n\nnew material\n")
	if err := os.Remove(filepath.Join(external, "b.md")); err != nil {
		t.Fatal(err)
	}
	if !engine.needsIndexing() {
		t.Fatal("healthy external changes not detected")
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := engine.docService.relPath(filepath.Join(external, "a.md"))
	c := engine.docService.relPath(filepath.Join(external, "c.md"))
	assertIndexed(t, engine, a, c, "docs/a.md")
	after, err := engine.vecService.store.ChunksByDocPath("docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unavailable root's chunks changed")
	}
	chunks, err := engine.vecService.store.ChunksByDocPath(a)
	if err != nil || len(chunks) == 0 || !strings.Contains(chunks[0].Content, "new external material") {
		t.Fatalf("external file not updated: %v, %v", chunks, err)
	}
	if engine.needsIndexing() {
		t.Fatal("unchanged external source triggers indexing")
	}
}

func TestIndexUnavailableAncestorPreservesRepositoryFiles(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	ancestor := t.TempDir()
	root := filepath.Join(ancestor, "project")
	writeCorpusFile(t, root, "docs/a.md", "# Preserved\n\nretained material\n")
	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	engine.docService.config.IndexDirs = []string{ancestor}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := engine.vecService.store
	before := discoveryTestChunks(t, store)
	beforeMetadata := discoveryTestMetadata(t, store)
	if err := os.Chmod(ancestor, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(ancestor, 0o755); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.ReadDir(ancestor); err == nil {
		t.Skip("filesystem permits reading")
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "docs/a.md")
	if !reflect.DeepEqual(before, discoveryTestChunks(t, store)) || !reflect.DeepEqual(beforeMetadata, discoveryTestMetadata(t, store)) {
		t.Fatal("unavailable ancestor changed the published index")
	}
	if engine.needsIndexing() {
		t.Fatal("unavailable ancestor alone triggers indexing")
	}
}

func TestIndexPartialDeletionPreservesKeywordOnlyUpgrade(t *testing.T) {
	engine := newKeywordOnlyEngine(t, testEmbedderAt(t, deadEmbeddingURL(t)), true)
	root := engine.repoRoot
	writeCorpusFile(t, root, "notes/b.md", "# Notes\n\nold notes\n")
	engine.docService.config.IndexDirs = []string{"docs", "notes"}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := engine.vecService.store
	moved := filepath.Join(t.TempDir(), "unavailable")
	if err := os.Rename(filepath.Join(root, "docs"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "notes/b.md")); err != nil {
		t.Fatal(err)
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertIndexed(t, engine, "docs/runbook.md")
	flag, err := store.GetMetadata(keywordOnlyMetadataKey)
	if err != nil || flag != "true" {
		t.Fatalf("retained keyword_only = %q, error = %v", flag, err)
	}
	if err := os.Rename(moved, filepath.Join(root, "docs")); err != nil {
		t.Fatal(err)
	}
	server := newTestEmbeddingServer(t)
	t.Cleanup(server.Close)
	engine.vecService.config.Embedder = testEmbedderAt(t, server.URL)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	flag, err = store.GetMetadata(keywordOnlyMetadataKey)
	if err != nil || flag != "" {
		t.Fatalf("rebuilt keyword_only = %q, error = %v", flag, err)
	}
	chunks := discoveryTestChunks(t, store)
	if len(chunks) == 0 {
		t.Fatal("no rebuilt chunks")
	}
	for _, chunk := range chunks {
		if len(chunk.Embedding) == 0 {
			t.Fatalf("chunk %s has no vector after model recovery", chunk.ID)
		}
	}
}
