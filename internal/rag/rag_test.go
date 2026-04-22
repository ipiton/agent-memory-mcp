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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/reranker"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

type failingCommitStore struct {
	vectorstore.Store
	failReadyCommitOnce bool
}

func (s *failingCommitStore) CommitIndexState(update vectorstore.IndexStateUpdate) error {
	if s.failReadyCommitOnce && update.Metadata[indexStateMetadataKey] == indexStateReady {
		s.failReadyCommitOnce = false
		return fmt.Errorf("injected final commit failure")
	}
	return s.Store.CommitIndexState(update)
}

func newTestEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/embed":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"embeddings": [][]float64{{0.9, 0.1}},
			})
		case "/api/embeddings":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"embedding": []float64{0.9, 0.1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func searchResultsWithScores(chunks []vectorstore.Chunk, scores map[string]float64) []vectorstore.SearchResult {
	results := make([]vectorstore.SearchResult, 0, len(scores))
	for _, chunk := range chunks {
		score, ok := scores[chunk.ID]
		if !ok {
			continue
		}
		results = append(results, vectorstore.SearchResult{
			Chunk: chunk,
			Score: score,
		})
	}
	return results
}

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

func TestIndexDocumentsRecoversAfterDirtyCommitFailure(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "runbook.md"), []byte("# Runbook\nrollback ingress safely"), 0o644); err != nil {
		t.Fatalf("write runbook: %v", err)
	}

	embeddingServer := newTestEmbeddingServer(t)
	defer embeddingServer.Close()

	emb, err := embedder.New(embedder.Config{
		OllamaBaseURL: embeddingServer.URL,
		Dimension:     2,
		Mode:          "local-only",
		MaxRetries:    0,
		Timeout:       time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("embedder.New: %v", err)
	}

	baseStore, err := vectorstore.NewSQLiteStore(filepath.Join(t.TempDir(), "vectors.db"), 2, zap.NewNop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = baseStore.Close() })

	store := &failingCommitStore{
		Store:               baseStore,
		failReadyCommitOnce: true,
	}

	engine := &Engine{
		config: config.Config{
			RootPath:      repoRoot,
			RAGMaxResults: 10,
		},
		repoRoot: repoRoot,
		logger:   zap.NewNop(),
		docService: newDocumentService(docServiceConfig{
			RepoRoot:     repoRoot,
			IndexDirs:    []string{"docs"},
			ChunkSize:    2000,
			ChunkOverlap: 200,
		}, zap.NewNop()),
		vecService: &vectorService{
			config: vecServiceConfig{
				Embedder:   emb,
				MaxResults: 10,
			},
			logger: zap.NewNop(),
			store:  store,
		},
		stopWatcher: make(chan struct{}),
	}

	err = engine.IndexDocuments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failed to commit index state") {
		t.Fatalf("first IndexDocuments error = %v, want final commit failure", err)
	}

	state, err := baseStore.GetMetadata(indexStateMetadataKey)
	if err != nil {
		t.Fatalf("GetMetadata(index_state) after failed run: %v", err)
	}
	if state != indexStateDirty {
		t.Fatalf("index_state after failed run = %q, want %q", state, indexStateDirty)
	}
	if baseStore.Count() == 0 {
		t.Fatal("expected chunks to remain after failed final commit")
	}

	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("second IndexDocuments: %v", err)
	}

	state, err = baseStore.GetMetadata(indexStateMetadataKey)
	if err != nil {
		t.Fatalf("GetMetadata(index_state) after retry: %v", err)
	}
	if state != indexStateReady {
		t.Fatalf("index_state after retry = %q, want %q", state, indexStateReady)
	}

	info, err := baseStore.GetIndexedFile("docs/runbook.md")
	if err != nil {
		t.Fatalf("GetIndexedFile after retry: %v", err)
	}
	if info == nil {
		t.Fatal("expected indexed file metadata after retry")
	}
	if info.ChunkCount != 1 {
		t.Fatalf("info.ChunkCount = %d, want 1", info.ChunkCount)
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
	toAdd, _ = e.calculateIndexChanges(docs, indexedFiles, true)
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

func TestDocumentServiceCollectDocumentsSkipsExcludedDir(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "docs", "private"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "public.md"), []byte("# Public\nhello"), 0o644); err != nil {
		t.Fatalf("write public: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "private", "secret.md"), []byte("# Secret\npassword: hunter2"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	ds := newDocumentService(docServiceConfig{
		RepoRoot:         repoRoot,
		IndexDirs:        []string{"docs"},
		IndexExcludeDirs: []string{"docs/private"},
		ChunkSize:        2000,
		ChunkOverlap:     200,
	}, nil)

	docs, err := ds.collectDocuments()
	if err != nil {
		t.Fatalf("collectDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1", len(docs))
	}
	if docs[0].Path != "docs/public.md" {
		t.Fatalf("docs[0].Path = %q, want docs/public.md", docs[0].Path)
	}
}

