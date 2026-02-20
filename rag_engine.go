package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RAGEngine provides embedded RAG functionality
type RAGEngine struct {
	config         Config
	repoRoot       string // Normalized repository root path (used everywhere)
	logger         *zap.Logger
	docService     *DocumentService
	vecService     *VectorService
	mu             sync.Mutex    // Protects indexing
	indexing       bool          // Flag to prevent concurrent indexing
	lastIndexCheck time.Time     // Last time we checked for changes
	stopWatcher    chan struct{} // Channel to stop the file watcher
}

// RAGSearchResult represents a single RAG search result
type RAGSearchResult struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Path         string    `json:"path"`
	Type         string    `json:"type"`
	Category     string    `json:"category"`
	TaskSlug     string    `json:"task_slug,omitempty"`
	TaskPhase    string    `json:"task_phase,omitempty"`
	Score        float64   `json:"score"`
	Snippet      string    `json:"snippet"`
	LastModified time.Time `json:"last_modified"`
}

// DocumentServiceConfig configures the document service
type DocumentServiceConfig struct {
	IndexDirs    []string // Directories to index (relative to RepoRoot)
	RepoRoot     string   // Repository root path
	ChunkSize    int
	ChunkOverlap int
}

// EmbedderConfig configures the embedding service
type EmbedderConfig struct {
	JinaToken      string
	OpenAIToken    string
	OpenAIBaseURL  string // OpenAI-compatible base URL (default: https://api.openai.com/v1)
	OpenAIModel    string // Embedding model (default: text-embedding-3-small)
	OllamaBaseURL  string
	Dimension      int // Required embedding dimension (default: 1024)
	MaxRetries     int
	Timeout        time.Duration
}

// VectorServiceConfig configures the vector service
type VectorServiceConfig struct {
	IndexPath  string
	Embedder   *Embedder
	MaxResults int
}

// NewRAGEngine creates a new embedded RAG engine
func NewRAGEngine(cfg Config, fileLogger *FileLogger) *RAGEngine {
	// Use file logger if available, otherwise create a no-op logger
	var logger *zap.Logger
	if fileLogger != nil {
		// Use file logger's underlying zap logger
		logger = fileLogger.logger
	} else {
		// Fallback: disable RAG logging to avoid mixing with MCP streams
		zapConfig := zap.NewProductionConfig()
		zapConfig.Level = zap.NewAtomicLevelAt(zap.FatalLevel) // Only fatal errors
		zapConfig.OutputPaths = []string{"/dev/null"}
		logger, _ = zapConfig.Build()
	}

	// Use repository root from config (already normalized in LoadConfig)
	repoRoot := cfg.RootPath

	// Build document service config from Config
	indexDirs := cfg.IndexDirs
	if len(indexDirs) == 0 {
		indexDirs = []string{"docs"}
	}
	// Add changelog if configured
	if cfg.ChangelogPath != "" {
		indexDirs = append(indexDirs, cfg.ChangelogPath)
	} else {
		indexDirs = append(indexDirs, "CHANGELOG.md")
	}

	docServiceCfg := DocumentServiceConfig{
		IndexDirs:    indexDirs,
		RepoRoot:     repoRoot,
		ChunkSize:    cfg.ChunkSize,
		ChunkOverlap: cfg.ChunkOverlap,
	}
	if docServiceCfg.ChunkSize == 0 {
		docServiceCfg.ChunkSize = 2000
	}
	if docServiceCfg.ChunkOverlap == 0 {
		docServiceCfg.ChunkOverlap = 200
	}

	docService := NewDocumentService(docServiceCfg, logger)

	// Initialize embedder with config
	embedder, err := NewEmbedder(EmbedderConfig{
		JinaToken:     cfg.JinaAPIKey,
		OpenAIToken:   cfg.OpenAIAPIKey,
		OpenAIBaseURL: cfg.OpenAIBaseURL,
		OpenAIModel:   cfg.OpenAIModel,
		OllamaBaseURL: cfg.OllamaBaseURL,
		Dimension:     cfg.EmbeddingDimension,
		MaxRetries:    2,
		Timeout:       10 * time.Second,
	}, logger)
	if err != nil {
		if fileLogger != nil {
			fileLogger.Error("Failed to initialize embedder",
				zap.Error(err),
				zap.String("jina_api_key_set", boolToString(cfg.JinaAPIKey != "")),
				zap.String("ollama_url", cfg.OllamaBaseURL),
			)
		}
		return nil
	}

	// Use RAG index path from config
	vecService, err := NewVectorService(VectorServiceConfig{
		IndexPath:  cfg.RAGIndexPath,
		Embedder:   embedder,
		MaxResults: cfg.RAGMaxResults,
	}, logger)

	if err != nil {
		if fileLogger != nil {
			fileLogger.Error("Failed to initialize vector service",
				zap.Error(err),
				zap.String("rag_index_path", cfg.RAGIndexPath),
			)
		}
		return nil
	}

	ragEngine := &RAGEngine{
		config:      cfg,
		repoRoot:    repoRoot,
		logger:      logger,
		docService:  docService,
		vecService:  vecService,
		indexing:    false,
		stopWatcher: make(chan struct{}),
	}

	// Auto-index if needed
	autoIndex := envOrDefault("MCP_RAG_AUTO_INDEX", "true")
	if autoIndex == "true" {
		if fileLogger != nil {
			fileLogger.Info("Starting auto-indexing check")
		}
		go ragEngine.autoIndexIfNeeded()

		// File watcher: enabled by default when RAG is on, can be disabled explicitly
		watcherDisabled := envOrDefault("MCP_RAG_FILE_WATCHER", "true")
		if watcherDisabled == "true" {
			if fileLogger != nil {
				fileLogger.Info("Starting file watcher for auto-reindexing")
			}
			go ragEngine.startFileWatcher()
		}
	}

	if fileLogger != nil {
		fileLogger.Info("RAG engine created successfully",
			zap.String("repo_root", repoRoot),
			zap.Strings("index_dirs", indexDirs),
			zap.String("rag_index_path", cfg.RAGIndexPath),
		)
	}

	return ragEngine
}

