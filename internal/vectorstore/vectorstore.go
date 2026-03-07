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
	"strings"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/topk"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

// ErrMetadataNotFound is returned by GetMetadata when the key does not exist.
var ErrMetadataNotFound = errors.New("metadata key not found")

// Store defines the interface for vector storage backends.
type Store interface {
	Upsert(chunks []Chunk) error
	DeleteByDocPath(docPath string) error
	Search(queryEmbedding []float32, limit int) ([]SearchResult, error)
	KeywordSearch(query string, limit int) ([]SearchResult, error)
	AllChunks() ([]Chunk, error)
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

type keywordDocStats struct {
	termFreq     map[string]int
	tokenCount   int
	titleLower   string
	pathLower    string
	contentLower string
}

type keywordQueryStats struct {
	totalDocs      int
	avgChunkLength float64
	docFreq        map[string]int
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

// KeywordSearch finds keyword-relevant chunks using a precomputed in-memory inverted index.
func (s *SQLiteStore) KeywordSearch(query string, limit int) ([]SearchResult, error) {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	queryTerms := uniqueKeywordTerms(tokenizeKeywordText(queryLower))
	if len(queryTerms) == 0 {
		return []SearchResult{}, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.keywordDocs) == 0 {
		return []SearchResult{}, nil
	}

	candidateIDs := make(map[string]struct{}, len(queryTerms)*4)
	docFreq := make(map[string]int, len(queryTerms))
	for _, term := range queryTerms {
		postings := s.keywordPostings[term]
		docFreq[term] = len(postings)
		for chunkID := range postings {
			candidateIDs[chunkID] = struct{}{}
		}
	}

	if len(candidateIDs) == 0 {
		return []SearchResult{}, nil
	}

	avgChunkLength := 1.0
	if len(s.keywordDocs) > 0 && s.totalKeywordTokens > 0 {
		avgChunkLength = float64(s.totalKeywordTokens) / float64(len(s.keywordDocs))
		if avgChunkLength == 0 {
			avgChunkLength = 1
		}
	}
	queryStats := keywordQueryStats{
		totalDocs:      len(s.keywordDocs),
		avgChunkLength: avgChunkLength,
		docFreq:        docFreq,
	}

	results := make([]SearchResult, 0, len(candidateIDs))
	for chunkID := range candidateIDs {
		chunk := s.chunks[chunkID]
		if chunk == nil {
			continue
		}
		docStats, ok := s.keywordDocs[chunkID]
		if !ok {
			continue
		}

		score := keywordScore(queryLower, queryTerms, queryStats, docStats)
		if score <= 0 {
			continue
		}

		results = append(results, SearchResult{
			Chunk: *chunk,
			Score: score,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].LastModified.After(results[j].LastModified)
		}
		return results[i].Score > results[j].Score
	})

	if len(results) > limit {
		results = results[:limit]
	}

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
// Returns ErrMetadataNotFound if the key does not exist.
func (s *SQLiteStore) GetMetadata(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM index_metadata WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", ErrMetadataNotFound
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

// CommitIndexState applies metadata and indexed file changes atomically.
func (s *SQLiteStore) CommitIndexState(update IndexStateUpdate) error {
	for _, info := range update.UpsertFiles {
		if info == nil {
			return fmt.Errorf("indexed file update contains nil entry")
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin index state transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for key, value := range update.Metadata {
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO index_metadata (key, value) VALUES (?, ?)
		`, key, value); err != nil {
			return fmt.Errorf("failed to persist metadata %q: %w", key, err)
		}
	}

	for _, filePath := range update.DeleteFilePaths {
		if _, err := tx.Exec("DELETE FROM indexed_files WHERE file_path = ?", filePath); err != nil {
			return fmt.Errorf("failed to delete indexed file metadata for %q: %w", filePath, err)
		}
	}

	for _, info := range update.UpsertFiles {
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO indexed_files (file_path, hash, mod_time, size, chunk_count)
			VALUES (?, ?, ?, ?, ?)
		`, info.FilePath, info.Hash, info.ModTime, info.Size, info.ChunkCount); err != nil {
			return fmt.Errorf("failed to upsert indexed file metadata for %q: %w", info.FilePath, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit index state transaction: %w", err)
	}

	return nil
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
	defer func() { _ = rows.Close() }()

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

func tokenizeKeywordText(value string) []string {
	fields := scoring.TokenizeWords(value)

	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) < 2 || field == "k" {
			continue
		}
		tokens = append(tokens, field)
	}
	return tokens
}

func uniqueKeywordTerms(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
}

func buildKeywordDocStats(chunk *Chunk) keywordDocStats {
	tokens := tokenizeKeywordText(chunk.Title + " " + chunk.DocPath + " " + chunk.Content)
	stats := keywordDocStats{
		termFreq:     make(map[string]int, len(tokens)),
		tokenCount:   len(tokens),
		titleLower:   strings.ToLower(chunk.Title),
		pathLower:    strings.ToLower(chunk.DocPath),
		contentLower: strings.ToLower(chunk.Content),
	}
	for _, token := range tokens {
		stats.termFreq[token]++
	}
	return stats
}

func (s *SQLiteStore) indexChunkKeywordsLocked(chunk *Chunk) {
	stats := buildKeywordDocStats(chunk)
	s.keywordDocs[chunk.ID] = stats
	s.totalKeywordTokens += stats.tokenCount

	for term, tf := range stats.termFreq {
		postings := s.keywordPostings[term]
		if postings == nil {
			postings = make(map[string]int)
			s.keywordPostings[term] = postings
		}
		postings[chunk.ID] = tf
	}
}

func (s *SQLiteStore) removeChunkKeywordsLocked(chunkID string) {
	stats, ok := s.keywordDocs[chunkID]
	if !ok {
		return
	}

	delete(s.keywordDocs, chunkID)
	s.totalKeywordTokens -= stats.tokenCount
	if s.totalKeywordTokens < 0 {
		s.totalKeywordTokens = 0
	}

	for term := range stats.termFreq {
		postings := s.keywordPostings[term]
		if postings == nil {
			continue
		}
		delete(postings, chunkID)
		if len(postings) == 0 {
			delete(s.keywordPostings, term)
		}
	}
}

func keywordScore(queryLower string, queryTerms []string, queryStats keywordQueryStats, stats keywordDocStats) float64 {
	if len(queryTerms) == 0 || queryStats.totalDocs == 0 || stats.tokenCount == 0 {
		return 0
	}

	titleLower := stats.titleLower
	pathLower := stats.pathLower
	contentLower := stats.contentLower

	const (
		k1 = 1.2
		b  = 0.75
	)

	score := 0.0
	chunkLength := float64(stats.tokenCount)
	for _, term := range queryTerms {
		tf := stats.termFreq[term]
		if tf == 0 {
			continue
		}

		df := queryStats.docFreq[term]
		if df == 0 {
			continue
		}

		idf := math.Log(1 + (float64(queryStats.totalDocs)-float64(df)+0.5)/(float64(df)+0.5))
		denom := float64(tf) + k1*(1-b+b*(chunkLength/queryStats.avgChunkLength))
		score += idf * ((float64(tf) * (k1 + 1)) / denom)
	}

	if score == 0 && !scoring.ContainsAny(titleLower, queryTerms...) && !scoring.ContainsAny(pathLower, queryTerms...) && !scoring.ContainsAny(contentLower, queryTerms...) {
		return 0
	}

	switch {
	case strings.Contains(titleLower, queryLower):
		score += 1.4
	case allTermsPresent(titleLower, queryTerms):
		score += 0.8
	}

	switch {
	case strings.Contains(pathLower, queryLower):
		score += 1.0
	case strings.Contains(pathLower, strings.ReplaceAll(queryLower, " ", "-")):
		score += 0.9
	case allTermsPresent(pathLower, queryTerms):
		score += 0.6
	}

	if strings.Contains(contentLower, queryLower) {
		score += 0.5
	}

	return score
}

func allTermsPresent(value string, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if !strings.Contains(value, term) {
			return false
		}
	}
	return true
}