func TestDocumentServiceCollectDocumentsSkipsExcludedGlob(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "keep.md"), []byte("# Keep\nok"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docs", "draft-secret.md"), []byte("# Draft\nsecret: value"), 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	ds := newDocumentService(docServiceConfig{
		RepoRoot:          repoRoot,
		IndexDirs:         []string{"docs"},
		IndexExcludeGlobs: []string{"docs/draft-*.md"},
		ChunkSize:         2000,
		ChunkOverlap:      200,
	}, nil)

	docs, err := ds.collectDocuments()
	if err != nil {
		t.Fatalf("collectDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1", len(docs))
	}
	if docs[0].Path != "docs/keep.md" {
		t.Fatalf("docs[0].Path = %q, want docs/keep.md", docs[0].Path)
	}
}

func TestBuildHybridSearchResultsIncludesTrustMetadata(t *testing.T) {
	now := time.Now()
	chunks := []vectorstore.Chunk{
		{
			ID:           "adr-1",
			DocPath:      "docs/adr/0001-cache.md",
			Title:        "Cache invalidation",
			Content:      "Cache invalidation decision and tradeoffs",
			LastModified: now.Add(-24 * time.Hour),
			Embedding:    []float32{1, 0},
		},
		{
			ID:           "doc-1",
			DocPath:      "docs/cache-notes.md",
			Title:        "Cache invalidation",
			Content:      "Cache invalidation notes and reminders",
			LastModified: now.Add(-24 * time.Hour),
			Embedding:    []float32{1, 0},
		},
	}
	results, _, debugInfo := buildHybridSearchResults(
		"cache invalidation",
		"",
		searchResultsWithScores(chunks, map[string]float64{
			"adr-1": 1.0,
			"doc-1": 1.0,
		}),
		nil,
		len(chunks),
		10,
		true,
	)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].SourceType != "adr" {
		t.Fatalf("top SourceType = %q, want adr", results[0].SourceType)
	}
	if results[0].Trust == nil {
		t.Fatal("expected trust metadata on top result")
	}
	if results[0].Trust.Confidence <= results[1].Trust.Confidence {
		t.Fatalf("expected top confidence %.2f to exceed second %.2f", results[0].Trust.Confidence, results[1].Trust.Confidence)
	}
	if results[0].Debug == nil {
		t.Fatal("expected debug info on top result")
	}
	if results[0].Debug.Breakdown.ConfidenceBoost <= 0 {
		t.Fatalf("expected positive confidence boost, got %.3f", results[0].Debug.Breakdown.ConfidenceBoost)
	}
	if debugInfo == nil {
		t.Fatal("expected search debug info")
	}
	if !strings.Contains(strings.Join(debugInfo.RankingSignals, ","), "trust_confidence") {
		t.Fatalf("ranking signals = %v, want trust_confidence", debugInfo.RankingSignals)
	}
}