// Search performs a search query
func (re *RAGEngine) Search(query string, limit int, docType, category string) (*RAGSearchResponse, error) {
	if re == nil || re.vecService == nil {
		return nil, fmt.Errorf("RAG engine not available")
	}

	// NOTE: On-demand indexing disabled for performance
	// Use index_documents tool explicitly to trigger reindexing
	// The old code was causing search timeouts with large document sets

	if limit <= 0 {
		limit = re.config.RAGMaxResults
	}
	if limit > re.config.RAGMaxResults {
		limit = re.config.RAGMaxResults
	}

	// Build filters
	filters := make(map[string]interface{})
	if docType != "" && docType != "all" {
		filters["type"] = docType
	}
	if category != "" {
		filters["category"] = category
	}

	// Perform search
	result, err := re.vecService.Search(SearchQuery{
		Query:   query,
		Limit:   limit,
		Filters: filters,
	})
	if err != nil {
		re.logger.Error("Vector search failed",
			zap.Error(err),
			zap.String("query", query),
			zap.Int("limit", limit),
		)
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Convert to response format
	searchResponse := &RAGSearchResponse{
		Query:      result.Query,
		Results:    result.Results,
		TotalFound: result.TotalFound,
		SearchTime: result.SearchTime,
	}

	return searchResponse, nil
}

// IndexDocuments performs incremental indexing of documents
func (re *RAGEngine) IndexDocuments() error {
	if re == nil || re.docService == nil || re.vecService == nil {
		return fmt.Errorf("RAG engine not available")
	}

	startTime := time.Now()

	// Current embedding configuration
	const embeddingModel = "jina-v3-bge-m3-1024d"
	const chunkerVersion = "char-v1"

	// Check if embedding model or chunker changed (stored in SQLite)
	store := re.vecService.store.(*SQLiteVectorStore)
	oldModel, _ := store.GetMetadata("embedding_model")
	oldChunker, _ := store.GetMetadata("chunker_version")

	needsRebuild := false
	if oldModel != "" && oldModel != embeddingModel {
		re.logger.Warn("Embedding model changed - full rebuild required")
		needsRebuild = true
	}
	if oldChunker != "" && oldChunker != chunkerVersion {
		re.logger.Warn("Chunker version changed - full rebuild required")
		needsRebuild = true
	}

	// Update metadata
	store.SetMetadata("embedding_model", embeddingModel)
	store.SetMetadata("chunker_version", chunkerVersion)

	// Collect all current documents
	allDocs, err := re.docService.CollectDocuments()
	if err != nil {
		return fmt.Errorf("failed to collect documents: %w", err)
	}

	if len(allDocs) == 0 {
		return fmt.Errorf("no documents found to index")
	}

	// Get indexed files from SQLite
	indexedFiles, err := store.GetAllIndexedFiles()
	if err != nil {
		indexedFiles = make(map[string]*IndexedFileInfo)
	}

	// Find documents to add/update and remove
	toAdd, toRemove := re.calculateIndexChangesSQL(allDocs, indexedFiles, needsRebuild)

	// Log indexing summary
	re.logger.Info("Indexing analysis",
		zap.Int("total_documents", len(allDocs)),
		zap.Int("indexed_files", len(indexedFiles)),
		zap.Int("documents_to_add", len(toAdd)),
		zap.Int("files_to_remove", len(toRemove)),
		zap.Bool("force_rebuild", needsRebuild),
	)

	// Handle deleted documents
	if len(toRemove) > 0 {
		re.logger.Info("Removing deleted documents", zap.Int("count", len(toRemove)))
		for _, filePath := range toRemove {
			if err := re.vecService.RemoveDocument(filePath); err != nil {
				re.logger.Warn("Failed to remove document", zap.String("path", filePath), zap.Error(err))
			}
			store.DeleteIndexedFile(filePath)
		}
	}

	// Index new/changed documents
	if len(toAdd) > 0 {
		re.logger.Info("Starting to index documents", zap.Int("count", len(toAdd)))

		// Count chunks per file across all documents to be indexed
		chunkCountsByFile := make(map[string]int)
		for _, doc := range toAdd {
			chunkCountsByFile[doc.Path]++
		}

		batchSize := 10
		totalBatches := (len(toAdd) + batchSize - 1) / batchSize

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

			result, err := re.vecService.IndexDocuments(batch)
			if err != nil {
				return fmt.Errorf("failed to index: %w", err)
			}

			// Update indexed_files in SQLite for successful files (once per file, not per chunk)
			// Track which files we've already updated
			updatedFiles := make(map[string]bool)
			
			for _, successID := range result.SuccessIDs {
				for _, doc := range batch {
					if doc.ID == successID && !updatedFiles[doc.Path] {
						// Use FileHash (hash of entire file) instead of chunk hash
						fileHash := doc.FileHash
						if fileHash == "" {
							// Fallback: calculate hash if not set
							fileHash = re.calculateFileHash(doc.Content)
						}
						
						// Get total chunk count for this file (from all batches)
						chunkCount := chunkCountsByFile[doc.Path]
						if chunkCount == 0 {
							chunkCount = 1 // Fallback
						}
						
						store.SetIndexedFile(&IndexedFileInfo{
							FilePath:   doc.Path,
							Hash:       fileHash, // Hash of entire file
							ModTime:    doc.LastModified,
							Size:       int64(len(doc.Content)), // Size is approximate (one chunk)
							ChunkCount: chunkCount, // Total chunks for this file
						})
						updatedFiles[doc.Path] = true
						break
					}
				}
			}

			re.logger.Info("Batch indexed",
				zap.Int("success", len(result.SuccessIDs)),
				zap.Int("failed", len(result.FailedIDs)))

			// Rate limiting
			if batchNum < totalBatches-1 {
				time.Sleep(500 * time.Millisecond)
			}
		}
	}

	// Update last indexed time
	store.SetMetadata("last_indexed", time.Now().Format(time.RFC3339))

	duration := time.Since(startTime)
	re.logger.Info("Indexing completed", zap.Duration("duration", duration))

	return nil
}

