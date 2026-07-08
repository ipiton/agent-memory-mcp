// Package rag provides RAG (Retrieval-Augmented Generation) with document indexing and hybrid retrieval.
package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/reranker"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
	"go.uber.org/zap"
)

// SearchResult represents a single document match from a RAG search query.
type SearchResult struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Path       string `json:"path"`
	SourceType string `json:"source_type,omitempty"`
	// SectionPath is the breadcrumb segments parsed from the Markdown chunk
	// header, e.g. ["Deploy Runbook", "Rollback", "Network failure"]. Empty
	// for chunks indexed without structure-aware chunking (non-Markdown
	// sources, or content rewritten without a leading [breadcrumb]).
	SectionPath []string `json:"section_path,omitempty"`
	// SectionKey is the canonical " > "-joined string of SectionPath, suitable
	// as input to ExpandSection. Empty when SectionPath is empty.
	SectionKey   string          `json:"section_key,omitempty"`
	Score        float64         `json:"score"`
	Snippet      string          `json:"snippet"`
	LastModified time.Time       `json:"last_modified"`
	Trust        *trust.Metadata `json:"trust,omitempty"`
	Debug        *ResultDebug    `json:"debug,omitempty"`
}

// SearchResponse holds the results and metadata for a search query.
type SearchResponse struct {
	Query      string         `json:"query"`
	Results    []SearchResult `json:"results"`
	TotalFound int            `json:"total_found"`
	SearchTime int64          `json:"search_time_ms"`
	Debug      *SearchDebug   `json:"debug,omitempty"`
}

// ResultDebug explains how a single result was ranked.
type ResultDebug struct {
	Breakdown     ScoreBreakdown `json:"breakdown"`
	AppliedBoosts []string       `json:"applied_boosts,omitempty"`
}

// ScoreBreakdown exposes the score components for a single result.
type ScoreBreakdown struct {
	Semantic          float64 `json:"semantic"`
	KeywordRaw        float64 `json:"keyword_raw"`
	KeywordNormalized float64 `json:"keyword_normalized"`
	RecencyBoost      float64 `json:"recency_boost"`
	SourceBoost       float64 `json:"source_boost"`
	ConfidenceBoost   float64 `json:"confidence_boost"`
	FinalScore        float64 `json:"final_score"`
	// RerankScore is the neural reranker's relevance score for this result
	// when reranking was applied; 0 for hybrid-only / tail items. Present
	// only when SearchDebug.RankingSignals contains "+ neural_reranker".
	RerankScore float64 `json:"rerank_score,omitempty"`
	// RerankTimeMs is the total wall time of the reranker HTTP call, copied
	// onto every reranked result for convenience when debugging latency.
	RerankTimeMs int64 `json:"rerank_time_ms,omitempty"`
}

// SearchDebug explains filters and ranking signals applied to the whole response.
type SearchDebug struct {
	AppliedFilters   []string `json:"applied_filters,omitempty"`
	RankingSignals   []string `json:"ranking_signals"`
	IndexedChunks    int      `json:"indexed_chunks"`
	FilteredOut      int      `json:"filtered_out"`
	DiscardedAsNoise int      `json:"discarded_as_noise"`
	CandidateCount   int      `json:"candidate_count"`
	ReturnedCount    int      `json:"returned_count"`
}

// Engine provides document indexing and hybrid retrieval over a repository.
type Engine struct {
	config     config.Config
	repoRoot   string
	logger     *zap.Logger
	docService *documentService
	vecService *vectorService
	mu         sync.Mutex
	indexing   bool
	// indexMu serialises the whole IndexDocuments write path so two indexing
	// runs never write to vectors.db concurrently. The file-watcher path
	// (indexWithLock) coalesces debounced ticks via the `indexing` flag, but
	// the foreground index_documents MCP tool calls IndexDocuments directly —
	// without this mutex the two could race and hit SQLITE_BUSY (RC5 in
	// 06-planning/2026-05-05-sqlite-busy-incident.md). Foreground callers wait
	// here and then index for real, rather than being skipped.
	indexMu        sync.Mutex
	lastIndexCheck time.Time
	stopWatcher    chan struct{}
	stopOnce       sync.Once
	bgWG           sync.WaitGroup // tracks background goroutines for clean shutdown
}

