package rag

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

func newRootEngine(t *testing.T, root, indexPath, embeddingURL string) *Engine {
	t.Helper()
	engine := NewEngine(config.Config{
		RootPath: root,
		RAG: config.RAGConfig{
			Enabled:    true,
			MaxResults: 10,
			IndexPath:  indexPath,
			IndexDirs:  []string{"docs"},
			ChunkSize:  2000,
		},
		Embeddings: config.EmbeddingsConfig{
			OllamaBaseURL: embeddingURL,
			Dimension:     2,
			Mode:          "local-only",
		},
	}, nil)
	if engine == nil {
		t.Fatal("NewEngine returned nil")
	}
	t.Cleanup(engine.Stop)
	return engine
}

func newRootProject(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range names {
		writeCorpusFile(t, root, "docs/"+name, "# "+name+"\n\nbody of "+name+".\n")
	}
	return root
}

func indexedPaths(t *testing.T, engine *Engine) []string {
	t.Helper()
	files, err := engine.vecService.store.GetAllIndexedFiles()
	if err != nil {
		t.Fatalf("GetAllIndexedFiles: %v", err)
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func assertIndexed(t *testing.T, engine *Engine, want ...string) {
	t.Helper()
	got := indexedPaths(t, engine)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("indexed files = %v, want %v", got, want)
	}
}

func recordedRoot(t *testing.T, engine *Engine) string {
	t.Helper()
	root, _ := engine.vecService.store.GetMetadata(indexRootMetadataKey)
	return root
}

// T161. The original symptom: another project's run left no error and an index
// holding only its own files. The witness is the index surviving, with its state
// untouched, not just the returned error.
func TestIndexRefusesToWipeIndexFromAnotherRoot(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	indexPath := t.TempDir()
	rootA := newRootProject(t, "a1.md", "a2.md", "a3.md")
	rootB := newRootProject(t, "b1.md")

	engineA := newRootEngine(t, rootA, indexPath, srv.URL)
	if err := engineA.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("index A: %v", err)
	}

	engineB := newRootEngine(t, rootB, indexPath, srv.URL)
	err := engineB.IndexDocuments(context.Background())
	if err == nil {
		t.Fatal("index B: want refusal, got nil")
	}
	for _, want := range []string{rootA, rootB, "3 of its 3", "--allow-root-change", "MCP_DATA_PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	assertIndexed(t, engineB, "docs/a1.md", "docs/a2.md", "docs/a3.md")
	if state, _ := engineB.vecService.store.GetMetadata(indexStateMetadataKey); state != indexStateReady {
		t.Errorf("index_state = %q, want %q: a refused run must not force a rebuild later", state, indexStateReady)
	}
	if got := recordedRoot(t, engineB); got != rootA {
		t.Errorf("index_root = %q, want %q", got, rootA)
	}
}

func TestIndexAllowRootChangeReplacesIndex(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	indexPath := t.TempDir()
	rootA := newRootProject(t, "a1.md", "a2.md")
	rootB := newRootProject(t, "b1.md")

	if err := newRootEngine(t, rootA, indexPath, srv.URL).IndexDocuments(context.Background()); err != nil {
		t.Fatalf("index A: %v", err)
	}

	engineB := newRootEngine(t, rootB, indexPath, srv.URL)
	engineB.AllowRootChange()
	if err := engineB.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("index B: %v", err)
	}
	assertIndexed(t, engineB, "docs/b1.md")
	if got := recordedRoot(t, engineB); got != rootB {
		t.Errorf("index_root = %q, want %q", got, rootB)
	}
}

// A moved repository changes the root but not the relative paths: nothing is
// lost, so nothing should need a flag.
func TestIndexAcceptsMovedRepository(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	indexPath := t.TempDir()
	oldRoot := newRootProject(t, "a1.md", "a2.md")
	newRoot := newRootProject(t, "a1.md", "a2.md")

	if err := newRootEngine(t, oldRoot, indexPath, srv.URL).IndexDocuments(context.Background()); err != nil {
		t.Fatalf("index old root: %v", err)
	}

	moved := newRootEngine(t, newRoot, indexPath, srv.URL)
	if err := moved.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("index new root: %v", err)
	}
	assertIndexed(t, moved, "docs/a1.md", "docs/a2.md")
	if got := recordedRoot(t, moved); got != newRoot {
		t.Errorf("index_root = %q, want %q", got, newRoot)
	}
}

// Deleting documents under the same root is ordinary maintenance, however many
// of them go.
func TestIndexSameRootMayRemoveMostFiles(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	root := newRootProject(t, "a1.md", "a2.md", "a3.md")

	engine := newRootEngine(t, root, t.TempDir(), srv.URL)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("first index: %v", err)
	}
	for _, name := range []string{"a2.md", "a3.md"} {
		if err := os.Remove(filepath.Join(root, "docs", name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("second index: %v", err)
	}
	assertIndexed(t, engine, "docs/a1.md")
}

// An index from a build without the stamp has no recorded root. The first run
// after the upgrade must still protect it, and adopt it when the files match.
func TestIndexWithoutRecordedRoot(t *testing.T) {
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)
	indexPath := t.TempDir()
	rootA := newRootProject(t, "a1.md", "a2.md")
	rootB := newRootProject(t, "b1.md")

	engineA := newRootEngine(t, rootA, indexPath, srv.URL)
	if err := engineA.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("index A: %v", err)
	}
	if err := engineA.vecService.store.SetMetadata(indexRootMetadataKey, ""); err != nil {
		t.Fatalf("clear index_root: %v", err)
	}

	engineB := newRootEngine(t, rootB, indexPath, srv.URL)
	err := engineB.IndexDocuments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "an unrecorded root") {
		t.Fatalf("index B = %v, want refusal naming an unrecorded root", err)
	}
	assertIndexed(t, engineB, "docs/a1.md", "docs/a2.md")

	adopted := newRootEngine(t, rootA, indexPath, srv.URL)
	if err := adopted.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("re-index A: %v", err)
	}
	if got := recordedRoot(t, adopted); got != rootA {
		t.Errorf("index_root = %q, want %q", got, rootA)
	}
}
