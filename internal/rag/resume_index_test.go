package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

// countingEmbedServer is an Ollama stand-in that counts the texts it embeds.
type countingEmbedServer struct {
	*httptest.Server
	texts atomic.Int64
}

func newCountingEmbedServer(t *testing.T) *countingEmbedServer {
	t.Helper()
	s := &countingEmbedServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/embed":
			var body struct {
				Input any `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			n := 1
			if inputs, ok := body.Input.([]any); ok {
				n = len(inputs)
			}
			s.texts.Add(int64(n))
			embeddings := make([][]float64, n)
			for i := range embeddings {
				embeddings[i] = []float64{0.9, 0.1}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
		case "/api/embeddings":
			s.texts.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{0.9, 0.1}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// newResumeTestEngine indexes files one-chunk markdown files under docs/.
func newResumeTestEngine(t *testing.T, files int) (*Engine, *vectorstore.SQLiteStore, *countingEmbedServer) {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	for i := range files {
		body := fmt.Sprintf("# Note %d\nfact number %d about the move", i, i)
		if err := os.WriteFile(filepath.Join(repoRoot, "docs", fmt.Sprintf("note-%03d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatalf("write note: %v", err)
		}
	}

	server := newCountingEmbedServer(t)
	emb, err := embedder.New(embedder.Config{
		OllamaBaseURL: server.URL,
		Dimension:     2,
		Mode:          "local-only",
		MaxRetries:    0,
		Timeout:       time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("embedder.New: %v", err)
	}

	store, err := vectorstore.NewSQLiteStore(filepath.Join(t.TempDir(), "vectors.db"), 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	engine := &Engine{
		config:   config.Config{RootPath: repoRoot, RAG: config.RAGConfig{MaxResults: 10}},
		repoRoot: repoRoot,
		logger:   zap.NewNop(),
		docService: newDocumentService(docServiceConfig{
			RepoRoot:     repoRoot,
			IndexDirs:    []string{"docs"},
			ChunkSize:    2000,
			ChunkOverlap: 200,
		}, zap.NewNop()),
		vecService: &vectorService{
			config: vecServiceConfig{Embedder: emb, MaxResults: 10},
			logger: zap.NewNop(),
			store:  store,
		},
		stopWatcher: make(chan struct{}),
	}
	return engine, store, server
}

// embeddedTexts counts passage texts, leaving out the one-text model probe each
// run makes once a model is recorded.
func embeddedTexts(before, after int64) int64 {
	return after - before - 1
}

// TestDirtyIndexIsReconciledNotRebuilt reproduces the production loop: a pod
// restarted in the middle of a run left the index dirty, the next start threw
// away all 33k chunks to rebuild them, took longer than the pod lived, and
// started over. A dirty index must cost only the files whose chunks are
// actually missing.
func TestDirtyIndexIsReconciledNotRebuilt(t *testing.T) {
	engine, store, server := newResumeTestEngine(t, 12)
	ctx := context.Background()

	if err := engine.IndexDocuments(ctx); err != nil {
		t.Fatalf("first IndexDocuments: %v", err)
	}
	full := store.Count()
	if full != 12 {
		t.Fatalf("chunks after first run = %d, want 12", full)
	}

	// The state an interrupted run leaves: marked dirty, and one file whose
	// chunks were deleted while its record was still there.
	if err := store.SetMetadata(indexStateMetadataKey, indexStateDirty); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	if err := store.DeleteByDocPath("docs/note-003.md"); err != nil {
		t.Fatalf("DeleteByDocPath: %v", err)
	}
	if !engine.needsIndexing() {
		t.Fatal("needsIndexing = false for a dirty index")
	}

	before := server.texts.Load()
	if err := engine.IndexDocuments(ctx); err != nil {
		t.Fatalf("second IndexDocuments: %v", err)
	}
	if got := embeddedTexts(before, server.texts.Load()); got != 1 {
		t.Fatalf("second run embedded %d texts, want 1 (only the file whose chunks were missing)", got)
	}
	if store.Count() != full {
		t.Fatalf("chunks after recovery = %d, want %d", store.Count(), full)
	}
	if state, _ := store.GetMetadata(indexStateMetadataKey); state != indexStateReady {
		t.Fatalf("index_state = %q, want %q", state, indexStateReady)
	}
}

// TestStoppedRebuildResumes covers the other half of the loop: progress made
// before a run stops must survive into the next one. A rebuild (here forced by
// a chunker change) that stops after its first batch must not start from zero.
func TestStoppedRebuildResumes(t *testing.T) {
	const files = 60 // two batches of 50
	engine, store, server := newResumeTestEngine(t, files)
	ctx := context.Background()

	if err := engine.IndexDocuments(ctx); err != nil {
		t.Fatalf("first IndexDocuments: %v", err)
	}
	if err := store.SetMetadata("chunker_version", "char-v1"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	// The embedder dies after the first batch of the rebuild: the run ends with
	// 50 files done and 10 not.
	var batches atomic.Int32
	working := engine.vecService.config.Embedder
	engine.vecService.config.Embedder = stopAfterBatches{Service: working, batches: &batches, allow: 1}
	if err := engine.IndexDocuments(ctx); err == nil {
		t.Fatal("rebuild with a failing embedder reported success")
	}
	if got := store.Count(); got != 50 {
		t.Fatalf("chunks after the stopped rebuild = %d, want 50", got)
	}

	engine.vecService.config.Embedder = working
	before := server.texts.Load()
	if err := engine.IndexDocuments(ctx); err != nil {
		t.Fatalf("resumed IndexDocuments: %v", err)
	}
	if got := embeddedTexts(before, server.texts.Load()); got != files-50 {
		t.Fatalf("resumed run embedded %d texts, want %d (only what the stopped run had not done)", got, files-50)
	}
	if store.Count() != files {
		t.Fatalf("chunks after resume = %d, want %d", store.Count(), files)
	}
	if v, _ := store.GetMetadata("chunker_version"); v == "char-v1" {
		t.Fatal("chunker_version still names the old chunker after the rebuild")
	}
}

// stopAfterBatches lets allow batch calls through and fails every later one,
// as an embedder that went away in the middle of a run.
type stopAfterBatches struct {
	embedder.Service
	batches *atomic.Int32
	allow   int32
}

func (s stopAfterBatches) BatchEmbedDetailed(ctx context.Context, texts []string) (*embedder.BatchEmbeddingResult, error) {
	if s.batches.Add(1) > s.allow {
		return nil, errors.New("embedder went away")
	}
	return s.Service.BatchEmbedDetailed(ctx, texts)
}