// calculateIndexChangesSQL determines what to add/remove using SQLite metadata
func (re *RAGEngine) calculateIndexChangesSQL(currentDocs []Document, indexedFiles map[string]*IndexedFileInfo, forceRebuild bool) (toAdd []Document, toRemove []string) {
	// Build map of current files by path (keep first document per file for metadata)
	currentByPath := make(map[string]Document)
	for _, doc := range currentDocs {
		if _, exists := currentByPath[doc.Path]; !exists {
			currentByPath[doc.Path] = doc
		}
		// All chunks for same file have same FileHash, Path, ModTime - we just need one for comparison
	}

	// Find files to remove (in index but not in filesystem)
	for filePath := range indexedFiles {
		if _, exists := currentByPath[filePath]; !exists {
			toRemove = append(toRemove, filePath)
		}
	}

	// Track which files we've already added (to avoid adding same file multiple times)
	addedFiles := make(map[string]bool)

	// Find files to add/update
	for _, doc := range currentDocs {
		// Skip if we already added this file
		if addedFiles[doc.Path] {
			continue
		}

		indexed, exists := indexedFiles[doc.Path]
		if forceRebuild || !exists {
			// Add all chunks for this file
			for _, d := range currentDocs {
				if d.Path == doc.Path {
					toAdd = append(toAdd, d)
				}
			}
			addedFiles[doc.Path] = true
			continue
		}

		// Check if file changed (by file hash or mod time)
		// Use FileHash which is hash of entire file, not individual chunk
		fileHash := doc.FileHash
		if fileHash == "" {
			// Fallback: calculate hash if not set (shouldn't happen with new code)
			fileHash = re.calculateFileHash(doc.Content)
		}

		if indexed.Hash != fileHash || indexed.ModTime.Before(doc.LastModified) {
			// File changed - add all chunks for this file
			for _, d := range currentDocs {
				if d.Path == doc.Path {
					toAdd = append(toAdd, d)
				}
			}
			addedFiles[doc.Path] = true
		}
	}

	return toAdd, toRemove
}

// SearchQuery represents a search query
type SearchQuery struct {
	Query   string
	Limit   int
	Filters map[string]interface{}
}

// SearchResponse represents the search response
type RAGSearchResponse struct {
	Query      string            `json:"query"`
	Results    []RAGSearchResult `json:"results"`
	TotalFound int               `json:"total_found"`
	SearchTime int64             `json:"search_time_ms"`
}


