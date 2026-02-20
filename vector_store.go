package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

// VectorStore is the interface for vector storage backends
type VectorStore interface {
	// Upsert adds or updates chunks in the store
	Upsert(chunks []Chunk) error

	// DeleteByDocPath removes all chunks for a given document path
	DeleteByDocPath(docPath string) error

	// Search performs vector similarity search with optional filters
	Search(queryEmbedding []float32, filters map[string]string, limit int) ([]SearchResult, error)

	// Count returns the number of chunks in the store
	Count() int

	// Close closes the store
	Close() error
}

// Chunk represents a document chunk with embedding
type Chunk struct {
	ID           string    `json:"id"`
	DocPath      string    `json:"doc_path"` // Original document path (for deletion)
	Content      string    `json:"content"`
	Title        string    `json:"title"`
	Path         string    `json:"path"` // Full path for display
	Type         string    `json:"type"`
	Category     string    `json:"category"`
	TaskSlug     string    `json:"task_slug"`
	TaskPhase    string    `json:"task_phase"`
	LastModified time.Time `json:"last_modified"`
	Embedding    []float32 `json:"embedding"`
}

// SearchResult represents a search result with score
type SearchResult struct {
	Chunk
	Score float64 `json:"score"`
}

// SQLiteVectorStore implements VectorStore using SQLite for storage
// and brute-force cosine similarity for search (no CGO extensions needed)
type SQLiteVectorStore struct {
	db     *sql.DB
	logger *zap.Logger
	mu     sync.RWMutex
	// In-memory cache for fast search
	chunks map[string]*Chunk
}

// NewSQLiteVectorStore creates a new SQLite-backed vector store.
// dimension is the expected embedding vector size (e.g. 1024).
// If the store already contains vectors with a different dimension, returns an error.
func NewSQLiteVectorStore(dbPath string, dimension int, logger *zap.Logger) (*SQLiteVectorStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	// Create schema
	schema := `
	CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY,
		doc_path TEXT NOT NULL,
		content TEXT NOT NULL,
		title TEXT,
		path TEXT,
		type TEXT,
		category TEXT,
		task_slug TEXT,
		task_phase TEXT,
		last_modified DATETIME,
		embedding BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_chunks_doc_path ON chunks(doc_path);
	CREATE INDEX IF NOT EXISTS idx_chunks_type ON chunks(type);
	CREATE INDEX IF NOT EXISTS idx_chunks_category ON chunks(category);

	CREATE TABLE IF NOT EXISTS index_metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS indexed_files (
		file_path TEXT PRIMARY KEY,
		hash TEXT NOT NULL,
		mod_time DATETIME,
		size INTEGER,
		chunk_count INTEGER DEFAULT 0
	);
	`

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	store := &SQLiteVectorStore{
		db:     db,
		logger: logger,
		chunks: make(map[string]*Chunk),
	}

	// Validate embedding dimension consistency
	if err := store.validateDimension(dimension); err != nil {
		db.Close()
		return nil, err
	}

	// Load chunks into memory for fast search
	if err := store.loadChunksToMemory(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to load chunks: %w", err)
	}

	logger.Info("Vector store initialized",
		zap.Int("chunks_loaded", len(store.chunks)),
		zap.Int("dimension", dimension),
	)

	return store, nil
}

// validateDimension checks that the configured dimension matches the stored one.
// On first use, saves the dimension. On subsequent uses, verifies consistency.
func (s *SQLiteVectorStore) validateDimension(dimension int) error {
	stored, err := s.GetMetadata("embedding_dimension")
	if err != nil {
		// No dimension stored yet — save it
		return s.SetMetadata("embedding_dimension", strconv.Itoa(dimension))
	}

	storedDim, err := strconv.Atoi(stored)
	if err != nil {
		// Corrupted value — overwrite
		return s.SetMetadata("embedding_dimension", strconv.Itoa(dimension))
	}

	if storedDim != dimension {
		return fmt.Errorf(
			"embedding dimension mismatch: index was built with %d dimensions but MCP_EMBEDDING_DIMENSION=%d. "+
				"Either set MCP_EMBEDDING_DIMENSION=%d to match existing data, "+
				"or delete the index and re-run index_documents to rebuild with %d dimensions",
			storedDim, dimension, storedDim, dimension,
		)
	}

	return nil
}

