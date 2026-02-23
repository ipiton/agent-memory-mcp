// Package vectorstore provides SQLite-backed vector storage with cosine similarity search.
package vectorstore

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

// Store defines the interface for vector storage backends.
type Store interface {
	Upsert(chunks []Chunk) error
	DeleteByDocPath(docPath string) error
	Search(queryEmbedding []float32, limit int) ([]SearchResult, error)
	Count() int
	Close() error
	// Metadata
	GetMetadata(key string) (string, error)
	SetMetadata(key, value string) error
	// Indexed files tracking
	GetAllIndexedFiles() (map[string]*IndexedFileInfo, error)
	GetIndexedFile(filePath string) (*IndexedFileInfo, error)
	SetIndexedFile(info *IndexedFileInfo) error
	DeleteIndexedFile(filePath string) error
}

// Chunk represents a document chunk with its embedding vector and metadata.
type Chunk struct {
	ID           string    `json:"id"`
	DocPath      string    `json:"doc_path"`
	Content      string    `json:"content"`
	Title        string    `json:"title"`
	LastModified time.Time `json:"last_modified"`
	Embedding    []float32 `json:"embedding"`
}

// SearchResult represents a search result with its cosine similarity score.
type SearchResult struct {
	Chunk
	Score float64 `json:"score"`
}

// IndexedFileInfo represents metadata about an indexed file for change detection.
type IndexedFileInfo struct {
	FilePath   string
	Hash       string
	ModTime    time.Time
	Size       int64
	ChunkCount int
}

// SQLiteStore implements Store using SQLite with in-memory cosine similarity search.
type SQLiteStore struct {
	db     *sql.DB
	logger *zap.Logger
	mu     sync.RWMutex
	chunks map[string]*Chunk
}

// NewSQLiteStore creates a new SQLite-backed vector store with the given embedding dimension.
func NewSQLiteStore(dbPath string, dimension int, logger *zap.Logger) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY,
		doc_path TEXT NOT NULL,
		content TEXT NOT NULL,
		title TEXT,
		last_modified DATETIME,
		embedding BLOB NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_chunks_doc_path ON chunks(doc_path);

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

	store := &SQLiteStore{
		db:     db,
		logger: logger,
		chunks: make(map[string]*Chunk),
	}

	if err := store.validateDimension(dimension); err != nil {
		db.Close()
		return nil, err
	}

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

func (s *SQLiteStore) validateDimension(dimension int) error {
	stored, err := s.GetMetadata("embedding_dimension")
	if err != nil {
		return s.SetMetadata("embedding_dimension", strconv.Itoa(dimension))
	}

	storedDim, err := strconv.Atoi(stored)
	if err != nil {
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

func (s *SQLiteStore) loadChunksToMemory() error {
	rows, err := s.db.Query(`
		SELECT id, doc_path, content, title, last_modified, embedding
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
			&lastModified, &embeddingBlob,
		)
		if err != nil {
			s.logger.Warn("Failed to scan chunk", zap.Error(err))
			continue
		}

		if lastModified.Valid {
			chunk.LastModified = lastModified.Time
		}

		if err := json.Unmarshal(embeddingBlob, &chunk.Embedding); err != nil {
			s.logger.Warn("Failed to unmarshal embedding", zap.String("id", chunk.ID), zap.Error(err))
			continue
		}

		s.chunks[chunk.ID] = &chunk
	}

	return rows.Err()
}

// Upsert inserts or replaces chunks in the store within a single transaction.
func (s *SQLiteStore) Upsert(chunks []Chunk) error {
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
		(id, doc_path, content, title, last_modified, embedding)
		VALUES (?, ?, ?, ?, ?, ?)
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
			chunk.LastModified, embeddingBlob,
		)
		if err != nil {
			s.logger.Warn("Failed to upsert chunk", zap.String("id", chunk.ID), zap.Error(err))
			continue
		}

		chunkCopy := chunk
		s.chunks[chunk.ID] = &chunkCopy
	}

	return tx.Commit()
}

// DeleteByDocPath removes all chunks associated with the given document path.
func (s *SQLiteStore) DeleteByDocPath(docPath string) error {
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
		return nil
	}

	_, err = s.db.Exec("DELETE FROM chunks WHERE doc_path = ?", docPath)
	if err != nil {
		return fmt.Errorf("failed to delete chunks: %w", err)
	}

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

// Search finds the most similar chunks to the query embedding, ranked by cosine similarity.
func (s *SQLiteStore) Search(queryEmbedding []float32, limit int) ([]SearchResult, error) {
	s.mu.RLock()
	chunksEmpty := len(s.chunks) == 0
	s.mu.RUnlock()

	if chunksEmpty {
		if err := s.loadChunksToMemory(); err != nil {
			return nil, fmt.Errorf("failed to reload chunks: %w", err)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.chunks) == 0 {
		return []SearchResult{}, nil
	}

	const minScore = 0.1

	var results []SearchResult
	for _, chunk := range s.chunks {
		score := CosineSimilarity(queryEmbedding, chunk.Embedding)
		if score < minScore {
			continue
		}

		results = append(results, SearchResult{
			Chunk: *chunk,
			Score: score,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// Count returns the number of chunks currently loaded in memory.
func (s *SQLiteStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks)
}

// Close closes the underlying SQLite database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// === Metadata Methods ===

// GetMetadata retrieves a metadata value by key from the index_metadata table.
func (s *SQLiteStore) GetMetadata(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM index_metadata WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetMetadata stores a key-value pair in the index_metadata table.
func (s *SQLiteStore) SetMetadata(key, value string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO index_metadata (key, value) VALUES (?, ?)
	`, key, value)
	return err
}

// === Indexed Files Methods ===

// GetIndexedFile retrieves indexing metadata for a file, or nil if not indexed.
func (s *SQLiteStore) GetIndexedFile(filePath string) (*IndexedFileInfo, error) {
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

// SetIndexedFile stores or updates indexing metadata for a file.
func (s *SQLiteStore) SetIndexedFile(info *IndexedFileInfo) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO indexed_files (file_path, hash, mod_time, size, chunk_count)
		VALUES (?, ?, ?, ?, ?)
	`, info.FilePath, info.Hash, info.ModTime, info.Size, info.ChunkCount)
	return err
}

// DeleteIndexedFile removes indexing metadata for a file.
func (s *SQLiteStore) DeleteIndexedFile(filePath string) error {
	_, err := s.db.Exec("DELETE FROM indexed_files WHERE file_path = ?", filePath)
	return err
}

// GetAllIndexedFiles returns indexing metadata for all tracked files.
func (s *SQLiteStore) GetAllIndexedFiles() (map[string]*IndexedFileInfo, error) {
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

// CosineSimilarity calculates the cosine similarity between two vectors.
func CosineSimilarity(a, b []float32) float64 {
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