type docServiceConfig struct {
	IndexDirs         []string
	IndexExcludeDirs  []string
	IndexExcludeGlobs []string
	RedactSecrets     bool
	RepoRoot          string
	ChunkSize         int
	ChunkOverlap      int
	// KeepNoise disables the heuristic noise filter (T49 slice 3) so
	// Table-of-Contents / References / Changelog sections are still indexed.
	// Default false — set via MCP_RAG_KEEP_NOISE=true.
	KeepNoise bool
}

type vecServiceConfig struct {
	IndexPath  string
	Embedder   embedder.Service
	MaxResults int
	// Reranker is optional. When nil the search path skips the rerank step
	// entirely. When non-nil, the top-RerankTopN hybrid candidates are passed
	// to Reranker.Rerank with a RerankTimeout-scoped context; any error or
	// timeout falls back to the hybrid ordering.
	Reranker      reranker.Reranker
	RerankTopN    int
	RerankTimeout time.Duration
}

type document struct {
	ID           string
	Content      string
	Title        string
	Path         string
	LastModified time.Time
	FileHash     string
}

type searchQuery struct {
	Query      string
	Limit      int
	SourceType string
	Debug      bool
}

type indexResult struct {
	SuccessIDs []string
	FailedIDs  []string
	Errors     []error
	ModelID    string
}

const (
	indexStateMetadataKey = "index_state"
	indexStateDirty       = "dirty"
	indexStateReady       = "ready"
	indexStartedAtKey     = "index_started_at"
)

