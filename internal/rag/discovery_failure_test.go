package rag

import (
	"context"
	"errors"
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

			err = engine.IndexDocuments(context.Background())
			if err == nil {
				t.Fatal("indexing with an unavailable source succeeded")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name unavailable path %q", err, path)
			}
			if tc.missing && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("error %v does not preserve os.ErrNotExist", err)
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
