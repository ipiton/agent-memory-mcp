package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

func writeCorpusFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// newBatchEmbeddingServer answers a batch of N texts with N vectors. The
// shared newTestEmbeddingServer always returns exactly one, which is fine for
// single-chunk fixtures and fails validation the moment a corpus has more.
func newBatchEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input any `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := 1
		if list, ok := req.Input.([]any); ok && len(list) > 0 {
			n = len(list)
		}
		switch r.URL.Path {
		case "/api/embed":
			vectors := make([][]float64, n)
			for i := range vectors {
				vectors[i] = []float64{0.9, 0.1}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		case "/api/embeddings":
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{0.9, 0.1}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newCoverageEngine(t *testing.T, root string, indexDirs []string) *Engine {
	t.Helper()
	srv := newBatchEmbeddingServer(t)
	t.Cleanup(srv.Close)

	engine := NewEngine(config.Config{
		RootPath: root,
		RAG: config.RAGConfig{
			Enabled:     true,
			MaxResults:  10,
			IndexPath:   t.TempDir(),
			IndexDirs:   indexDirs,
			ChunkSize:   2000,
			AutoIndex:   false,
			FileWatcher: false,
		},
		Embeddings: config.EmbeddingsConfig{
			OllamaBaseURL: srv.URL,
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

// T97. A configured root that contributes nothing is the state that hid the
// original gap: `semantic_search` returns "no results", which reads as "nothing
// like that exists" rather than "that corner was never indexed". The coverage
// report has to name such a root, not merely omit it — an omitted root is
// indistinguishable from one nobody configured.
func TestCoverageNamesAConfiguredRootThatIndexedNothing(t *testing.T) {
	root := t.TempDir()
	writeCorpusFile(t, root, "docs/guide.md", "# Guide\n\nalpha beta gamma content here.\n")
	if err := os.MkdirAll(filepath.Join(root, "tasks", "archive"), 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}

	engine := newCoverageEngine(t, root, []string{"docs", "tasks/archive"})
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}

	report, err := engine.Coverage()
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}

	if len(report.Empty) != 1 || report.Empty[0] != "tasks/archive" {
		t.Fatalf("Empty = %v, want [tasks/archive]", report.Empty)
	}
	if len(report.Roots) != 1 || report.Roots[0].Root != "docs" {
		t.Fatalf("Roots = %+v, want a single docs entry", report.Roots)
	}
	if report.Roots[0].Files != 1 || report.Roots[0].Chunks == 0 {
		t.Errorf("docs coverage = %+v, want 1 file and a non-zero chunk count", report.Roots[0])
	}

	text := FormatCoverage(report)
	if !strings.Contains(text, "tasks/archive") || !strings.Contains(text, "contributed nothing") {
		t.Errorf("rendered report does not call out the empty root:\n%s", text)
	}
}

// Files are attributed to the deepest matching root, and a root name is not a
// prefix match on the raw string — "docs" must not claim "docs-archive".
func TestCoverageAttributesByLongestRoot(t *testing.T) {
	root := t.TempDir()
	writeCorpusFile(t, root, "docs/guide.md", "# Guide\n\nalpha beta gamma.\n")
	writeCorpusFile(t, root, "docs/adr/0001.md", "# ADR 1\n\ndecision record body.\n")
	writeCorpusFile(t, root, "docs-archive/old.md", "# Old\n\narchived body text.\n")

	engine := newCoverageEngine(t, root, []string{"docs", "docs/adr", "docs-archive"})
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}

	report, err := engine.Coverage()
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}

	files := map[string]int{}
	for _, rc := range report.Roots {
		files[rc.Root] = rc.Files
	}
	if files["docs/adr"] != 1 {
		t.Errorf("docs/adr = %d files, want 1 — the deepest matching root owns the file", files["docs/adr"])
	}
	if files["docs"] != 1 {
		t.Errorf("docs = %d files, want 1 (guide.md only; the ADR belongs to docs/adr)", files["docs"])
	}
	if files["docs-archive"] != 1 {
		t.Errorf("docs-archive = %d files, want 1 — a shared name prefix is not containment", files["docs-archive"])
	}
	if report.Other.Files != 0 {
		t.Errorf("Other = %d files, want 0", report.Other.Files)
	}
}