func TestRedactSensitiveContent(t *testing.T) {
	redacted := redactSensitiveContent(strings.Join([]string{
		"password: hunter2",
		"Authorization: Bearer abc123",
		"-----BEGIN PRIVATE KEY-----",
		"supersecret",
		"-----END PRIVATE KEY-----",
	}, "\n"))

	if strings.Contains(redacted, "hunter2") {
		t.Fatalf("redacted content still contains password value: %q", redacted)
	}
	if strings.Contains(redacted, "abc123") {
		t.Fatalf("redacted content still contains bearer token: %q", redacted)
	}
	if strings.Contains(redacted, "supersecret") {
		t.Fatalf("redacted content still contains private key content: %q", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("redacted content = %q, want [REDACTED]", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED PRIVATE KEY]") {
		t.Fatalf("redacted content = %q, want [REDACTED PRIVATE KEY]", redacted)
	}
}

func TestClassifySourceType(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "README.md", want: "docs"},
		{path: "docs/architecture/rfc-001.md", want: "rfc"},
		{path: "docs/adr/0001-cache.md", want: "adr"},
		{path: "runbooks/ingress-rollback.md", want: "runbook"},
		{path: "postmortems/2026-01-incident.md", want: "postmortem"},
		{path: "CHANGELOG.md", want: "changelog"},
		{path: ".github/workflows/deploy.yml", want: "ci_config"},
		{path: "helm/api/values.yaml", want: "helm"},
		{path: "terraform/modules/app/main.tf", want: "terraform"},
		{path: "k8s/ingress.yaml", want: "k8s"},
		{path: "dead_ends/why-we-avoid-async-migration.md", want: "dead_end"},
		{path: "notes/why-we-avoid-shared-mutable-state.md", want: "dead_end"},
	}

	for _, tc := range tests {
		if got := classifySourceType(tc.path, "", ""); got != tc.want {
			t.Fatalf("classifySourceType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSourceAwareBoostDeadEnd(t *testing.T) {
	if got := sourceAwareBoost("how to migrate async processing safely", "dead_end"); got <= 0 {
		t.Fatalf("sourceAwareBoost dead_end with keyword = %.3f, want >0", got)
	}
	if got := sourceAwareBoost("lesson learned from sharding", "dead_end"); got <= 0 {
		t.Fatalf("sourceAwareBoost dead_end with lesson keyword = %.3f, want >0", got)
	}
	if got := sourceAwareBoost("catalog service schema version", "dead_end"); got != 0 {
		t.Fatalf("sourceAwareBoost dead_end on neutral query = %.3f, want 0", got)
	}
	if got := sourceAwareBoost("", "dead_end"); got != 0 {
		t.Fatalf("sourceAwareBoost dead_end empty query = %.3f, want 0", got)
	}
	// Regression: word-boundary matching must suppress "try" inside "retry".
	if got := sourceAwareBoost("retry storm", "dead_end"); got != 0 {
		t.Fatalf("sourceAwareBoost dead_end on 'retry storm' = %.3f, want 0 (substring false positive)", got)
	}
	if got := sourceAwareBoost("unavoidable dependency graph", "dead_end"); got != 0 {
		t.Fatalf("sourceAwareBoost dead_end on 'unavoidable dependency graph' = %.3f, want 0 (substring false positive)", got)
	}
}

func TestBuildHybridSearchResultsKeywordBoostsRunbook(t *testing.T) {
	chunks := []vectorstore.Chunk{
		{
			ID:           "runbook",
			DocPath:      "runbooks/ingress-rollback.md",
			Title:        "Ingress rollback",
			Content:      "Rollback steps for ingress and controller recovery",
			LastModified: time.Now().Add(-24 * time.Hour),
			Embedding:    []float32{0, 1, 0},
		},
		{
			ID:           "generic-doc",
			DocPath:      "docs/networking.md",
			Title:        "Networking notes",
			Content:      "General networking background",
			LastModified: time.Now().Add(-24 * time.Hour),
			Embedding:    []float32{0.2, 0.98, 0},
		},
	}
	results, _, debug := buildHybridSearchResults(
		"rollback ingress",
		"",
		searchResultsWithScores(chunks, map[string]float64{
			"runbook":     0.05,
			"generic-doc": 0.30,
		}),
		searchResultsWithScores(chunks, map[string]float64{
			"runbook":     4.8,
			"generic-doc": 0.2,
		}),
		len(chunks),
		10,
		true,
	)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if debug == nil {
		t.Fatal("debug = nil, want populated debug info")
	}
	if debug.IndexedChunks != 2 {
		t.Fatalf("debug.IndexedChunks = %d, want 2", debug.IndexedChunks)
	}
	if results[0].Path != "runbooks/ingress-rollback.md" {
		t.Fatalf("results[0].Path = %q, want runbooks/ingress-rollback.md", results[0].Path)
	}
	if results[0].SourceType != "runbook" {
		t.Fatalf("results[0].SourceType = %q, want runbook", results[0].SourceType)
	}
	if results[0].Score <= results[1].Score {
		t.Fatalf("want keyword-boosted runbook to rank above generic doc: %f <= %f", results[0].Score, results[1].Score)
	}
	if results[0].Debug == nil {
		t.Fatal("results[0].Debug = nil, want score breakdown")
	}
	if results[0].Debug.Breakdown.KeywordNormalized <= 0 {
		t.Fatalf("KeywordNormalized = %f, want > 0", results[0].Debug.Breakdown.KeywordNormalized)
	}
	if len(results[0].Debug.AppliedBoosts) == 0 {
		t.Fatal("AppliedBoosts empty, want debug boosts")
	}
}

func TestBuildHybridSearchResultsAppliesRecencyBoost(t *testing.T) {
	chunks := []vectorstore.Chunk{
		{
			ID:           "old",
			DocPath:      "CHANGELOG-old.md",
			Title:        "Release notes",
			Content:      "Release migration steps for v1",
			LastModified: time.Now().Add(-365 * 24 * time.Hour),
			Embedding:    []float32{1, 0, 0},
		},
		{
			ID:           "recent",
			DocPath:      "CHANGELOG.md",
			Title:        "Release notes",
			Content:      "Release migration steps for v2",
			LastModified: time.Now().Add(-24 * time.Hour),
			Embedding:    []float32{1, 0, 0},
		},
	}
	results, _, debug := buildHybridSearchResults(
		"release migration",
		"changelog",
		searchResultsWithScores(chunks, map[string]float64{
			"old":    1.0,
			"recent": 1.0,
		}),
		searchResultsWithScores(chunks, map[string]float64{
			"old":    2.0,
			"recent": 2.0,
		}),
		len(chunks),
		10,
		true,
	)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if debug == nil {
		t.Fatal("debug = nil, want debug info")
	}
	if len(debug.AppliedFilters) != 1 || debug.AppliedFilters[0] != "source_type=changelog" {
		t.Fatalf("AppliedFilters = %v, want source_type=changelog", debug.AppliedFilters)
	}
	if results[0].Path != "CHANGELOG.md" {
		t.Fatalf("results[0].Path = %q, want CHANGELOG.md", results[0].Path)
	}
	if results[0].Score <= results[1].Score {
		t.Fatalf("want recent changelog to outrank old one: %f <= %f", results[0].Score, results[1].Score)
	}
	if results[0].Debug == nil || results[0].Debug.Breakdown.RecencyBoost <= results[1].Debug.Breakdown.RecencyBoost {
		t.Fatalf("expected recent result to have stronger recency boost: %+v vs %+v", results[0].Debug, results[1].Debug)
	}
}

func TestBuildHybridSearchResultsFiltersBySourceType(t *testing.T) {
	chunks := []vectorstore.Chunk{
		{
			ID:        "runbook",
			DocPath:   "runbooks/ingress-rollback.md",
			Title:     "Ingress rollback",
			Content:   "Rollback steps for ingress",
			Embedding: []float32{0, 1, 0},
		},
		{
			ID:        "changelog",
			DocPath:   "CHANGELOG.md",
			Title:     "Release notes",
			Content:   "Ingress rollback was changed in the last release",
			Embedding: []float32{1, 0, 0},
		},
	}
	results, _, debug := buildHybridSearchResults(
		"rollback ingress",
		"runbook",
		searchResultsWithScores(chunks, map[string]float64{
			"runbook":   0.4,
			"changelog": 0.7,
		}),
		searchResultsWithScores(chunks, map[string]float64{
			"runbook":   3.1,
			"changelog": 0.5,
		}),
		len(chunks),
		10,
		true,
	)

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if debug == nil {
		t.Fatal("debug = nil, want debug info")
	}
	if debug.FilteredOut != 1 {
		t.Fatalf("debug.FilteredOut = %d, want 1", debug.FilteredOut)
	}
	if debug.ReturnedCount != 1 {
		t.Fatalf("debug.ReturnedCount = %d, want 1", debug.ReturnedCount)
	}
	if results[0].SourceType != "runbook" {
		t.Fatalf("SourceType = %q, want runbook", results[0].SourceType)
	}
	if results[0].Path != "runbooks/ingress-rollback.md" {
		t.Fatalf("Path = %q, want runbooks/ingress-rollback.md", results[0].Path)
	}
}

// --- T44 neural reranker integration tests ---

// fakeReranker is an in-process Reranker that rewrites scores according to
// the ordering fn provided by the test. Used to verify the integration path
// in vectorService.search end-to-end.
type fakeReranker struct {
	score func(id string) float64
	// blockFor makes Rerank wait this long before responding. Used to
	// simulate a provider that misses its deadline.
	blockFor time.Duration
	// fail, when set, makes Rerank return an error instead.
	fail error
}

func (f *fakeReranker) Rerank(ctx context.Context, _ string, candidates []reranker.Candidate) ([]reranker.Scored, error) {
	if f.blockFor > 0 {
		select {
		case <-time.After(f.blockFor):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.fail != nil {
		return nil, f.fail
	}
	out := make([]reranker.Scored, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, reranker.Scored{ID: c.ID, Score: f.score(c.ID)})
	}
	return out, nil
}

// newRerankTestEngine builds a minimal engine with a seeded in-memory
// vector store and a stubbed embedder that the deterministic-embedding
// helper already handles. Use SetReranker to inject the fake.
func newRerankTestEngine(t *testing.T, chunks []vectorstore.Chunk) *Engine {
	t.Helper()

	embeddingServer := newTestEmbeddingServer(t)
	t.Cleanup(embeddingServer.Close)

	emb, err := embedder.New(embedder.Config{
		OllamaBaseURL: embeddingServer.URL,
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

	if err := store.Upsert(chunks); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	return &Engine{
		config: config.Config{
			RAGMaxResults: 10,
		},
		logger: zap.NewNop(),
		vecService: &vectorService{
			config: vecServiceConfig{
				Embedder:      emb,
				MaxResults:    10,
				RerankTopN:    40,
				RerankTimeout: time.Second,
			},
			logger: zap.NewNop(),
			store:  store,
		},
		stopWatcher: make(chan struct{}),
	}
}

// rerankTestChunks returns a fixture where hybrid search returns them in
// natural ID order; a reranker that inverts the score lets us verify reorder.
func rerankTestChunks() []vectorstore.Chunk {
	now := time.Now()
	return []vectorstore.Chunk{
		{ID: "first", DocPath: "docs/first.md", Title: "First", Content: "alpha beta gamma", LastModified: now, Embedding: []float32{0.9, 0.1}},
		{ID: "second", DocPath: "docs/second.md", Title: "Second", Content: "alpha beta", LastModified: now, Embedding: []float32{0.8, 0.2}},
		{ID: "third", DocPath: "docs/third.md", Title: "Third", Content: "alpha", LastModified: now, Embedding: []float32{0.7, 0.3}},
	}
}

func TestSearchAppliesReranker(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())

	// Reranker makes "third" the new top (highest score). Without rerank,
	// "third" would rank last (worst keyword/semantic overlap).
	engine.SetReranker(&fakeReranker{
		score: func(id string) float64 {
			switch id {
			case "third":
				return 0.99
			case "second":
				return 0.50
			default:
				return 0.10
			}
		},
	})

	resp, err := engine.Search(context.Background(), "alpha", 5, "", true)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) < 2 {
		t.Fatalf("results = %d, want ≥2", len(resp.Results))
	}
	if resp.Results[0].ID != "third" {
		t.Fatalf("top result = %q, want %q (reranker should have promoted it)", resp.Results[0].ID, "third")
	}
	if resp.Debug == nil {
		t.Fatal("Debug = nil, want populated debug info")
	}
	signals := strings.Join(resp.Debug.RankingSignals, ",")
	if !strings.Contains(signals, "+ neural_reranker") {
		t.Fatalf("ranking signals = %v, want '+ neural_reranker'", resp.Debug.RankingSignals)
	}
	if resp.Results[0].Debug == nil || resp.Results[0].Debug.Breakdown.RerankScore <= 0 {
		t.Fatalf("expected RerankScore > 0 on top result, got %+v", resp.Results[0].Debug)
	}
}

func TestSearchFallsBackOnRerankTimeout(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())

	// Tighten the timeout so we can fail fast without slowing CI.
	engine.vecService.config.RerankTimeout = 50 * time.Millisecond

	engine.SetReranker(&fakeReranker{
		blockFor: 2 * time.Second, // well past the 50ms timeout above
		score:    func(id string) float64 { return 0 },
	})

	started := time.Now()
	resp, err := engine.Search(context.Background(), "alpha", 5, "", true)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("Search should fall back on rerank timeout, got error: %v", err)
	}
	// Hybrid fallback must finish within roughly the rerank timeout plus
	// bookkeeping — certainly well under the reranker's blockFor.
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("Search took %v, want fast fallback (<1.5s)", elapsed)
	}
	if resp.Debug == nil {
		t.Fatal("Debug = nil")
	}
	signals := strings.Join(resp.Debug.RankingSignals, ",")
	if !strings.Contains(signals, "rerank_failed:") {
		t.Fatalf("ranking signals = %v, want rerank_failed marker", resp.Debug.RankingSignals)
	}
	if strings.Contains(signals, "+ neural_reranker") {
		t.Fatalf("ranking signals = %v, should NOT claim neural_reranker applied on failure", resp.Debug.RankingSignals)
	}
}