// Document represents a document for indexing
type Document struct {
	ID           string
	Content      string
	Title        string
	Path         string
	Type         string
	Category     string
	TaskSlug     string
	TaskPhase    string
	LastModified time.Time
	FileHash     string // Hash of the entire file (before chunking) for change detection
}

// DocumentService handles document collection (simplified version)
type DocumentService struct {
	config DocumentServiceConfig
	logger *zap.Logger
}

// NewDocumentService creates a new document service
func NewDocumentService(config DocumentServiceConfig, logger *zap.Logger) *DocumentService {
	return &DocumentService{
		config: config,
		logger: logger,
	}
}

// CollectDocuments collects all markdown documents from configured directories
func (ds *DocumentService) CollectDocuments() ([]Document, error) {
	var allDocs []Document

	for _, dir := range ds.config.IndexDirs {
		fullPath := filepath.Join(ds.config.RepoRoot, dir)

		// Check if it's a file (like CHANGELOG.md)
		info, err := os.Stat(fullPath)
		if err != nil {
			ds.logger.Warn("Path not found", zap.String("path", fullPath))
			continue
		}

		if !info.IsDir() {
			// Single file
			if strings.HasSuffix(fullPath, ".md") {
				docs, err := ds.processFile(fullPath)
				if err != nil {
					ds.logger.Warn("Failed to process file", zap.String("path", fullPath), zap.Error(err))
	} else {
					allDocs = append(allDocs, docs...)
				}
			}
			continue
		}

		// Directory - walk it
		err = filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".md") {
				docs, err := ds.processFile(path)
				if err != nil {
					ds.logger.Warn("Failed to process file", zap.String("path", path), zap.Error(err))
					return nil
				}
				allDocs = append(allDocs, docs...)
			}
			return nil
		})
		if err != nil {
			ds.logger.Error("Failed to walk directory", zap.String("path", fullPath), zap.Error(err))
		}
	}

	return allDocs, nil
}

// getDocType determines document type from file path
func (ds *DocumentService) getDocType(path string) string {
	relPath := strings.TrimPrefix(path, ds.config.RepoRoot)
	relPath = strings.TrimPrefix(relPath, "/")

	switch {
	case strings.HasPrefix(relPath, "tasks/"):
		return "tasks"
	case strings.HasPrefix(relPath, "memory-bank/"):
		return "memory"
	case strings.HasPrefix(relPath, "docs/"):
		return "docs"
	case strings.Contains(relPath, "CHANGELOG"):
		return "changelog"
	default:
		return "docs"
	}
}

// processFile processes a single markdown file
func (ds *DocumentService) processFile(path string) ([]Document, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	docType := ds.getDocType(path)
	relPath := strings.TrimPrefix(path, ds.config.RepoRoot)
	relPath = strings.TrimPrefix(relPath, "/")

	// Extract title and clean content
	title := ds.extractTitle(string(content), filepath.Base(path))
	cleanContent := ds.removeFrontmatter(string(content))

	// Calculate file hash once for the entire file (before chunking)
	fileHash := ds.calculateFileHash(cleanContent)
	modTime := ds.getFileModTime(path)

	// Extract task info if it's a task file
	var taskSlug, taskPhase string
	if docType == "tasks" {
		taskSlug, taskPhase = ds.extractTaskInfo(relPath)
	}

	// Split into chunks
	chunks := ds.splitIntoChunks(cleanContent)

	var docs []Document
	for i, chunk := range chunks {
		doc := Document{
			ID:           fmt.Sprintf("%s-%d", relPath, i),
			Content:      chunk,
			Title:        title,
			Path:         relPath,
			Type:         docType,
			Category:     ds.extractCategory(relPath),
			TaskSlug:     taskSlug,
			TaskPhase:    taskPhase,
			LastModified: modTime,
			FileHash:     fileHash, // Store file hash for comparison
		}
		docs = append(docs, doc)
	}

	return docs, nil
}

// VectorService handles vector operations with SQLite store
type VectorService struct {
	config VectorServiceConfig
	logger *zap.Logger
	store  VectorStore
}

// NewVectorService creates a new vector service with SQLite backend
func NewVectorService(config VectorServiceConfig, logger *zap.Logger) (*VectorService, error) {
	// Ensure index directory exists
	if err := os.MkdirAll(config.IndexPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create index directory: %w", err)
	}

	// Use SQLite-backed vector store
	dbPath := filepath.Join(config.IndexPath, "vectors.db")
	logger.Info("Using SQLite vector store",
		zap.String("db_path", dbPath))

	store, err := NewSQLiteVectorStore(dbPath, config.Embedder.dimension, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create vector store: %w", err)
	}

	return &VectorService{
		config: config,
		logger: logger,
		store:  store,
	}, nil
}


