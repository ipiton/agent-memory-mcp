// Package rag provides RAG (Retrieval-Augmented Generation) with document indexing and hybrid retrieval.
package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/logger"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

// SearchResult represents a single document match from a RAG search query.
type SearchResult struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Path         string         `json:"path"`
	SourceType   string         `json:"source_type,omitempty"`
	Score        float64        `json:"score"`
	Snippet      string         `json:"snippet"`
	LastModified time.Time      `json:"last_modified"`
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
	config         config.Config
	repoRoot       string
	logger         *zap.Logger
	docService     *documentService
	vecService     *vectorService
	mu             sync.Mutex
	indexing       bool
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
}

type vecServiceConfig struct {
	IndexPath  string
	Embedder   *embedder.Embedder
	MaxResults int
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
	}
	if dsCfg.ChunkSize == 0 {
		dsCfg.ChunkSize = 2000
	}
	if dsCfg.ChunkOverlap == 0 {
		dsCfg.ChunkOverlap = 200
	}

	docSvc := newDocumentService(dsCfg, zapLogger)

	emb, err := embedder.New(embedder.Config{
		JinaToken:     cfg.JinaAPIKey,
		OpenAIToken:   cfg.OpenAIAPIKey,
		OpenAIBaseURL: cfg.OpenAIBaseURL,
		OpenAIModel:   cfg.OpenAIModel,
		OllamaBaseURL: cfg.OllamaBaseURL,
		Dimension:     cfg.EmbeddingDimension,
		Mode:          cfg.EmbeddingMode,
		MaxRetries:    2,
		Timeout:       10 * time.Second,
	}, zapLogger)
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

	vecSvc, err := newVectorService(vecServiceConfig{
		IndexPath:  cfg.RAGIndexPath,
		Embedder:   emb,
		MaxResults: cfg.RAGMaxResults,
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
func (re *Engine) IndexDocuments(ctx context.Context) error {
	if re == nil || re.docService == nil || re.vecService == nil {
		return fmt.Errorf("RAG engine not available")
	}

	startTime := time.Now().UTC()
	const chunkerVersion = "char-v1"

	allDocs, err := re.docService.collectDocuments()
	if err != nil {
		return fmt.Errorf("failed to collect documents: %w", err)
	}

	store := re.vecService.store
	oldModel, _ := store.GetMetadata("embedding_model")
	oldChunker, _ := store.GetMetadata("chunker_version")
	indexState, _ := store.GetMetadata(indexStateMetadataKey)

	needsRebuild := false
	if indexState == indexStateDirty {
		re.logger.Warn("Index state marked dirty - forcing rebuild to recover tracking consistency")
		needsRebuild = true
	}
	if oldModel != "" && len(allDocs) > 0 {
		currentModel, err := re.vecService.detectModelID(ctx, allDocs[0].Content)
		if err != nil {
			return fmt.Errorf("failed to detect current embedding model: %w", err)
		}
		if oldModel != currentModel {
			re.logger.Warn("Embedding model changed - full rebuild required",
				zap.String("old_model", oldModel),
				zap.String("current_model", currentModel),
			)
			needsRebuild = true
		}
	}
	if oldChunker != "" && oldChunker != chunkerVersion {
		re.logger.Warn("Chunker version changed - full rebuild required")
		needsRebuild = true
	}

	indexedFiles, err := store.GetAllIndexedFiles()
	if err != nil {
		indexedFiles = make(map[string]*vectorstore.IndexedFileInfo)
	}

	if len(allDocs) == 0 && len(indexedFiles) == 0 && !needsRebuild {
		return fmt.Errorf("no documents found to index")
	}

	toAdd, toRemove := re.calculateIndexChanges(allDocs, indexedFiles, needsRebuild)

	re.logger.Info("Indexing analysis",
		zap.Int("total_documents", len(allDocs)),
		zap.Int("indexed_files", len(indexedFiles)),
		zap.Int("documents_to_add", len(toAdd)),
		zap.Int("files_to_remove", len(toRemove)),
		zap.Bool("force_rebuild", needsRebuild),
	)

	if err := store.CommitIndexState(vectorstore.IndexStateUpdate{
		Metadata: map[string]string{
			indexStateMetadataKey: indexStateDirty,
			indexStartedAtKey:     startTime.Format(time.RFC3339),
		},
	}); err != nil {
		return fmt.Errorf("failed to mark index state dirty: %w", err)
	}

	if len(toRemove) > 0 {
		re.logger.Info("Removing deleted documents", zap.Int("count", len(toRemove)))
		for _, filePath := range toRemove {
			if err := re.vecService.removeDocument(filePath); err != nil {
				return fmt.Errorf("failed to remove indexed document %q: %w", filePath, err)
			}
		}
	}

	indexedModel := oldModel
	indexedFilesToUpsert := make(map[string]*vectorstore.IndexedFileInfo)
	failedFiles := make(map[string]struct{})

	if len(toAdd) > 0 {
		re.logger.Info("Starting to index documents", zap.Int("count", len(toAdd)))

		chunkCountsByFile := make(map[string]int)
		docsByID := make(map[string]document, len(toAdd))
		for _, doc := range toAdd {
			chunkCountsByFile[doc.Path]++
			docsByID[doc.ID] = doc
		}

		// Delete old chunks before re-indexing to prevent stale data.
		// Upsert (INSERT OR REPLACE) only replaces by chunk ID; if chunk
		// count changes, orphaned chunks with higher indices would remain.
		cleanedPaths := make(map[string]struct{})
		for _, doc := range toAdd {
			if _, done := cleanedPaths[doc.Path]; done {
				continue
			}
			cleanedPaths[doc.Path] = struct{}{}
			if err := re.vecService.removeDocument(doc.Path); err != nil {
				re.logger.Warn("Failed to clean old chunks before re-index",
					zap.String("path", doc.Path), zap.Error(err))
			}
		}

		batchSize := 50
		totalBatches := (len(toAdd) + batchSize - 1) / batchSize
		currentBatchModel := ""

		for batchNum := 0; batchNum < totalBatches; batchNum++ {
			start := batchNum * batchSize
			end := start + batchSize
			if end > len(toAdd) {
				end = len(toAdd)
			}
			batch := toAdd[start:end]

			re.logger.Info("Processing batch",
				zap.Int("batch", batchNum+1),
				zap.Int("total", totalBatches))

			result, err := re.vecService.indexDocuments(ctx, batch)
			if err != nil {
				return fmt.Errorf("failed to index: %w", err)
			}
			if result.ModelID != "" {
				if currentBatchModel == "" {
					currentBatchModel = result.ModelID
				} else if currentBatchModel != result.ModelID {
					return fmt.Errorf("embedding model changed during indexing: started with %s, got %s", currentBatchModel, result.ModelID)
				}
			}

			for _, failedID := range result.FailedIDs {
				doc, ok := docsByID[failedID]
				if !ok {
					continue
				}
				failedFiles[doc.Path] = struct{}{}
				delete(indexedFilesToUpsert, doc.Path)
			}

			for _, successID := range result.SuccessIDs {
				doc, ok := docsByID[successID]
				if !ok {
					continue
				}
				if _, failed := failedFiles[doc.Path]; failed {
					continue
				}
				if _, alreadyTracked := indexedFilesToUpsert[doc.Path]; alreadyTracked {
					continue
				}

				fileHash := doc.FileHash
				if fileHash == "" {
					fileHash = calculateFileHash(doc.Content)
				}

				chunkCount := chunkCountsByFile[doc.Path]
				if chunkCount == 0 {
					chunkCount = 1
				}

				indexedFilesToUpsert[doc.Path] = &vectorstore.IndexedFileInfo{
					FilePath:   doc.Path,
					Hash:       fileHash,
					ModTime:    doc.LastModified,
					Size:       int64(len(doc.Content)),
					ChunkCount: chunkCount,
				}
			}

			if len(failedFiles) > 0 {
				re.logger.Warn("Files with failed chunks will be re-indexed next cycle",
					zap.Int("failed_files", len(failedFiles)))
			}

			re.logger.Info("Batch indexed",
				zap.Int("success", len(result.SuccessIDs)),
				zap.Int("failed", len(result.FailedIDs)))

			if batchNum < totalBatches-1 {
				time.Sleep(50 * time.Millisecond)
			}
		}

		if currentBatchModel != "" {
			indexedModel = currentBatchModel
		}
	}

	deletePaths := append([]string(nil), toRemove...)
	for filePath := range failedFiles {
		deletePaths = append(deletePaths, filePath)
	}
	sort.Strings(deletePaths)

	upsertPaths := make([]string, 0, len(indexedFilesToUpsert))
	for filePath := range indexedFilesToUpsert {
		upsertPaths = append(upsertPaths, filePath)
	}
	sort.Strings(upsertPaths)

	upsertFiles := make([]*vectorstore.IndexedFileInfo, 0, len(upsertPaths))
	for _, filePath := range upsertPaths {
		upsertFiles = append(upsertFiles, indexedFilesToUpsert[filePath])
	}

	finalMetadata := map[string]string{
		"chunker_version":     chunkerVersion,
		indexStateMetadataKey: indexStateReady,
		indexStartedAtKey:     startTime.Format(time.RFC3339),
		"last_indexed":        time.Now().UTC().Format(time.RFC3339),
	}
	if indexedModel != "" {
		finalMetadata["embedding_model"] = indexedModel
	}
	if len(failedFiles) > 0 {
		finalMetadata[indexStateMetadataKey] = indexStateDirty
	}

	if err := store.CommitIndexState(vectorstore.IndexStateUpdate{
		Metadata:        finalMetadata,
		UpsertFiles:     upsertFiles,
		DeleteFilePaths: deletePaths,
	}); err != nil {
		return fmt.Errorf("failed to commit index state: %w", err)
	}

	duration := time.Since(startTime)
	re.logger.Info("Indexing completed", zap.Duration("duration", duration))

	if len(failedFiles) > 0 {
		return fmt.Errorf("indexing completed with %d failed file(s); index state remains dirty for recovery", len(failedFiles))
	}

	return nil
}

func (re *Engine) calculateIndexChanges(currentDocs []document, indexedFiles map[string]*vectorstore.IndexedFileInfo, forceRebuild bool) (toAdd []document, toRemove []string) {
	if forceRebuild {
		toAdd = append(toAdd, currentDocs...)
		for filePath := range indexedFiles {
			toRemove = append(toRemove, filePath)
		}
		return toAdd, toRemove
	}

	// Group docs by path for O(1) lookup
	docsByPath := make(map[string][]document)
	for _, doc := range currentDocs {
		docsByPath[doc.Path] = append(docsByPath[doc.Path], doc)
	}

	for filePath := range indexedFiles {
		if _, exists := docsByPath[filePath]; !exists {
			toRemove = append(toRemove, filePath)
		}
	}

	addedFiles := make(map[string]bool)

	for path, docs := range docsByPath {
		if addedFiles[path] {
			continue
		}

		indexed, exists := indexedFiles[path]
		if forceRebuild || !exists {
			toAdd = append(toAdd, docs...)
			addedFiles[path] = true
			continue
		}

		fileHash := docs[0].FileHash
		if fileHash == "" {
			fileHash = calculateFileHash(docs[0].Content)
		}

		if indexed.Hash != fileHash || indexed.ModTime.Before(docs[0].LastModified) {
			toAdd = append(toAdd, docs...)
			addedFiles[path] = true
		}
	}

	return toAdd, toRemove
}

func (re *Engine) autoIndexIfNeeded() {
	select {
	case <-time.After(2 * time.Second):
	case <-re.stopWatcher:
		return
	}

	if re.needsIndexing() {
		re.logger.Info("Index needs updating, starting auto-indexing")
		re.indexWithLock("initial_startup")
	} else {
		re.logger.Info("Index is up to date, skipping auto-indexing")
	}
}

func (re *Engine) startFileWatcher() {
	interval := re.config.WatchInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	debounceDuration := re.config.DebounceDuration
	if debounceDuration <= 0 {
		debounceDuration = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var pendingMu sync.Mutex
	var pendingReindex bool
	var debounceTimer *time.Timer

	for {
		select {
		case <-re.stopWatcher:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case <-ticker.C:
			if re.needsIndexing() {
				pendingMu.Lock()
				pendingReindex = true
				pendingMu.Unlock()

				if debounceTimer != nil {
					debounceTimer.Stop()
				}

				debounceTimer = time.AfterFunc(debounceDuration, func() {
					pendingMu.Lock()
					shouldIndex := pendingReindex
					pendingReindex = false
					pendingMu.Unlock()
					if shouldIndex {
						re.indexWithLock("file_watcher")
					}
				})
			}
		}
	}
}

func (re *Engine) indexWithLock(trigger string) {
	re.mu.Lock()
	if re.indexing {
		re.mu.Unlock()
		re.logger.Debug("Indexing already in progress, skipping", zap.String("trigger", trigger))
		return
	}
	re.indexing = true
	re.mu.Unlock()

	defer func() {
		re.mu.Lock()
		re.indexing = false
		re.mu.Unlock()
	}()

	re.logger.Info("Starting indexing", zap.String("trigger", trigger))
	err := re.IndexDocuments(context.Background())
	if err != nil {
		re.logger.Error("Indexing failed", zap.Error(err), zap.String("trigger", trigger))
	} else {
		re.logger.Info("Indexing completed successfully", zap.String("trigger", trigger))
		re.mu.Lock()
		re.lastIndexCheck = time.Now()
		re.mu.Unlock()
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

func (re *Engine) needsIndexing() bool {
	store := re.vecService.store

	indexedFiles, err := store.GetAllIndexedFiles()
	if err != nil {
		re.logger.Warn("Failed to get indexed files from store, indexing required",
			zap.Error(err),
			zap.String("store_type", "SQLiteStore"),
		)
		return true
	}
	if len(indexedFiles) == 0 {
		re.logger.Info("No indexed files found, indexing required",
			zap.String("repo_root", re.repoRoot),
		)
		return true
	}

	currentDocs, err := re.docService.collectDocuments()
	if err != nil {
		re.logger.Warn("Failed to check filesystem, indexing required",
			zap.Error(err),
			zap.String("repo_root", re.repoRoot),
		)
		return true
	}

	toAdd, toRemove := re.calculateIndexChanges(currentDocs, indexedFiles, false)

	needsIndex := len(toAdd) > 0 || len(toRemove) > 0
	if needsIndex {
		re.logger.Info("Index needs updating",
			zap.Int("indexed", len(indexedFiles)),
			zap.Int("current", len(currentDocs)),
			zap.Int("to_add", len(toAdd)),
			zap.Int("to_remove", len(toRemove)),
			zap.String("repo_root", re.repoRoot),
		)
	} else {
		re.logger.Debug("Index is up to date",
			zap.Int("indexed", len(indexedFiles)),
			zap.Int("current", len(currentDocs)),
		)
	}

	return needsIndex
}

// === Vector Service ===

type vectorService struct {
	config vecServiceConfig
	logger *zap.Logger
	store  vectorstore.Store
}

func newVectorService(cfg vecServiceConfig, logger *zap.Logger) (*vectorService, error) {
	if err := os.MkdirAll(cfg.IndexPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create index directory: %w", err)
	}

	dbPath := filepath.Join(cfg.IndexPath, "vectors.db")
	logger.Info("Using SQLite vector store", zap.String("db_path", dbPath))

	store, err := vectorstore.NewSQLiteStore(dbPath, cfg.Embedder.Dimension, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create vector store: %w", err)
	}

	return &vectorService{
		config: cfg,
		logger: logger,
		store:  store,
	}, nil
}

func (vs *vectorService) search(ctx context.Context, query searchQuery) (*SearchResponse, error) {
	startTime := time.Now()

	queryResult, err := vs.config.Embedder.EmbedQueryDetailed(ctx, query.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	storedModel, err := vs.store.GetMetadata("embedding_model")
	if err == nil && storedModel != "" && storedModel != queryResult.ModelID {
		return nil, fmt.Errorf("embedding model mismatch: index was built with %s but current query model is %s. Run index_documents to rebuild the index", storedModel, queryResult.ModelID)
	}

	semanticLimit := max(query.Limit*8, 50)
	keywordLimit := max(query.Limit*12, 100)

	semanticResults, err := vs.store.Search(queryResult.Embedding, semanticLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to load semantic candidates: %w", err)
	}

	keywordResults, err := vs.store.KeywordSearch(query.Query, keywordLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to load keyword candidates: %w", err)
	}

	searchResults, debugInfo := buildHybridSearchResults(query.Query, query.SourceType, semanticResults, keywordResults, vs.store.Count(), query.Limit, query.Debug)

	return &SearchResponse{
		Query:      query.Query,
		Results:    searchResults,
		TotalFound: len(searchResults),
		SearchTime: time.Since(startTime).Milliseconds(),
		Debug:      debugInfo,
	}, nil
}

func (vs *vectorService) indexDocuments(ctx context.Context, docs []document) (*indexResult, error) {
	result := &indexResult{
		SuccessIDs: make([]string, 0, len(docs)),
		FailedIDs:  make([]string, 0),
		Errors:     make([]error, 0),
	}

	// Collect texts for batch embedding
	texts := make([]string, len(docs))
	for i, doc := range docs {
		texts[i] = doc.Content
	}

	// Batch embed all texts at once
	batchResult, err := vs.config.Embedder.BatchEmbedDetailed(ctx, texts)
	if err != nil {
		// Batch failed entirely — mark all as failed
		for _, doc := range docs {
			result.FailedIDs = append(result.FailedIDs, doc.ID)
		}
		result.Errors = append(result.Errors, err)
		vs.logger.Error("Batch embedding failed", zap.Error(err), zap.Int("count", len(docs)))
		return result, nil
	}
	result.ModelID = batchResult.ModelID
	embeddings := batchResult.Embeddings

	var chunks []vectorstore.Chunk
	for i, doc := range docs {
		if i >= len(embeddings) || embeddings[i] == nil {
			vs.logger.Warn("Nil embedding for document", zap.String("id", doc.ID))
			result.FailedIDs = append(result.FailedIDs, doc.ID)
			result.Errors = append(result.Errors, fmt.Errorf("nil embedding for doc %s", doc.ID))
			continue
		}

		chunks = append(chunks, vectorstore.Chunk{
			ID:           doc.ID,
			DocPath:      doc.Path,
			Content:      doc.Content,
			Title:        doc.Title,
			LastModified: doc.LastModified,
			Embedding:    embeddings[i],
		})

		result.SuccessIDs = append(result.SuccessIDs, doc.ID)
	}

	if len(chunks) > 0 {
		if err := vs.store.Upsert(chunks); err != nil {
			vs.logger.Error("Failed to upsert chunks", zap.Error(err))
			return result, err
		}
	}

	return result, nil
}

func (vs *vectorService) detectModelID(ctx context.Context, text string) (string, error) {
	result, err := vs.config.Embedder.EmbedDetailed(ctx, text)
	if err != nil {
		return "", err
	}
	return result.ModelID, nil
}

func (vs *vectorService) removeDocument(path string) error {
	return vs.store.DeleteByDocPath(path)
}
