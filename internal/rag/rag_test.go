package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
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
	results, debugInfo := buildHybridSearchResults(
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
	results, debug := buildHybridSearchResults(
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
	results, debug := buildHybridSearchResults(
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
	results, debug := buildHybridSearchResults(
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