// Search performs vector search using the store
func (vs *VectorService) Search(query SearchQuery) (*RAGSearchResponse, error) {
	startTime := time.Now()

	// Generate embedding for the query using query-optimized task type
	queryEmbedding, err := vs.config.Embedder.EmbedQuery(query.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// Convert filters to string map
	var filters map[string]string
	if query.Filters != nil {
		filters = make(map[string]string)
		for k, v := range query.Filters {
			if str, ok := v.(string); ok && str != "" {
				filters[k] = str
			}
		}
	}

	// Perform search with filtering built into store
	results, err := vs.store.Search(queryEmbedding, filters, query.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}

	// Convert to RAGSearchResult
	var searchResults []RAGSearchResult
	for _, result := range results {
		// Extract snippet (first 200 characters)
		snippet := result.Content
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}

		searchResults = append(searchResults, RAGSearchResult{
			ID:           result.ID,
			Title:        result.Title,
			Path:         result.Path,
			Type:         result.Type,
			Category:     result.Category,
			TaskSlug:     result.TaskSlug,
			TaskPhase:    result.TaskPhase,
			Score:        result.Score,
			Snippet:      snippet,
			LastModified: result.LastModified,
		})
	}

	return &RAGSearchResponse{
		Query:      query.Query,
		Results:    searchResults,
		TotalFound: len(searchResults),
		SearchTime: time.Since(startTime).Milliseconds(),
	}, nil
}

// autoIndexIfNeeded performs automatic indexing if the index is empty or stale
func (re *RAGEngine) autoIndexIfNeeded() {
	// Give some time for MCP server to fully initialize
	time.Sleep(2 * time.Second)

	// Check if index needs updating
	if re.needsIndexing() {
		re.logger.Info("Index needs updating, starting auto-indexing")
		re.indexWithLock("initial_startup")
	} else {
		re.logger.Info("Index is up to date, skipping auto-indexing")
	}
}

// startFileWatcher monitors file changes and triggers reindexing
func (re *RAGEngine) startFileWatcher() {
	// Get check interval from env or use default (5 minutes)
	intervalStr := envOrDefault("MCP_RAG_WATCH_INTERVAL", "5m")
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		interval = 5 * time.Minute
	}

	// Get debounce duration from env or use default (30 seconds)
	debounceStr := envOrDefault("MCP_RAG_DEBOUNCE", "30s")
	debounceDuration, err := time.ParseDuration(debounceStr)
	if err != nil {
		debounceDuration = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

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
			// Check if files have changed
			if re.needsIndexing() {
				// Set pending reindex and start/reset debounce timer
				pendingReindex = true

				if debounceTimer != nil {
					debounceTimer.Stop()
				}

				debounceTimer = time.AfterFunc(debounceDuration, func() {
					if pendingReindex {
						re.indexWithLock("file_watcher")
						pendingReindex = false
					}
				})
			}
		}
	}
}

// indexWithLock performs indexing with concurrency protection using mutex
func (re *RAGEngine) indexWithLock(trigger string) {
	// Try to acquire lock (non-blocking check)
	re.mu.Lock()
	if re.indexing {
		re.mu.Unlock()
		re.logger.Debug("Indexing already in progress, skipping", zap.String("trigger", trigger))
		return
	}
	re.indexing = true
	re.mu.Unlock()

	// Ensure we release the indexing flag when done
	defer func() {
		re.mu.Lock()
		re.indexing = false
		re.mu.Unlock()
	}()

	re.logger.Info("Starting indexing", zap.String("trigger", trigger))
	err := re.IndexDocuments()
	if err != nil {
		re.logger.Error("Indexing failed", zap.Error(err), zap.String("trigger", trigger))
	} else {
		re.logger.Info("Indexing completed successfully", zap.String("trigger", trigger))
		re.mu.Lock()
		re.lastIndexCheck = time.Now()
		re.mu.Unlock()
	}
}

// Stop gracefully stops the RAG engine
func (re *RAGEngine) Stop() {
	if re != nil {
		close(re.stopWatcher)
	}
}