func TestSearchSkipsRerankWhenDisabled(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())
	// Deliberately no SetReranker call — feature off.

	resp, err := engine.Search(context.Background(), "alpha", 5, "", true)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.Debug == nil {
		t.Fatal("Debug = nil")
	}
	signals := strings.Join(resp.Debug.RankingSignals, ",")
	if strings.Contains(signals, "+ neural_reranker") {
		t.Fatalf("ranking signals = %v, want NO neural_reranker signal when disabled", resp.Debug.RankingSignals)
	}
	if strings.Contains(signals, "rerank_failed") {
		t.Fatalf("ranking signals = %v, want NO rerank_failed signal when disabled", resp.Debug.RankingSignals)
	}
}

func TestSearchRerankNonTimeoutError(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())
	engine.SetReranker(&fakeReranker{
		fail: errors.New("bogus json"),
	})

	resp, err := engine.Search(context.Background(), "alpha", 5, "", true)
	if err != nil {
		t.Fatalf("Search should fall back on rerank error, got: %v", err)
	}
	signals := strings.Join(resp.Debug.RankingSignals, ",")
	if !strings.Contains(signals, "rerank_failed") {
		t.Fatalf("ranking signals = %v, want rerank_failed marker on non-timeout error", resp.Debug.RankingSignals)
	}
}