// NewEngine creates a new Engine with the given configuration and optional file logger.
func NewEngine(cfg config.Config, fileLogger *logger.FileLogger) *Engine {
	var zapLogger *zap.Logger
	if fileLogger != nil {
		zapLogger = fileLogger.Logger
	} else {
		zapLogger = zap.NewNop()
	}

	repoRoot := cfg.RootPath

	indexDirs := cfg.IndexDirs
	if len(indexDirs) == 0 {
		indexDirs = []string{"docs"}
	}

	dsCfg := docServiceConfig{
		IndexDirs:         indexDirs,
		IndexExcludeDirs:  cfg.IndexExcludeDirs,
		IndexExcludeGlobs: cfg.IndexExcludeGlobs,
		RedactSecrets:     cfg.RedactSecrets,
		RepoRoot:          repoRoot,
		ChunkSize:         cfg.ChunkSize,
		ChunkOverlap:      cfg.ChunkOverlap,
		KeepNoise:         cfg.RagKeepNoise,
	}
	if dsCfg.ChunkSize == 0 {
		dsCfg.ChunkSize = 2000
	}
	if dsCfg.ChunkOverlap == 0 {
		dsCfg.ChunkOverlap = 200
	}

	docSvc := newDocumentService(dsCfg, zapLogger)

	emb, err := embedder.New(cfg.EmbedderConfig(), zapLogger)
	if err != nil {
		if fileLogger != nil {
			fileLogger.Error("Failed to initialize embedder",
				zap.Error(err),
				zap.String("jina_api_key_set", config.BoolToString(cfg.JinaAPIKey != "")),
				zap.String("ollama_url", cfg.OllamaBaseURL),
			)
		}
		return nil
	}

	// Build optional neural reranker. A disabled provider (empty or
	// "disabled") returns ErrDisabled which we treat as non-fatal: the
	// pipeline falls back to hybrid-only ranking. An unknown provider is
	// logged at Warn but also degrades gracefully — we do not want to break
	// the RAG engine over a misconfigured optional feature.
	var rerankProv reranker.Reranker
	if cfg.RerankEnabled {
		rp, rerr := reranker.New(reranker.Config{
			Provider: cfg.RerankProvider,
			Model:    cfg.JinaRerankerModel,
			APIKey:   cfg.JinaAPIKey,
			Timeout:  cfg.RerankTimeout,
			TopN:     cfg.RerankTopN,
		}, zapLogger)
		switch {
		case rerr == nil:
			rerankProv = rp
			zapLogger.Info("Neural reranker enabled",
				zap.String("provider", cfg.RerankProvider),
				zap.String("model", cfg.JinaRerankerModel),
				zap.Duration("timeout", cfg.RerankTimeout),
				zap.Int("top_n", cfg.RerankTopN),
			)
		case errors.Is(rerr, reranker.ErrDisabled):
			zapLogger.Info("Neural reranker disabled by provider config",
				zap.String("provider", cfg.RerankProvider),
			)
		default:
			zapLogger.Warn("Neural reranker init failed, falling back to hybrid-only",
				zap.Error(rerr),
				zap.String("provider", cfg.RerankProvider),
			)
		}
	}

	vecSvc, err := newVectorService(vecServiceConfig{
		IndexPath:     cfg.RAGIndexPath,
		Embedder:      emb,
		MaxResults:    cfg.RAGMaxResults,
		Reranker:      rerankProv,
		RerankTopN:    cfg.RerankTopN,
		RerankTimeout: cfg.RerankTimeout,
	}, zapLogger)
	if err != nil {
		if fileLogger != nil {
			fileLogger.Error("Failed to initialize vector service",
				zap.Error(err),
				zap.String("rag_index_path", cfg.RAGIndexPath),
			)
		}
		return nil
	}

	engine := &Engine{
		config:      cfg,
		repoRoot:    repoRoot,
		logger:      zapLogger,
		docService:  docSvc,
		vecService:  vecSvc,
		indexing:    false,
		stopWatcher: make(chan struct{}),
	}

	if cfg.AutoIndex {
		if fileLogger != nil {
			fileLogger.Info("Starting auto-indexing check")
		}
		engine.bgWG.Add(1)
		go func() {
			defer engine.bgWG.Done()
			engine.autoIndexIfNeeded()
		}()

		if cfg.FileWatcher {
			if fileLogger != nil {
				fileLogger.Info("Starting file watcher for auto-reindexing")
			}
			engine.bgWG.Add(1)
			go func() {
				defer engine.bgWG.Done()
				engine.startFileWatcher()
			}()
		}
	}

	if fileLogger != nil {
		fileLogger.Info("RAG engine created successfully",
			zap.String("repo_root", repoRoot),
			zap.Strings("index_dirs", indexDirs),
			zap.String("rag_index_path", cfg.RAGIndexPath),
		)
	}

	return engine
}

// Search performs a hybrid search query across indexed documents.
func (re *Engine) Search(ctx context.Context, query string, limit int, sourceType string, debug bool) (*SearchResponse, error) {
	if re == nil || re.vecService == nil {
		return nil, fmt.Errorf("RAG engine not available")
	}

	if limit <= 0 {
		limit = re.config.RAGMaxResults
	}
	if limit > re.config.RAGMaxResults {
		limit = re.config.RAGMaxResults
	}

	result, err := re.vecService.search(ctx, searchQuery{
		Query:      query,
		Limit:      limit,
		SourceType: sourceType,
		Debug:      debug,
	})
	if err != nil {
		re.logger.Error("Vector search failed",
			zap.Error(err),
			zap.String("query", query),
			zap.Int("limit", limit),
		)
		return nil, fmt.Errorf("search failed: %w", err)
	}

	return &SearchResponse{
		Query:      result.Query,
		Results:    result.Results,
		TotalFound: result.TotalFound,
		SearchTime: result.SearchTime,
		Debug:      result.Debug,
	}, nil
}