// needsIndexing checks if documents need to be indexed
func (re *RAGEngine) needsIndexing() bool {
	store := re.vecService.store.(*SQLiteVectorStore)

	// Get indexed files from SQLite
	indexedFiles, err := store.GetAllIndexedFiles()
	if err != nil {
		re.logger.Warn("Failed to get indexed files from store, indexing required",
			zap.Error(err),
			zap.String("store_type", "SQLiteVectorStore"),
		)
		return true
	}
	if len(indexedFiles) == 0 {
		re.logger.Info("No indexed files found, indexing required",
			zap.String("repo_root", re.repoRoot),
		)
		return true
	}

	// Check if any files have changed
	currentDocs, err := re.docService.CollectDocuments()
	if err != nil {
		re.logger.Warn("Failed to check filesystem, indexing required",
			zap.Error(err),
			zap.String("repo_root", re.repoRoot),
		)
		return true
	}

	toAdd, toRemove := re.calculateIndexChangesSQL(currentDocs, indexedFiles, false)

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


// calculateFileHash calculates MD5 hash of content
func (re *RAGEngine) calculateFileHash(content string) string {
	hasher := md5.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}


// IndexResult contains the result of indexing operation
type IndexResult struct {
	SuccessIDs []string // IDs of successfully indexed documents
	FailedIDs  []string // IDs of failed documents
	Errors     []error  // Errors for failed documents
}

// IndexDocuments indexes documents using the store
func (vs *VectorService) IndexDocuments(docs []Document) (*IndexResult, error) {
	result := &IndexResult{
		SuccessIDs: make([]string, 0, len(docs)),
		FailedIDs:  make([]string, 0),
		Errors:     make([]error, 0),
	}

	// Convert documents to chunks with embeddings
	var chunks []Chunk
	for i, doc := range docs {
		// Rate limiting: delay BEFORE request (except first)
		if i > 0 {
			time.Sleep(200 * time.Millisecond)
		}

		// Generate embedding for the document
		embedding, err := vs.config.Embedder.Embed(doc.Content)
		if err != nil {
			vs.logger.Warn("Failed to embed document", zap.String("id", doc.ID), zap.Error(err))
			result.FailedIDs = append(result.FailedIDs, doc.ID)
			result.Errors = append(result.Errors, err)
			continue
		}

		chunks = append(chunks, Chunk{
			ID:           doc.ID,
			DocPath:      doc.Path, // Original document path for deletion
			Content:      doc.Content,
			Title:        doc.Title,
			Path:         doc.Path,
			Type:         doc.Type,
			Category:     doc.Category,
			TaskSlug:     doc.TaskSlug,
			TaskPhase:    doc.TaskPhase,
			LastModified: doc.LastModified,
			Embedding:    embedding,
		})

		result.SuccessIDs = append(result.SuccessIDs, doc.ID)
	}

	// Batch upsert all chunks
	if len(chunks) > 0 {
		if err := vs.store.Upsert(chunks); err != nil {
			vs.logger.Error("Failed to upsert chunks", zap.Error(err))
			return result, err
		}
	}

	return result, nil
}

// ClearIndex clears all chunks from the store
func (vs *VectorService) ClearIndex() error {
	// For SQLite, we can truncate the table
	// For now, this is a no-op as we handle updates via upsert
	return nil
}

// RemoveDocument removes all chunks for a document path
func (vs *VectorService) RemoveDocument(path string) error {
	return vs.store.DeleteByDocPath(path)
}

// Count returns the number of chunks in the store
func (vs *VectorService) Count() int {
	return vs.store.Count()
}

// Helper functions

func (ds *DocumentService) extractTitle(content, filename string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return strings.TrimSuffix(filename, filepath.Ext(filename))
}

func (ds *DocumentService) removeFrontmatter(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) > 1 && strings.TrimSpace(lines[0]) == "---" {
		for i, line := range lines[1:] {
			if strings.TrimSpace(line) == "---" {
				return strings.Join(lines[i+2:], "\n")
			}
		}
	}
	return content
}

func (ds *DocumentService) splitIntoChunks(content string) []string {
	chunkSize := ds.config.ChunkSize
	overlap := ds.config.ChunkOverlap

	if len(content) <= chunkSize {
		return []string{content}
	}

	var chunks []string
	contentLen := len(content)
	step := chunkSize - overlap

	for start := 0; start < contentLen; start += step {
		end := start + chunkSize
		if end > contentLen {
			end = contentLen
		}

		// Try to break at word boundary (look back up to 100 chars)
		if end < contentLen {
			breakPoint := end
			for i := end; i > end-100 && i > start; i-- {
				if content[i] == ' ' || content[i] == '\n' {
					breakPoint = i
					break
				}
			}
			end = breakPoint
		}

		chunk := strings.TrimSpace(content[start:end])
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}

		// Adjust next start to account for actual end position
		if end >= contentLen {
			break
		}
	}

	return chunks
}

func (ds *DocumentService) extractCategory(path string) string {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) > 0 {
		category := strings.TrimSuffix(parts[0], filepath.Ext(parts[0]))
		return strings.ToLower(category)
	}
	return "general"
}

func (ds *DocumentService) extractTaskInfo(path string) (string, string) {
	parts := strings.Split(path, string(filepath.Separator))

	var taskSlug string
	for _, part := range parts {
		if strings.Contains(part, "_") && !strings.Contains(part, ".") {
			taskSlug = strings.Split(part, "_")[0]
			break
		}
	}

	filename := filepath.Base(path)
	var phase string
	switch {
	case strings.Contains(filename, "requirements"):
		phase = "requirements"
	case strings.Contains(filename, "design"):
		phase = "design"
	case strings.Contains(filename, "tasks"):
		phase = "tasks"
	}

	return taskSlug, phase
}

func (ds *DocumentService) getFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Now()
	}
	return info.ModTime()
}

// calculateFileHash calculates MD5 hash of content (helper method for DocumentService)
func (ds *DocumentService) calculateFileHash(content string) string {
	hasher := md5.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}

