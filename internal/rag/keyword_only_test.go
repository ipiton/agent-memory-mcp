package rag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

func testEmbedderAt(t *testing.T, url string) embedder.Service {
	t.Helper()
	emb, err := embedder.New(embedder.Config{
		OllamaBaseURL: url,
		Dimension:     2,
		Mode:          "local-only",
		MaxRetries:    1,
		Timeout:       time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("embedder.New: %v", err)
	}
	return embedder.AsService(emb)
}

// deadEmbeddingURL is an address that refuses connections, as a machine with no
// model server does.
func deadEmbeddingURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	return server.URL
}

func newKeywordOnlyEngine(t *testing.T, emb embedder.Service, fallback bool) *Engine {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "runbook.md"), []byte("# Runbook\nrollback ingress safely"), 0o644); err != nil {
		t.Fatalf("write runbook: %v", err)
	}
	store, err := vectorstore.NewSQLiteStore(filepath.Join(t.TempDir(), "vectors.db"), 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return &Engine{
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
			config: vecServiceConfig{Embedder: emb, MaxResults: 10, KeywordFallback: fallback},
			logger: zap.NewNop(),
			store:  store,
		},
		stopWatcher: make(chan struct{}),
	}
}

func searchRunbook(t *testing.T, e *Engine) *SearchResponse {
	t.Helper()
	response, err := e.vecService.search(context.Background(), searchQuery{Query: "rollback ingress", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return response
}

func TestKeywordOnlyIndexThenModelAppears(t *testing.T) {
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, deadEmbeddingURL(t)), true)
	store := e.vecService.store

	if err := e.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments without a model: %v", err)
	}
	if got, _ := store.GetMetadata(keywordOnlyMetadataKey); got != "true" {
		t.Fatalf("keyword_only = %q, want true", got)
	}
	if got, _ := store.GetMetadata(indexStateMetadataKey); got != indexStateReady {
		t.Fatalf("index state = %q, want ready", got)
	}
	if got, _ := store.GetMetadata("embedding_model"); got != "" {
		t.Fatalf("embedding_model = %q, want none", got)
	}

	response := searchRunbook(t, e)
	if len(response.Results) == 0 {
		t.Fatal("no results from keywords")
	}
	if !response.Retrieval.KeywordOnly || !response.Retrieval.Degraded {
		t.Fatalf("retrieval = %+v, want keyword_only and degraded", response.Retrieval)
	}

	server := newTestEmbeddingServer(t)
	defer server.Close()
	e.vecService.config.Embedder = testEmbedderAt(t, server.URL)

	if err := e.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments with a model: %v", err)
	}
	if got, _ := store.GetMetadata(keywordOnlyMetadataKey); got != "" {
		t.Fatalf("keyword_only = %q after the rebuild", got)
	}
	chunks, err := store.AllChunks()
	if err != nil {
		t.Fatalf("AllChunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks after the rebuild")
	}
	for _, chunk := range chunks {
		if len(chunk.Embedding) == 0 {
			t.Fatalf("chunk %s has no vector after the rebuild", chunk.ID)
		}
	}
	if response := searchRunbook(t, e); response.Retrieval.KeywordOnly || response.Retrieval.Degraded {
		t.Fatalf("retrieval after the rebuild = %+v", response.Retrieval)
	}
}

func TestWithoutFallbackNoModelStaysAnError(t *testing.T) {
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, deadEmbeddingURL(t)), false)

	if err := e.IndexDocuments(context.Background()); err == nil {
		t.Fatal("IndexDocuments without a model and without the flag succeeded")
	}
	if got, _ := e.vecService.store.GetMetadata(keywordOnlyMetadataKey); got != "" {
		t.Fatalf("keyword_only = %q", got)
	}
	if _, err := e.vecService.search(context.Background(), searchQuery{Query: "rollback", Limit: 5}); err == nil {
		t.Fatal("search without a model and without the flag succeeded")
	}
}

func TestOutageDoesNotReplaceVectors(t *testing.T) {
	server := newTestEmbeddingServer(t)
	defer server.Close()
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), true)
	if err := e.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}

	e.vecService.config.Embedder = testEmbedderAt(t, deadEmbeddingURL(t))
	if err := e.IndexDocuments(context.Background()); err == nil {
		t.Fatal("indexing during an outage succeeded")
	}
	chunks, err := e.vecService.store.AllChunks()
	if err != nil {
		t.Fatalf("AllChunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("chunks lost")
	}
	for _, chunk := range chunks {
		if len(chunk.Embedding) == 0 {
			t.Fatalf("chunk %s lost its vector during the outage", chunk.ID)
		}
	}

	response := searchRunbook(t, e)
	if len(response.Results) == 0 || !response.Retrieval.KeywordOnly {
		t.Fatalf("search during the outage: %d results, retrieval %+v", len(response.Results), response.Retrieval)
	}
}

func TestStrictModeWinsOverKeywordFallback(t *testing.T) {
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, deadEmbeddingURL(t)), true)
	e.vecService.config.Strict = true

	if _, err := e.vecService.search(context.Background(), searchQuery{Query: "rollback", Limit: 5}); err == nil {
		t.Fatal("strict search answered from keywords")
	}
}
