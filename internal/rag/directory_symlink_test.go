package rag

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexDirectorySymlinkKeepsLogicalPaths(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "relative"
		if absolute {
			name = "absolute"
		}
		t.Run(name, func(t *testing.T) {
			srv := newBatchEmbeddingServer(t)
			t.Cleanup(srv.Close)
			root := newRootProject(t, "guide.md")
			engine := newRootEngine(t, root, t.TempDir(), srv.URL)
			ctx := context.Background()
			if err := engine.IndexDocuments(ctx); err != nil {
				t.Fatal(err)
			}
			assertIndexed(t, engine, "docs/guide.md")

			target := filepath.Join(root, "source")
			if err := os.Rename(filepath.Join(root, "docs"), target); err != nil {
				t.Fatal(err)
			}
			linkTarget := "source"
			if absolute {
				linkTarget = target
				engine.docService.config.IndexDirs = []string{filepath.Join(root, "docs")}
			}
			if err := os.Symlink(linkTarget, filepath.Join(root, "docs")); err != nil {
				t.Fatal(err)
			}
			if err := engine.IndexDocuments(ctx); err != nil {
				t.Fatal(err)
			}
			assertIndexed(t, engine, "docs/guide.md")
			response, err := engine.Search(ctx, "guide", 5, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Path != "docs/guide.md" {
				t.Fatalf("search after directory replacement = %+v", response.Results)
			}

			writeCorpusFile(t, root, "source/new.md", "# New guide\n\nNew content.\n")
			if err := engine.IndexDocuments(ctx); err != nil {
				t.Fatal(err)
			}
			assertIndexed(t, engine, "docs/guide.md", "docs/new.md")
		})
	}
}

func TestCollectDirectorySymlinkHonorsExcludesAndSkipsNestedLinks(t *testing.T) {
	root := newRootProject(t, "guide.md", "private/hidden.md", "draft.md")
	if err := os.Symlink(".", filepath.Join(root, "docs", "cycle")); err != nil {
		t.Fatal(err)
	}
	outside := newRootProject(t, "outside.md")
	if err := os.Symlink(filepath.Join(outside, "docs"), filepath.Join(root, "docs", "external")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docs", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	ds := newDocumentService(docServiceConfig{
		RepoRoot: root, IndexDirs: []string{"linked"}, ChunkSize: 2000,
		IndexExcludeDirs:  []string{"linked/private"},
		IndexExcludeGlobs: []string{"linked/draft.md"},
	}, nil)
	docs, err := ds.collectDocuments()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Path != "linked/guide.md" || docs[0].ID != "linked/guide.md-0" {
		t.Fatalf("collected documents = %+v, want only linked/guide.md", docs)
	}
}
