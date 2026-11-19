package rag

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIndexLegacySymlinkRootPreservesUnavailableSources(t *testing.T) {
	for _, tc := range []struct {
		name            string
		unavailable     string
		allowRootChange bool
	}{
		{name: "partial scan", unavailable: "notes"},
		{name: "only source unavailable", unavailable: "docs"},
		{name: "partial scan with override", unavailable: "notes", allowRootChange: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			server := newBatchEmbeddingServer(t)
			t.Cleanup(server.Close)
			root := newRootProject(t, "a.md", "b.md")
			link := filepath.Join(t.TempDir(), "project")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}
			engine := newRootEngine(t, link, t.TempDir(), server.URL)
			if tc.unavailable == "notes" {
				writeCorpusFile(t, root, "notes/c.md", "# Notes\n\nRetained material.\n")
				engine.docService.config.IndexDirs = []string{"docs", "notes"}
			}
			if err := engine.IndexDocuments(ctx); err != nil {
				t.Fatal(err)
			}
			store := engine.vecService.store
			// Older writers stored the configured alias, before root resolution.
			if err := store.SetMetadata(indexRootMetadataKey, link); err != nil {
				t.Fatal(err)
			}
			if err := store.SetMetadata(indexRootResolvedMetadataKey, ""); err != nil {
				t.Fatal(err)
			}
			before := discoveryTestChunks(t, store)
			filesBefore, err := store.GetAllIndexedFiles()
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, tc.unavailable)
			offline := filepath.Join(root, "offline")
			if err := os.Rename(source, offline); err != nil {
				t.Fatal(err)
			}
			if tc.allowRootChange {
				engine.AllowRootChange()
			}
			if err := engine.IndexDocuments(ctx); err != nil {
				t.Fatalf("upgrade index with unavailable source: %v", err)
			}
			filesAfter, err := store.GetAllIndexedFiles()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(filesBefore, filesAfter) || !reflect.DeepEqual(before, discoveryTestChunks(t, store)) {
				t.Fatal("legacy root upgrade changed indexed records or chunks")
			}
			if got, want := recordedRoot(t, engine), resolvedTestRoot(t, root); got != want {
				t.Fatalf("upgraded index root = %q, want %q", got, want)
			}
			if marker, err := store.GetMetadata(indexRootResolvedMetadataKey); err != nil || marker != recordedRoot(t, engine) {
				t.Fatalf("resolved root marker = %q, error = %v", marker, err)
			}
			if tc.unavailable == "notes" {
				response, err := engine.DocumentContext(ctx, "docs/a.md", ContextOptions{})
				if err != nil || len(response.Sections) == 0 {
					t.Fatalf("context for available document after upgrade = %+v, error = %v", response, err)
				}
			}
			if err := os.Rename(offline, source); err != nil {
				t.Fatal(err)
			}
			if err := engine.IndexDocuments(ctx); err != nil {
				t.Fatalf("index after source recovery: %v", err)
			}
			if !reflect.DeepEqual(before, discoveryTestChunks(t, store)) {
				t.Fatal("source recovery changed unchanged chunks")
			}
		})
	}
}

func TestIndexResolvedRootReplacedBySymlinkKeepsForeignRootGuard(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "complete scan"
		if partial {
			name = "partial scan"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			server := newBatchEmbeddingServer(t)
			t.Cleanup(server.Close)
			root := resolvedTestRoot(t, newRootProject(t, "a.md", "b.md", "c.md"))
			index := t.TempDir()
			old := newRootEngine(t, root, index, server.URL)
			if err := old.IndexDocuments(ctx); err != nil {
				t.Fatal(err)
			}
			store := old.vecService.store
			before := discoveryTestChunks(t, store)
			beforeMetadata := discoveryTestMetadata(t, store)
			nextRoot := newRootProject(t, "other.md")
			dirs := []string{"docs"}
			if partial {
				nextRoot = newRootProject(t, "a.md", "b.md")
				dirs = []string{"docs/a.md", "docs/b.md", "docs/c.md"}
			}
			if err := os.Rename(root, filepath.Join(t.TempDir(), "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(nextRoot, root); err != nil {
				t.Fatal(err)
			}
			next := newRootEngine(t, nextRoot, index, server.URL)
			next.docService.config.IndexDirs = dirs
			if err := next.IndexDocuments(ctx); err == nil {
				t.Fatal("canonical root retarget was accepted as the same project")
			}
			if !reflect.DeepEqual(before, discoveryTestChunks(t, store)) ||
				!reflect.DeepEqual(beforeMetadata, discoveryTestMetadata(t, store)) {
				t.Fatal("refused root change modified the published index")
			}
		})
	}
}

func TestIndexLegacySymlinkRootCannotAdoptAnotherTree(t *testing.T) {
	for _, missingAlias := range []bool{false, true} {
		name := "different resolved root"
		if missingAlias {
			name = "unavailable old alias"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			server := newBatchEmbeddingServer(t)
			t.Cleanup(server.Close)
			root := newRootProject(t, "a.md", "b.md")
			writeCorpusFile(t, root, "notes/c.md", "# Notes\n\nOld project material.\n")
			link := filepath.Join(t.TempDir(), "project")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}
			index := t.TempDir()
			oldEngine := newRootEngine(t, link, index, server.URL)
			oldEngine.docService.config.IndexDirs = []string{"docs", "notes"}
			if err := oldEngine.IndexDocuments(ctx); err != nil {
				t.Fatal(err)
			}
			store := oldEngine.vecService.store
			if err := store.SetMetadata(indexRootMetadataKey, link); err != nil {
				t.Fatal(err)
			}
			if err := store.SetMetadata(indexRootResolvedMetadataKey, ""); err != nil {
				t.Fatal(err)
			}
			before := discoveryTestChunks(t, store)
			beforeMetadata := discoveryTestMetadata(t, store)
			if missingAlias {
				if err := os.Rename(link, link+"-offline"); err != nil {
					t.Fatal(err)
				}
			}
			next := newRootEngine(t, newRootProject(t, "a.md", "b.md"), index, server.URL)
			next.docService.config.IndexDirs = []string{"docs", "notes"}
			if err := next.IndexDocuments(ctx); err == nil || !strings.Contains(err.Error(), "cannot adopt") {
				t.Fatalf("automatic adoption of another root = %v", err)
			}
			if !reflect.DeepEqual(before, discoveryTestChunks(t, store)) ||
				!reflect.DeepEqual(beforeMetadata, discoveryTestMetadata(t, store)) {
				t.Fatal("failed adoption changed the published index")
			}
		})
	}
}