// DefaultEmbeddingDimension is the default vector dimension for embedding providers.
// Jina v3 and bge-m3 produce 1024 natively; OpenAI supports Matryoshka truncation to any size.
// Can be overridden via MCP_EMBEDDING_DIMENSION env var. Changing requires re-indexing.
const DefaultEmbeddingDimension = 1024

// Embedder provides text embedding operations with HF API primary, Ollama fallback
type Embedder struct {
	config            EmbedderConfig
	logger            *zap.Logger
	client            *http.Client
	dimension         int       // Embedding dimension
	jinaDisabled      bool      // Flag to disable Jina after auth errors
	jinaDisabledUntil time.Time // Time when Jina can be retried again (for auth errors)
	jinaErrorCount    int       // Count of consecutive Jina errors
	jinaDisabledMu    sync.Mutex // Mutex for jinaDisabled flag
}

// NewEmbedder creates a new embedder with Jina AI API primary, Ollama fallback
func NewEmbedder(config EmbedderConfig, logger *zap.Logger) (*Embedder, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if config.OllamaBaseURL == "" {
		config.OllamaBaseURL = "http://localhost:11434"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 2
	}
	if config.Dimension == 0 {
		config.Dimension = DefaultEmbeddingDimension
	}

	return &Embedder{
		config:            config,
		logger:            logger,
		client:            &http.Client{
			Timeout: config.Timeout,
		},
		dimension:         config.Dimension,
		jinaDisabled:      false,
		jinaDisabledUntil: time.Time{}, // Zero time means not disabled
		jinaErrorCount:    0,
		jinaDisabledMu:    sync.Mutex{}, // Initialize mutex
	}, nil
}

// Embed generates embeddings for the given text (for documents)
func (e *Embedder) Embed(text string) ([]float32, error) {
	return e.EmbedWithTask(text, "retrieval.passage")
}

// EmbedQuery generates embeddings for a search query (optimized for retrieval)
func (e *Embedder) EmbedQuery(text string) ([]float32, error) {
	return e.EmbedWithTask(text, "retrieval.query")
}

// EmbedWithTask generates embeddings with specific task type
func (e *Embedder) EmbedWithTask(text string, task string) ([]float32, error) {
	// Safety check: ensure logger is not nil
	if e == nil {
		return nil, fmt.Errorf("embedder is nil")
	}
	if e.logger == nil {
		e.logger = zap.NewNop()
	}

	// Check if Jina is disabled (after auth errors) - quick check without lock
	e.jinaDisabledMu.Lock()
	jinaDisabled := e.jinaDisabled
	disabledUntil := e.jinaDisabledUntil
	e.jinaDisabledMu.Unlock()

	// Check if disabled period has expired (retry after 1 hour for auth errors)
	if jinaDisabled && !disabledUntil.IsZero() && time.Now().After(disabledUntil) {
		e.jinaDisabledMu.Lock()
		e.jinaDisabled = false
		e.jinaDisabledUntil = time.Time{}
		e.jinaErrorCount = 0
		e.jinaDisabledMu.Unlock()
		if e.logger != nil {
			e.logger.Info("Retrying Jina AI after timeout period",
				zap.String("task", task),
				zap.String("hint", "If API key was fixed, Jina will work now"),
			)
		}
		jinaDisabled = false
	}

	// tryProvider attempts an embedding call and validates dimensions.
	// Returns the embedding if successful and dimensions match, nil otherwise.
	tryProvider := func(name string, embed func() ([]float32, error)) []float32 {
		embedding, err := embed()
		if err != nil {
			e.logger.Warn("Embedding provider failed", zap.String("provider", name), zap.Error(err))
			return nil
		}
		if len(embedding) != e.dimension {
			e.logger.Error("Embedding dimension mismatch — check model configuration",
				zap.String("provider", name),
				zap.Int("got", len(embedding)),
				zap.Int("expected", e.dimension),
				zap.String("hint", fmt.Sprintf("The model returned %d dimensions but %d are required. Set MCP_EMBEDDING_DIMENSION=%d or use a model that supports %d-dimensional output.", len(embedding), e.dimension, len(embedding), e.dimension)),
			)
			return nil
		}
		return embedding
	}

	// 1. Try Jina AI first (preferred — high quality, multilingual) if not disabled
	if !jinaDisabled && e.config.JinaToken != "" {
		embedding := tryProvider("jina", func() ([]float32, error) {
			return e.embedJinaWithTask(text, task)
		})
		if embedding != nil {
			e.jinaDisabledMu.Lock()
			e.jinaErrorCount = 0
			e.jinaDisabledMu.Unlock()
			return embedding, nil
		}

		// Handle Jina-specific auth errors
		e.jinaDisabledMu.Lock()
		e.jinaErrorCount++
		errorCount := e.jinaErrorCount
		// Auto-disable Jina after repeated failures
		if errorCount >= 3 && !e.jinaDisabled {
			e.jinaDisabled = true
			e.jinaDisabledUntil = time.Now().Add(1 * time.Hour)
			e.jinaDisabledMu.Unlock()
			e.logger.Error("Jina AI disabled after repeated failures, using fallback providers",
				zap.String("hint", "Check JINA_API_KEY or remove it to skip Jina"),
			)
		} else {
			e.jinaDisabledMu.Unlock()
		}
	}

	// 2. Try OpenAI-compatible API (OpenAI, Together, Mistral, etc.)
	if e.config.OpenAIToken != "" {
		embedding := tryProvider("openai", func() ([]float32, error) {
			return e.embedOpenAI(text)
		})
		if embedding != nil {
			return embedding, nil
		}
	}

	// 3. Fallback to Ollama (local, free)
	if e.config.OllamaBaseURL != "" {
		// Try bge-m3 first
		embedding := tryProvider("ollama/bge-m3", func() ([]float32, error) {
			return e.embedOllamaModel(text, "bge-m3:latest")
		})
		if embedding != nil {
			return embedding, nil
		}

		// Try mxbai-embed-large as secondary
		embedding = tryProvider("ollama/mxbai-embed-large", func() ([]float32, error) {
			return e.embedOllamaModel(text, "mxbai-embed-large:latest")
		})
		if embedding != nil {
			return embedding, nil
		}
	}

	return nil, fmt.Errorf("all embedding providers failed: configure at least one of JINA_API_KEY, OPENAI_API_KEY, or OLLAMA_BASE_URL")
}