// loadChunksToMemory loads all chunks from SQLite into memory cache
func (s *SQLiteVectorStore) loadChunksToMemory() error {
	rows, err := s.db.Query(`
		SELECT id, doc_path, content, title, path, type, category,
		       task_slug, task_phase, last_modified, embedding
		FROM chunks
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.chunks = make(map[string]*Chunk)

	for rows.Next() {
		var chunk Chunk
		var embeddingBlob []byte
		var lastModified sql.NullTime

		err := rows.Scan(
			&chunk.ID, &chunk.DocPath, &chunk.Content, &chunk.Title,
			&chunk.Path, &chunk.Type, &chunk.Category,
			&chunk.TaskSlug, &chunk.TaskPhase, &lastModified, &embeddingBlob,
		)
		if err != nil {
			s.logger.Warn("Failed to scan chunk", zap.Error(err))
			continue
		}

		if lastModified.Valid {
			chunk.LastModified = lastModified.Time
		}

		// Decode embedding from JSON blob
		if err := json.Unmarshal(embeddingBlob, &chunk.Embedding); err != nil {
			s.logger.Warn("Failed to unmarshal embedding", zap.String("id", chunk.ID), zap.Error(err))
			continue
		}

		s.chunks[chunk.ID] = &chunk
	}

	return rows.Err()
}

// Upsert adds or updates chunks in the store
func (s *SQLiteVectorStore) Upsert(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO chunks
		(id, doc_path, content, title, path, type, category, task_slug, task_phase, last_modified, embedding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, chunk := range chunks {
		embeddingBlob, err := json.Marshal(chunk.Embedding)
		if err != nil {
			s.logger.Warn("Failed to marshal embedding", zap.String("id", chunk.ID), zap.Error(err))
			continue
		}

		_, err = stmt.Exec(
			chunk.ID, chunk.DocPath, chunk.Content, chunk.Title,
			chunk.Path, chunk.Type, chunk.Category,
			chunk.TaskSlug, chunk.TaskPhase, chunk.LastModified, embeddingBlob,
		)
		if err != nil {
			s.logger.Warn("Failed to upsert chunk", zap.String("id", chunk.ID), zap.Error(err))
			continue
		}

		// Update in-memory cache
		chunkCopy := chunk
		s.chunks[chunk.ID] = &chunkCopy
	}

	return tx.Commit()
}

// DeleteByDocPath removes all chunks for a given document path
func (s *SQLiteVectorStore) DeleteByDocPath(docPath string) error {
	// First, find all chunk IDs for this doc_path
	rows, err := s.db.Query("SELECT id FROM chunks WHERE doc_path = ?", docPath)
	if err != nil {
		return fmt.Errorf("failed to query chunks: %w", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	if len(ids) == 0 {
		return nil // Nothing to delete
	}

	// Delete from database
	_, err = s.db.Exec("DELETE FROM chunks WHERE doc_path = ?", docPath)
	if err != nil {
		return fmt.Errorf("failed to delete chunks: %w", err)
	}

	// Remove from memory cache
	s.mu.Lock()
	for _, id := range ids {
		delete(s.chunks, id)
	}
	s.mu.Unlock()

	s.logger.Info("Deleted chunks for document",
		zap.String("doc_path", docPath),
		zap.Int("count", len(ids)))

	return nil
}

// Search performs vector similarity search with optional filters
func (s *SQLiteVectorStore) Search(queryEmbedding []float32, filters map[string]string, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	chunksEmpty := len(s.chunks) == 0
	s.mu.RUnlock()

	// Try to reload if empty (outside of lock)
	if chunksEmpty {
		if err := s.loadChunksToMemory(); err != nil {
			return nil, fmt.Errorf("failed to reload chunks: %w", err)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.chunks) == 0 {
		// No documents indexed - return empty results, not error
		return []SearchResult{}, nil
	}

	// Calculate similarities and filter
	var results []SearchResult
	for _, chunk := range s.chunks {
		// Apply filters
		if !matchFilters(chunk, filters) {
			continue
		}

		// Calculate cosine similarity
		score := cosineSimilarity(queryEmbedding, chunk.Embedding)

		results = append(results, SearchResult{
			Chunk: *chunk,
			Score: score,
		})
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Limit results
	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// Count returns the number of chunks in the store
func (s *SQLiteVectorStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks)
}

// Close closes the store
func (s *SQLiteVectorStore) Close() error {
	return s.db.Close()
}

// === Metadata Methods ===

// GetMetadata retrieves a metadata value by key
func (s *SQLiteVectorStore) GetMetadata(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM index_metadata WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetMetadata sets a metadata value
func (s *SQLiteVectorStore) SetMetadata(key, value string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO index_metadata (key, value) VALUES (?, ?)
	`, key, value)
	return err
}

