// Package vectorstore provides SQLite-backed vector storage with cosine similarity search.
package vectorstore

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/dbutil"
	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/topk"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

// Store defines the interface for vector storage backends.
type Store interface {
	Upsert(chunks []Chunk) error
	DeleteByDocPath(docPath string) error
	Search(queryEmbedding []float32, limit int) ([]SearchResult, error)
	KeywordSearch(query string, limit int) ([]SearchResult, error)
	AllChunks() ([]Chunk, error)
	ChunksByDocPath(docPath string) ([]Chunk, error)
	Count() int
	Close() error
	// Metadata
	GetMetadata(key string) (string, error)
	SetMetadata(key, value string) error
	CommitIndexState(update IndexStateUpdate) error
	// Indexed files tracking
	GetAllIndexedFiles() (map[string]*IndexedFileInfo, error)
	GetIndexedFile(filePath string) (*IndexedFileInfo, error)
	SetIndexedFile(info *IndexedFileInfo) error
	DeleteIndexedFile(filePath string) error
	// Orphan cleanup
	CleanOrphans() (int, error)
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

// IndexStateUpdate applies metadata and indexed file tracking changes in one transaction.
type IndexStateUpdate struct {
	Metadata        map[string]string
	UpsertFiles     []*IndexedFileInfo
	DeleteFilePaths []string
}

// SQLiteStore implements Store using SQLite with in-memory cosine similarity search.
type SQLiteStore struct {
	db                 *sql.DB
	logger             *zap.Logger
	mu                 sync.RWMutex
	chunks             map[string]*Chunk
	keywordDocs        map[string]keywordDocStats
	keywordPostings    map[string]map[string]int
	totalKeywordTokens int
}

// NewSQLiteStore creates a new SQLite-backed vector store with the given embedding dimension.
func NewSQLiteStore(dbPath string, dimension int, logger *zap.Logger) (*SQLiteStore, error) {
	db, err := dbutil.OpenSQLite(dbPath, logger)
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
		_ = db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	store := &SQLiteStore{
		db:              db,
		logger:          logger,
		chunks:          make(map[string]*Chunk),
		keywordDocs:     make(map[string]keywordDocStats),
		keywordPostings: make(map[string]map[string]int),
	}

	if err := store.validateDimension(dimension); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := store.loadChunksToMemory(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to load chunks: %w", err)
	}

	if orphans, err := store.CleanOrphans(); err != nil {
		logger.Warn("Failed to clean orphan chunks at startup", zap.Error(err))
	} else if orphans > 0 {
		logger.Info("Startup orphan cleanup", zap.Int("removed", orphans))
	}

	logger.Info("Vector store initialized",
		zap.Int("chunks_loaded", len(store.chunks)),
		zap.Int("dimension", dimension),
	)

	return store, nil
}

func (s *SQLiteStore) validateDimension(dimension int) error {
	stored, err := s.GetMetadata("embedding_dimension")
	if errors.Is(err, ErrMetadataNotFound) {
		return s.SetMetadata("embedding_dimension", strconv.Itoa(dimension))
	}
	if err != nil {
		return fmt.Errorf("failed to read embedding dimension: %w", err)
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
	defer func() { _ = rows.Close() }()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.chunks = make(map[string]*Chunk)
	s.keywordDocs = make(map[string]keywordDocStats)
	s.keywordPostings = make(map[string]map[string]int)
	s.totalKeywordTokens = 0

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

		embedding, err := decodeEmbedding(embeddingBlob)
		if err != nil {
			s.logger.Warn("Failed to decode embedding", zap.String("id", chunk.ID), zap.Error(err))
			continue
		}
		chunk.Embedding = embedding

		s.chunks[chunk.ID] = &chunk
		s.indexChunkKeywordsLocked(&chunk)
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
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO chunks
		(id, doc_path, content, title, last_modified, embedding)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, chunk := range chunks {
		embeddingBlob := encodeEmbedding(chunk.Embedding)

		_, err = stmt.Exec(
			chunk.ID, chunk.DocPath, chunk.Content, chunk.Title,
			chunk.LastModified, embeddingBlob,
		)
		if err != nil {
			return fmt.Errorf("failed to upsert chunk %s: %w", chunk.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit upsert transaction: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, chunk := range chunks {
		if _, exists := s.chunks[chunk.ID]; exists {
			s.removeChunkKeywordsLocked(chunk.ID)
		}

		chunkCopy := chunk
		s.chunks[chunk.ID] = &chunkCopy
		s.indexChunkKeywordsLocked(&chunkCopy)
	}

	return nil
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
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close chunk rows: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	_, err = s.db.Exec("DELETE FROM chunks WHERE doc_path = ?", docPath)
	if err != nil {
		return fmt.Errorf("failed to delete chunks: %w", err)
	}

	s.mu.Lock()
	for _, id := range ids {
		s.removeChunkKeywordsLocked(id)
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
	defer s.mu.RUnlock()

	if len(s.chunks) == 0 {
		return []SearchResult{}, nil
	}

	const minScore = 0.1

	useHeap := limit > 0
	var topResults *topk.MinHeap[SearchResult]
	if useHeap {
		topResults = topk.NewMinHeap(limit, func(a, b SearchResult) bool {
			return a.Score < b.Score
		})
	}
	results := make([]SearchResult, 0)
	for _, chunk := range s.chunks {
		score := CosineSimilarity(queryEmbedding, chunk.Embedding)
		if score < minScore {
			continue
		}

		result := SearchResult{
			Chunk: *chunk,
			Score: score,
		}
		if !useHeap {
			results = append(results, result)
			continue
		}
		if topResults.Len() < limit {
			topResults.PushItem(result)
			continue
		}
		if topResults.PeekMin().Score < result.Score {
			topResults.ReplaceMin(result)
		}
	}

	if useHeap {
		results = make([]SearchResult, 0, topResults.Len())
		for topResults.Len() > 0 {
			results = append(results, topResults.PopItem())
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, nil
}

// AllChunks returns a snapshot copy of all indexed chunks currently loaded in memory.
func (s *SQLiteStore) AllChunks() ([]Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	chunks := make([]Chunk, 0, len(s.chunks))
	for _, chunk := range s.chunks {
		chunkCopy := *chunk
		if chunk.Embedding != nil {
			chunkCopy.Embedding = append([]float32(nil), chunk.Embedding...)
		}
		chunks = append(chunks, chunkCopy)
	}

	return chunks, nil
}

// ChunksByDocPath returns every chunk currently indexed for the given
// document path. Order matches chunk ID ascending so callers can rely on
// document order — chunk IDs are emitted as "<docPath>-<seq>" by the rag
// package, and seq is monotonic in document order. Embeddings are included
// for completeness but most callers that just want to reassemble the source
// text can ignore them.
func (s *SQLiteStore) ChunksByDocPath(docPath string) ([]Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var chunks []Chunk
	for _, chunk := range s.chunks {
		if chunk.DocPath != docPath {
			continue
		}
		chunkCopy := *chunk
		if chunk.Embedding != nil {
			chunkCopy.Embedding = append([]float32(nil), chunk.Embedding...)
		}
		chunks = append(chunks, chunkCopy)
	}

	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ID < chunks[j].ID
	})
	return chunks, nil
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

// encodeEmbedding serializes a float32 slice to a compact binary blob (little-endian).
func encodeEmbedding(embedding []float32) []byte {
	buf := make([]byte, len(embedding)*4)
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// decodeEmbedding deserializes an embedding blob. Tries binary first, falls back to JSON
// for backwards compatibility with older indexes.
func decodeEmbedding(blob []byte) ([]float32, error) {
	if len(blob) > 0 && len(blob)%4 == 0 && blob[0] != '[' {
		n := len(blob) / 4
		result := make([]float32, n)
		for i := range n {
			result[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
		}
		return result, nil
	}
	var result []float32
	if err := json.Unmarshal(blob, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CosineSimilarity is a thin alias kept for backwards compatibility.
// The implementation now lives in internal/scoring; new callers should
// import scoring.CosineSimilarity directly.
func CosineSimilarity(a, b []float32) float64 {
	return scoring.CosineSimilarity(a, b)
}