// BatchEmbed generates embeddings for multiple texts
func (e *Embedder) BatchEmbed(texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))

	for i, text := range texts {
		embedding, err := e.Embed(text)
		if err != nil {
			return nil, fmt.Errorf("failed to embed text at index %d: %w", i, err)
		}
		embeddings[i] = embedding
	}

	return embeddings, nil
}

// embedOllamaModel generates embeddings using specified Ollama model
func (e *Embedder) embedOllamaModel(text, model string) ([]float32, error) {
	url := e.config.OllamaBaseURL + "/api/embeddings"

	payload := map[string]interface{}{
		"model":  model,
		"prompt": text,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	resp, err := e.client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("Ollama %s request failed: %w", model, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Ollama %s returned status %d: %s", model, resp.StatusCode, string(body))
	}

	var ollamaResp struct {
		Embedding []float64 `json:"embedding"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to decode Ollama %s response: %w", model, err)
	}

	// Convert to float32
	embedding := make([]float32, len(ollamaResp.Embedding))
	for i, v := range ollamaResp.Embedding {
		embedding[i] = float32(v)
	}

	return embedding, nil
}

// embedOpenAI generates embeddings using OpenAI-compatible API
// Works with OpenAI, Together AI, Mistral, Azure OpenAI, and any /v1/embeddings endpoint
func (e *Embedder) embedOpenAI(text string) ([]float32, error) {
	model := e.config.OpenAIModel
	if model == "" {
		model = "text-embedding-3-small"
	}
	baseURL := e.config.OpenAIBaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	url := strings.TrimRight(baseURL, "/") + "/embeddings"

	payload := map[string]interface{}{
		"input":      text,
		"model":      model,
		"dimensions": e.dimension,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+e.config.OpenAIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI API returned status %d: %s", resp.StatusCode, string(body))
	}

	var openaiResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return nil, fmt.Errorf("failed to decode OpenAI response: %w", err)
	}

	if len(openaiResp.Data) == 0 {
		return nil, fmt.Errorf("OpenAI returned no embeddings")
	}

	embedding := make([]float32, len(openaiResp.Data[0].Embedding))
	for i, v := range openaiResp.Data[0].Embedding {
		embedding[i] = float32(v)
	}

	return embedding, nil
}

// embedJinaWithTask generates embeddings using Jina AI API with task type
// Task types: "retrieval.passage" for documents, "retrieval.query" for queries
func (e *Embedder) embedJinaWithTask(text string, task string) ([]float32, error) {
	url := "https://api.jina.ai/v1/embeddings"

	payload := map[string]interface{}{
		"input":           []string{text},
		"model":           "jina-embeddings-v3",
		"encoding_format": "float",
		"dimensions":      e.dimension,
		"task":            task, // "retrieval.passage" or "retrieval.query"
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+e.config.JinaToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Jina AI API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Jina AI API returned status %d: %s", resp.StatusCode, string(body))
	}

	var jinaResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&jinaResp); err != nil {
		return nil, fmt.Errorf("failed to decode Jina AI response: %w", err)
	}

	if len(jinaResp.Data) == 0 {
		return nil, fmt.Errorf("Jina AI returned no embeddings")
	}

	// Convert to float32
	embedding := make([]float32, len(jinaResp.Data[0].Embedding))
	for i, v := range jinaResp.Data[0].Embedding {
		embedding[i] = float32(v)
	}

	return embedding, nil
}