// IndexDocuments performs incremental indexing of documents in configured directories.
// SectionExpansion is the result of ExpandSection: the resolved doc and
// section, plus every chunk that belongs to the section in document order
// (with the breadcrumb prefix stripped from each chunk so callers see only
// the section body). FullText is the chunks joined by a blank line — useful
// for an LLM call that wants the entire section in one go.
type SectionExpansion struct {
	DocPath     string   `json:"doc_path"`
	SectionPath []string `json:"section_path"`
	SectionKey  string   `json:"section_key"`
	Chunks      []string `json:"chunks"`
	FullText    string   `json:"full_text"`
}

// ExpandSection returns every chunk belonging to (docPath, sectionKey) in
// document order. sectionKey is the canonical " > "-joined breadcrumb path
// surfaced by SearchResult.SectionKey — pass it back unchanged.
//
// Intended use: caller runs Search, finds a relevant chunk, then calls
// ExpandSection to load the whole containing section before handing context
// to the LLM. This is the "pointer-based context" half of T49: search
// returns a pointer (chunk + section), expansion materialises the full
// section.
//
// Returns an error if the engine has no vector store. Returns a result with
// Chunks==nil when no chunks match — callers should treat that as a
// not-found, not an error.
func (re *Engine) ExpandSection(_ context.Context, docPath, sectionKey string) (*SectionExpansion, error) {
	if re == nil || re.vecService == nil || re.vecService.store == nil {
		return nil, fmt.Errorf("RAG engine not available")
	}
	docPath = strings.TrimSpace(docPath)
	sectionKey = strings.TrimSpace(sectionKey)
	if docPath == "" || sectionKey == "" {
		return nil, fmt.Errorf("doc_path and section_key are required")
	}

	chunks, err := re.vecService.store.ChunksByDocPath(docPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load chunks for %s: %w", docPath, err)
	}

	expansion := &SectionExpansion{
		DocPath:    docPath,
		SectionKey: sectionKey,
	}
	for _, chunk := range chunks {
		path, body, ok := ExtractBreadcrumb(chunk.Content)
		if !ok || SectionKey(path) != sectionKey {
			continue
		}
		if expansion.SectionPath == nil {
			expansion.SectionPath = path
		}
		expansion.Chunks = append(expansion.Chunks, body)
	}
	if len(expansion.Chunks) > 0 {
		expansion.FullText = strings.Join(expansion.Chunks, "\n\n")
	}
	return expansion, nil
}

// SetReranker replaces the engine's neural reranker at runtime. It is
// intended for tests and harness wiring — production configures the reranker
// via config.Config. Passing nil disables the rerank step.
//
// The default timeout/top_n values kick in when they were never configured
// (zero on the underlying vecServiceConfig): 5 seconds and 40 candidates.
func (re *Engine) SetReranker(r reranker.Reranker) {
	if re == nil || re.vecService == nil {
		return
	}
	re.vecService.config.Reranker = r
	if re.vecService.config.RerankTimeout <= 0 {
		re.vecService.config.RerankTimeout = 5 * time.Second
	}
	if re.vecService.config.RerankTopN <= 0 {
		re.vecService.config.RerankTopN = 40
	}
}

// Stop gracefully stops the Engine, terminating the file watcher
// and waiting for background goroutines to finish.
func (re *Engine) Stop() {
	if re != nil {
		re.stopOnce.Do(func() {
			close(re.stopWatcher)
		})
		re.bgWG.Wait()
	}
}

// maxRerankTopN caps how many candidates we send to the reranker provider.
// Jina's /v1/rerank endpoint documents a 100-document-per-request limit;
// exceeding it trips a 400 at the provider and we fall back to hybrid, so
// clamp locally and log once per request when the clamp fires.
const maxRerankTopN = 100
