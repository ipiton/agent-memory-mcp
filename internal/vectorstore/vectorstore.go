// Package vectorstore provides SQLite-backed vector storage with cosine similarity search.
package vectorstore

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	RefreshIfStale() (bool, error)
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

// SQLiteStore implements Store using SQLite, FTS5 for keywords and in-memory
// cosine similarity for vectors. Only the vectors live in memory, and only once
// a vector search has asked for them: a process that answers keyword queries, or
// runs without a model, never pays for them (T163).
type SQLiteStore struct {
	db     *sql.DB
	logger *zap.Logger
	mu     sync.RWMutex
	// chunks holds vectors and identity for semantic search, without Content:
	// the text is read from disk for the chunks a search returns.
	chunks        map[string]*Chunk
	vectorsLoaded bool
	// invalidUTF8Chunks counts chunks cut mid-rune by the pre-T118 splitter,
	// found by the one-time sweep. Reported so the next such share is found by a
	// number rather than by accident.
	invalidUTF8Chunks int
	// loadedIndexVersion is the index version this process last caught up with,
	// so it can tell whether someone else has re-indexed since.
	loadedIndexVersion string
	// refreshMu serializes loading the vectors and the check-and-reload in
	// RefreshIfStale so parallel searches do not each rebuild the same map.
	refreshMu sync.Mutex
	// ftsReady closes once the keyword index is usable; ftsErr is set before that
	// if it could not be built.
	ftsReady chan struct{}
	ftsErr   error
	// backfill tracks the background build so Close does not pull the database
	// out from under it; stop tells the build to give way.
	backfill sync.WaitGroup
	stop     chan struct{}
	stopOnce sync.Once
}

// indexVersionKey is the metadata key the indexer stamps in the same
// transaction that marks the index ready, so it never names a half-written
// index.
const indexVersionKey = "last_indexed"