func TestApplyRerankScoresPreservesTail(t *testing.T) {
	results := []SearchResult{
		{ID: "a", Score: 0.5, Debug: &ResultDebug{}},
		{ID: "b", Score: 0.4, Debug: &ResultDebug{}},
		{ID: "c", Score: 0.3, Debug: &ResultDebug{}},
		{ID: "d", Score: 0.2, Debug: &ResultDebug{}},
	}
	scored := []reranker.Scored{
		{ID: "b", Score: 0.9},
		{ID: "a", Score: 0.8},
	}

	reordered := applyRerankScores(results, scored, 2, 12)
	if len(reordered) != 4 {
		t.Fatalf("len = %d, want 4 (no item should be dropped)", len(reordered))
	}
	if reordered[0].ID != "b" || reordered[1].ID != "a" {
		t.Fatalf("head order = [%s, %s], want [b, a]", reordered[0].ID, reordered[1].ID)
	}
	if reordered[2].ID != "c" || reordered[3].ID != "d" {
		t.Fatalf("tail order = [%s, %s], want [c, d] preserved", reordered[2].ID, reordered[3].ID)
	}
	if reordered[0].Debug.Breakdown.RerankScore != 0.9 {
		t.Fatalf("RerankScore[0] = %v, want 0.9", reordered[0].Debug.Breakdown.RerankScore)
	}
	if reordered[0].Debug.Breakdown.RerankTimeMs != 12 {
		t.Fatalf("RerankTimeMs[0] = %v, want 12", reordered[0].Debug.Breakdown.RerankTimeMs)
	}
	if reordered[2].Debug.Breakdown.RerankScore != 0 {
		t.Fatalf("tail item RerankScore = %v, want 0 (untouched)", reordered[2].Debug.Breakdown.RerankScore)
	}
}

func TestRerankErrorReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"canceled", context.Canceled, "canceled"},
		{"http 500", errors.New("jina returned http status 500"), "http_error"},
		{"decode", errors.New("failed to decode response"), "decode"},
		{"bad index", errors.New("index 42 out of range"), "bad_index"},
		{"nil", nil, ""},
		{"generic", errors.New("network flap error"), "error"},
		{"unknown", errors.New("network flap"), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rerankErrorReason(tc.err); got != tc.want {
				t.Fatalf("rerankErrorReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// captureReranker records the candidates it is asked to rerank so tests can
// assert what was actually handed to the reranker (Content length, count, ...).
type captureReranker struct {
	mu       sync.Mutex
	received []reranker.Candidate
	score    func(id string) float64
}

func (c *captureReranker) Rerank(_ context.Context, _ string, candidates []reranker.Candidate) ([]reranker.Scored, error) {
	c.mu.Lock()
	c.received = append([]reranker.Candidate(nil), candidates...)
	c.mu.Unlock()
	out := make([]reranker.Scored, 0, len(candidates))
	for _, cd := range candidates {
		s := 0.0
		if c.score != nil {
			s = c.score(cd.ID)
		}
		out = append(out, reranker.Scored{ID: cd.ID, Score: s})
	}
	return out, nil
}

// TestSearchRerankerReceivesFullContent verifies Fix 1 (code review): the
// reranker must be fed the full chunk content, not the 200-char snippet.
// Jina cross-encoder has an 8k token window — truncating to 200 chars
// wastes most of the signal the model can use.
func TestSearchRerankerReceivesFullContent(t *testing.T) {
	// Build a chunk whose Content is comfortably longer than the 200-char
	// snippet cap so we can detect snippet vs full-content routing.
	longContent := strings.Repeat("alpha beta gamma delta epsilon. ", 40) // ~1.2 KB
	if len(longContent) <= 200 {
		t.Fatalf("fixture content must exceed 200 chars, got %d", len(longContent))
	}

	now := time.Now()
	chunks := []vectorstore.Chunk{
		{ID: "long", DocPath: "docs/long.md", Title: "Long", Content: longContent, LastModified: now, Embedding: []float32{0.9, 0.1}},
		{ID: "short", DocPath: "docs/short.md", Title: "Short", Content: "alpha beta", LastModified: now, Embedding: []float32{0.8, 0.2}},
	}
	engine := newRerankTestEngine(t, chunks)

	cap := &captureReranker{
		score: func(id string) float64 {
			if id == "long" {
				return 0.9
			}
			return 0.1
		},
	}
	engine.SetReranker(cap)

	if _, err := engine.Search(context.Background(), "alpha", 5, "", true); err != nil {
		t.Fatalf("Search: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.received) == 0 {
		t.Fatal("reranker received 0 candidates, want ≥1")
	}
	var longCand *reranker.Candidate
	for i := range cap.received {
		if cap.received[i].ID == "long" {
			longCand = &cap.received[i]
			break
		}
	}
	if longCand == nil {
		t.Fatalf("reranker did not receive the 'long' candidate; received IDs: %v", candidateIDs(cap.received))
	}
	// The snippet cap is 200 chars + "..." — assert we got the full content,
	// not the snippet-truncated version.
	if len(longCand.Content) <= 203 {
		t.Fatalf("Content len = %d, want > 203 (full chunk, not 200-char snippet)", len(longCand.Content))
	}
	if longCand.Content != longContent {
		t.Fatalf("Content mismatch: got %d bytes, want %d bytes exact match", len(longCand.Content), len(longContent))
	}
}

func candidateIDs(cands []reranker.Candidate) []string {
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.ID)
	}
	return ids
}

// TestApplyReranker_ClampsTopN_To_100 verifies Fix 4 (code review): the
// reranker call must cap top_n at 100 even if configured higher, because
// Jina's API caps at 100 documents per request.
func TestApplyReranker_ClampsTopN_To_100(t *testing.T) {
	now := time.Now()
	chunks := make([]vectorstore.Chunk, 0, 150)
	for i := 0; i < 150; i++ {
		chunks = append(chunks, vectorstore.Chunk{
			ID:           fmt.Sprintf("doc-%03d", i),
			DocPath:      fmt.Sprintf("docs/doc-%03d.md", i),
			Title:        fmt.Sprintf("Doc %03d", i),
			Content:      fmt.Sprintf("alpha content for document %d", i),
			LastModified: now,
			Embedding:    []float32{float32(i%10) / 10.0, float32((i+1)%10) / 10.0},
		})
	}
	engine := newRerankTestEngine(t, chunks)
	// Bump MaxResults/RerankTopN well above the clamp so the clamp is the
	// only thing that can limit the candidate count.
	engine.config.RAGMaxResults = 150
	engine.vecService.config.MaxResults = 150
	engine.vecService.config.RerankTopN = 200

	cap := &captureReranker{score: func(id string) float64 { return 0 }}
	engine.SetReranker(cap)

	if _, err := engine.Search(context.Background(), "alpha", 150, "", false); err != nil {
		t.Fatalf("Search: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.received) > 100 {
		t.Fatalf("reranker received %d candidates, want ≤ 100 (Jina cap)", len(cap.received))
	}
	if len(cap.received) < 100 {
		t.Fatalf("reranker received %d candidates, want exactly 100 (clamp should apply)", len(cap.received))
	}
}