// IndexedFileInfo represents info about an indexed file
type IndexedFileInfo struct {
	FilePath   string
	Hash       string
	ModTime    time.Time
	Size       int64
	ChunkCount int
}

// GetIndexedFile retrieves info about an indexed file
func (s *SQLiteVectorStore) GetIndexedFile(filePath string) (*IndexedFileInfo, error) {
	var info IndexedFileInfo
	var modTime sql.NullTime
	err := s.db.QueryRow(`
		SELECT file_path, hash, mod_time, size, chunk_count
		FROM indexed_files WHERE file_path = ?
	`, filePath).Scan(&info.FilePath, &info.Hash, &modTime, &info.Size, &info.ChunkCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if modTime.Valid {
		info.ModTime = modTime.Time
	}
	return &info, nil
}

// SetIndexedFile saves info about an indexed file
func (s *SQLiteVectorStore) SetIndexedFile(info *IndexedFileInfo) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO indexed_files (file_path, hash, mod_time, size, chunk_count)
		VALUES (?, ?, ?, ?, ?)
	`, info.FilePath, info.Hash, info.ModTime, info.Size, info.ChunkCount)
	return err
}

// DeleteIndexedFile removes an indexed file record
func (s *SQLiteVectorStore) DeleteIndexedFile(filePath string) error {
	_, err := s.db.Exec("DELETE FROM indexed_files WHERE file_path = ?", filePath)
	return err
}

// GetAllIndexedFiles returns all indexed file paths
func (s *SQLiteVectorStore) GetAllIndexedFiles() (map[string]*IndexedFileInfo, error) {
	rows, err := s.db.Query("SELECT file_path, hash, mod_time, size, chunk_count FROM indexed_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*IndexedFileInfo)
	for rows.Next() {
		var info IndexedFileInfo
		var modTime sql.NullTime
		if err := rows.Scan(&info.FilePath, &info.Hash, &modTime, &info.Size, &info.ChunkCount); err != nil {
			continue
		}
		if modTime.Valid {
			info.ModTime = modTime.Time
		}
		result[info.FilePath] = &info
	}
	return result, rows.Err()
}

// matchFilters checks if a chunk matches all filters
func matchFilters(chunk *Chunk, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if value == "" {
			continue
		}

		var chunkValue string
		switch key {
		case "type":
			chunkValue = chunk.Type
		case "category":
			chunkValue = chunk.Category
		case "task_slug":
			chunkValue = chunk.TaskSlug
		case "task_phase":
			chunkValue = chunk.TaskPhase
		case "doc_type": // Alias for type
			chunkValue = chunk.Type
		default:
			continue
		}

		if chunkValue != value {
			return false
		}
	}

	return true
}

// cosineSimilarity calculates cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}