// indexGenerationKey is a counter bumped in every transaction that publishes a
// change to the chunks: the final index commit and an orphan sweep that
// removed something. last_indexed alone has one-second resolution, so two runs
// finishing within the same second left the stamp unchanged and a reader that
// reloaded between them missed the second. The counter does not depend on the
// clock; last_indexed stays part of the version so a store indexed by a build
// that predates the counter is still noticed.
const indexGenerationKey = "index_generation"

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
		db:       db,
		logger:   logger,
		chunks:   make(map[string]*Chunk),
		ftsReady: make(chan struct{}),
		stop:     make(chan struct{}),
	}

	if err := store.validateDimension(dimension); err != nil {
		_ = db.Close()
		return nil, err
	}
	store.loadedIndexVersion = store.readIndexVersion()

	keywordIndexReady, err := store.prepareKeywordIndex()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to prepare keyword index: %w", err)
	}

	// T118: documents whose chunks were cut mid-rune are queued for re-chunking
	// before the orphan sweep, so one index pass fixes both.
	if marked, invalid, err := markInvalidUTF8DocumentsForReindexOnce(db); err != nil {
		logger.Warn("Failed to queue invalid-UTF-8 documents for re-index", zap.Error(err))
	} else if marked > 0 {
		store.invalidUTF8Chunks = invalid
		logger.Info("Queued documents for re-chunking after the T118 rune fix",
			zap.Int("documents", marked),
			zap.Int("invalid_chunks", invalid),
		)
	}

	if orphans, err := store.CleanOrphans(); err != nil {
		logger.Warn("Failed to clean orphan chunks at startup", zap.Error(err))
	} else if orphans > 0 {
		logger.Info("Startup orphan cleanup", zap.Int("removed", orphans))
	}

	// The first build after an upgrade takes tens of seconds on a large index.
	// It runs behind the open so the MCP handshake is not held up by it; writes
	// wait on ftsReady, searches report ErrKeywordIndexBuilding.
	if keywordIndexReady {
		close(store.ftsReady)
	} else {
		store.backfill.Add(1)
		go store.runKeywordBackfill()
	}

	logger.Info("Vector store initialized",
		zap.Int("chunks", store.Count()),
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
		// A keyword-only index holds no vectors, so its recorded dimension binds
		// nothing: a model configured later must not be refused because of it.
		var hasVectors bool
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM chunks WHERE length(embedding) > 0)`).Scan(&hasVectors); err != nil {
			return fmt.Errorf("failed to check stored vectors: %w", err)
		}
		if !hasVectors {
			return s.SetMetadata("embedding_dimension", strconv.Itoa(dimension))
		}
		return fmt.Errorf(
			"embedding dimension mismatch: index was built with %d dimensions but MCP_EMBEDDING_DIMENSION=%d. "+
				"Either set MCP_EMBEDDING_DIMENSION=%d to match existing data, "+
				"or delete the index and re-run index_documents to rebuild with %d dimensions",
			storedDim, dimension, storedDim, dimension,
		)
	}

	return nil
}

// loadVectors reads the vectors into memory. Chunks without a vector (a
// keyword-only index) stay out of the map and out of semantic search.
func (s *SQLiteStore) loadVectors() error {
	// Read the version before the rows. If an indexing run commits in between,
	// the stamp kept here is the older one and the next search reloads once
	// more; the reverse order would record a version newer than the data and
	// the update would be missed for good.
	version := s.readIndexVersion()

	rows, err := s.db.Query(`
		SELECT id, doc_path, title, last_modified, embedding
		FROM chunks
		WHERE length(embedding) > 0
	`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	loaded := make(map[string]*Chunk)
	for rows.Next() {
		var chunk Chunk
		var embeddingBlob []byte
		var title sql.NullString
		var lastModified sql.NullTime

		if err := rows.Scan(&chunk.ID, &chunk.DocPath, &title, &lastModified, &embeddingBlob); err != nil {
			s.logger.Warn("Failed to scan chunk", zap.Error(err))
			continue
		}
		chunk.Title = sanitizeUTF8(title.String)
		if lastModified.Valid {
			chunk.LastModified = lastModified.Time
		}

		embedding, err := dbutil.DecodeEmbedding(embeddingBlob)
		if err != nil {
			s.logger.Warn("Failed to decode embedding", zap.String("id", chunk.ID), zap.Error(err))
			continue
		}
		chunk.Embedding = embedding
		loaded[chunk.ID] = &chunk
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = loaded
	s.vectorsLoaded = true
	s.loadedIndexVersion = version
	return nil
}

// ensureVectors loads the vectors on the first semantic search.
func (s *SQLiteStore) ensureVectors() error {
	s.mu.RLock()
	loaded := s.vectorsLoaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.mu.RLock()
	loaded = s.vectorsLoaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}
	began := time.Now()
	if err := s.loadVectors(); err != nil {
		return fmt.Errorf("failed to load vectors: %w", err)
	}
	s.logger.Info("Vectors loaded", zap.Duration("took", time.Since(began)))
	return nil
}

// readIndexVersion returns the version stamped by the last completed indexing
// run, or "" when the index has never been stamped or the row cannot be read.
// An unreadable version deliberately reads as "changed" rather than
// "unchanged": a lost stamp must not be able to pin a process to a stale copy.
func (s *SQLiteStore) readIndexVersion() string {
	return readIndexVersionFrom(s.db)
}

// readIndexVersionFrom reads the version through db or through an open
// transaction, so a writer can see the version on both sides of its own change.
func readIndexVersionFrom(q interface {
	QueryRow(query string, args ...any) *sql.Row
}) string {
	var generation, stamp sql.NullString
	err := q.QueryRow(`
		SELECT
			(SELECT value FROM index_metadata WHERE key = ?),
			(SELECT value FROM index_metadata WHERE key = ?)
	`, indexGenerationKey, indexVersionKey).Scan(&generation, &stamp)
	if err != nil {
		return ""
	}
	return generation.String + "|" + stamp.String
}

// bumpIndexGeneration advances the generation inside tx and returns the
// resulting version. The caller reads the version before at the start of the
// same transaction, ahead of its own writes (which may include last_indexed).
func bumpIndexGeneration(tx *sql.Tx) (string, error) {
	if _, err := tx.Exec(`
		INSERT INTO index_metadata (key, value) VALUES (?, '1')
		ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1
	`, indexGenerationKey); err != nil {
		return "", fmt.Errorf("failed to bump index generation: %w", err)
	}
	return readIndexVersionFrom(tx), nil
}

// adoptIndexVersionLocked records this process's own published change as
// loaded, sparing it a full reload of maps it has already updated in place.
// Only when it was current just before its change: if another process
// published in between, the versions differ and RefreshIfStale reloads.
// The caller holds s.mu.
func (s *SQLiteStore) adoptIndexVersionLocked(before, after string) {
	if before != "" && s.loadedIndexVersion == before {
		s.loadedIndexVersion = after
	}
}

// RefreshIfStale catches up when another process has re-indexed since this one
// last looked, and reports whether it did. Keyword search reads the disk and is
// always current; what needs reloading are the vectors, if they are in memory.
//
// Semantic search answers from a map, but the database is written by a different
// process — the indexing run the git hook starts after a
// commit. Without this check a long-lived process (an MCP session, the resident
// HTTP instance) keeps answering from the snapshot it started with: the commit
// is in vectors.db, absent from the results, and nothing says so. Signals could
// not close that gap because they only reach the processes someone remembered
// to address.
//
// The cost when nothing changed is one primary-key lookup of a single metadata
// row; the reload runs only when the version actually moved.
func (s *SQLiteStore) RefreshIfStale() (bool, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	current := s.readIndexVersion()

	s.mu.RLock()
	loaded := s.loadedIndexVersion
	vectorsLoaded := s.vectorsLoaded
	s.mu.RUnlock()

	if current == loaded {
		return false, nil
	}

	if !vectorsLoaded {
		s.mu.Lock()
		s.loadedIndexVersion = current
		s.mu.Unlock()
		return true, nil
	}

	if err := s.loadVectors(); err != nil {
		return false, fmt.Errorf("failed to reload vectors after index change: %w", err)
	}

	s.logger.Info("Reloaded vectors after external re-indexing",
		zap.String("previous_version", loaded),
		zap.String("current_version", current),
		zap.Int("chunks", s.Count()),
	)

	return true, nil
}

// sanitizeUTF8 replaces invalid byte sequences with U+FFFD, leaving valid input
// untouched.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// InvalidUTF8Chunks reports how many chunks the one-time sweep found with invalid
// UTF-8 (T118). Non-zero means the index predates the rune-aware splitter and
// those documents are queued for re-chunking; results are sanitized when read.
func (s *SQLiteStore) InvalidUTF8Chunks() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.invalidUTF8Chunks
}

// Upsert inserts or replaces chunks in the store within a single transaction.
func (s *SQLiteStore) Upsert(chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if err := s.waitKeywordIndex(); err != nil {
		return err
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
		// REPLACE gives the row a new rowid, so the old keyword row has to go
		// first, while it can still be found.
		if err := deleteKeywordRowByChunkID(tx, chunk.ID); err != nil {
			return fmt.Errorf("failed to replace keyword row of chunk %s: %w", chunk.ID, err)
		}

		embeddingBlob := dbutil.EncodeEmbedding(chunk.Embedding)
		if embeddingBlob == nil {
			embeddingBlob = []byte{} // a keyword-only chunk: empty, but NOT NULL holds
		}
		res, err := stmt.Exec(
			chunk.ID, chunk.DocPath, chunk.Content, chunk.Title,
			chunk.LastModified, embeddingBlob,
		)
		if err != nil {
			return fmt.Errorf("failed to upsert chunk %s: %w", chunk.ID, err)
		}
		rowid, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("failed to read rowid of chunk %s: %w", chunk.ID, err)
		}
		if err := insertKeywordRow(tx, rowid, chunk.Title, chunk.DocPath, chunk.Content); err != nil {
			return fmt.Errorf("failed to index keywords of chunk %s: %w", chunk.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit upsert transaction: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.vectorsLoaded {
		return nil
	}
	for _, chunk := range chunks {
		if len(chunk.Embedding) == 0 {
			delete(s.chunks, chunk.ID)
			continue
		}
		chunkCopy := chunk
		chunkCopy.Content = ""
		s.chunks[chunk.ID] = &chunkCopy
	}

	return nil
}

// DeleteByDocPath removes all chunks associated with the given document path.
func (s *SQLiteStore) DeleteByDocPath(docPath string) error {
	if err := s.waitKeywordIndex(); err != nil {
		return err
	}

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

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM `+ftsTable+` WHERE rowid IN (SELECT rowid FROM chunks WHERE doc_path = ?)`, docPath); err != nil {
		return fmt.Errorf("failed to delete keyword rows: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM chunks WHERE doc_path = ?", docPath); err != nil {
		return fmt.Errorf("failed to delete chunks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit delete: %w", err)
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
	if err := s.ensureVectors(); err != nil {
		return nil, err
	}
	results := s.rankVectors(queryEmbedding, limit)
	if err := s.loadContent(results); err != nil {
		return nil, fmt.Errorf("failed to read chunk text: %w", err)
	}
	return results, nil
}

func (s *SQLiteStore) rankVectors(queryEmbedding []float32, limit int) []SearchResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.chunks) == 0 {
		return []SearchResult{}
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

	return results
}

// loadContent fills in the text of the chunks a search is returning.
func (s *SQLiteStore) loadContent(results []SearchResult) error {
	const batch = 500
	byID := make(map[string]*SearchResult, len(results))
	args := make([]any, len(results))
	for i := range results {
		byID[results[i].ID] = &results[i]
		args[i] = results[i].ID
	}
	for start := 0; start < len(args); start += batch {
		end := min(start+batch, len(args))
		rows, err := s.db.Query(`SELECT id, content FROM chunks WHERE id IN (?`+strings.Repeat(",?", end-start-1)+`)`, args[start:end]...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, content string
			if err := rows.Scan(&id, &content); err != nil {
				_ = rows.Close()
				return err
			}
			byID[id].Content = sanitizeUTF8(content)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// AllChunks returns every indexed chunk, read from disk.
func (s *SQLiteStore) AllChunks() ([]Chunk, error) {
	return s.queryChunks(`SELECT id, doc_path, content, title, last_modified, embedding FROM chunks`)
}

// ChunksByDocPath returns every chunk currently indexed for the given
// document path. Order matches chunk ID ascending so callers can rely on
// document order — chunk IDs are emitted as "<docPath>-<seq>" by the rag
// package, and seq is monotonic in document order. Embeddings are included
// for completeness but most callers that just want to reassemble the source
// text can ignore them.
func (s *SQLiteStore) ChunksByDocPath(docPath string) ([]Chunk, error) {
	chunks, err := s.queryChunks(`SELECT id, doc_path, content, title, last_modified, embedding FROM chunks WHERE doc_path = ?`, docPath)
	if err != nil {
		return nil, err
	}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].ID < chunks[j].ID
	})
	return chunks, nil
}

func (s *SQLiteStore) queryChunks(query string, args ...any) ([]Chunk, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var chunks []Chunk
	for rows.Next() {
		var chunk Chunk
		var title sql.NullString
		var lastModified sql.NullTime
		var embeddingBlob []byte
		if err := rows.Scan(&chunk.ID, &chunk.DocPath, &chunk.Content, &title, &lastModified, &embeddingBlob); err != nil {
			return nil, err
		}
		chunk.Content = sanitizeUTF8(chunk.Content)
		chunk.Title = sanitizeUTF8(title.String)
		if lastModified.Valid {
			chunk.LastModified = lastModified.Time
		}
		if len(embeddingBlob) > 0 {
			embedding, err := dbutil.DecodeEmbedding(embeddingBlob)
			if err != nil {
				s.logger.Warn("Failed to decode embedding", zap.String("id", chunk.ID), zap.Error(err))
			} else {
				chunk.Embedding = embedding
			}
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

// Count returns the number of indexed chunks.
func (s *SQLiteStore) Count() int {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
		s.logger.Warn("Failed to count chunks", zap.Error(err))
		return 0
	}
	return count
}

// Close closes the underlying SQLite database connection.
func (s *SQLiteStore) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	s.backfill.Wait()
	return s.db.Close()
}

// CosineSimilarity is a thin alias kept for backwards compatibility.
// The implementation now lives in internal/scoring; new callers should
// import scoring.CosineSimilarity directly.
func CosineSimilarity(a, b []float32) float64 {
	return scoring.CosineSimilarity(a, b)
}
