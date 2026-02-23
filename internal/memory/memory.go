// Package memory provides persistent agent memory storage with semantic vector search.
package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
	_ "modernc.org/sqlite" // SQLite driver
)

// Type represents the category of a memory entry.
type Type string

const (
	TypeEpisodic   Type = "episodic"   // Events and actions
	TypeSemantic   Type = "semantic"   // Facts and knowledge
	TypeProcedural Type = "procedural" // Patterns and skills
	TypeWorking    Type = "working"    // Short-term working memory
)

// Memory represents a stored memory entry with content, metadata, and embedding.
type Memory struct {
	ID          string            `json:"id"`
	Content     string            `json:"content"`
	Type        Type              `json:"type"`
	Title       string            `json:"title,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Context     string            `json:"context,omitempty"` // Task slug, session, etc.
	Importance  float64           `json:"importance"`        // 0.0 - 1.0
	Metadata    map[string]string `json:"metadata,omitempty"`
	Embedding   []float32         `json:"-"` // Vector embedding
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	AccessedAt  time.Time         `json:"accessed_at"`  // Last retrieval time
	AccessCount int               `json:"access_count"` // How many times retrieved
}

// Store provides persistent memory storage backed by SQLite with in-memory vector search.
type Store struct {
	db       *sql.DB
	logger   *zap.Logger
	embedder *embedder.Embedder
	mu       sync.RWMutex
	// In-memory cache for fast search
	memories map[string]*Memory
}

// NewStore creates a new Store backed by a SQLite database at dbPath.
func NewStore(dbPath string, embedder *embedder.Embedder, logger *zap.Logger) (*Store, error) {
	// Create nop logger if not provided
	if logger == nil {
		config := zap.NewProductionConfig()
		config.Level = zap.NewAtomicLevelAt(zap.FatalLevel)
		config.OutputPaths = []string{"/dev/null"}
		logger, _ = config.Build()
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open memory database: %w", err)
	}

	// Create schema for memories
	schema := `
	CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY,
		content TEXT NOT NULL,
		type TEXT NOT NULL,
		title TEXT,
		tags TEXT,
		context TEXT,
		importance REAL DEFAULT 0.5,
		metadata TEXT,
		embedding BLOB,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		accessed_at DATETIME NOT NULL,
		access_count INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(type);
	CREATE INDEX IF NOT EXISTS idx_memories_context ON memories(context);
	CREATE INDEX IF NOT EXISTS idx_memories_importance ON memories(importance);
	CREATE INDEX IF NOT EXISTS idx_memories_created_at ON memories(created_at);
	`

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create memory schema: %w", err)
	}

	store := &Store{
		db:       db,
		logger:   logger,
		embedder: embedder,
		memories: make(map[string]*Memory),
	}

	// Load memories into cache
	if err := store.loadMemoriesToCache(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to load memories: %w", err)
	}

	return store, nil
}

// loadMemoriesToCache loads all memories from SQLite into memory cache
func (ms *Store) loadMemoriesToCache() error {
	rows, err := ms.db.Query(`
		SELECT id, content, type, title, tags, context, importance, metadata,
		       embedding, created_at, updated_at, accessed_at, access_count
		FROM memories
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.memories = make(map[string]*Memory)

	for rows.Next() {
		var m Memory
		var tagsJSON, metadataJSON sql.NullString
		var embeddingBlob []byte
		var createdAt, updatedAt, accessedAt sql.NullTime

		err := rows.Scan(
			&m.ID, &m.Content, &m.Type, &m.Title, &tagsJSON, &m.Context,
			&m.Importance, &metadataJSON, &embeddingBlob,
			&createdAt, &updatedAt, &accessedAt, &m.AccessCount,
		)
		if err != nil {
			ms.logger.Warn("Failed to scan memory", zap.Error(err))
			continue
		}

		// Parse tags
		if tagsJSON.Valid && tagsJSON.String != "" {
			if err := json.Unmarshal([]byte(tagsJSON.String), &m.Tags); err != nil {
				ms.logger.Warn("Failed to unmarshal tags", zap.String("id", m.ID), zap.Error(err))
			}
		}

		// Parse metadata
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &m.Metadata); err != nil {
				ms.logger.Warn("Failed to unmarshal metadata", zap.String("id", m.ID), zap.Error(err))
			}
		}

		// Parse embedding
		if len(embeddingBlob) > 0 {
			if err := json.Unmarshal(embeddingBlob, &m.Embedding); err != nil {
				ms.logger.Warn("Failed to unmarshal embedding", zap.String("id", m.ID), zap.Error(err))
			}
		}

		// Parse timestamps
		if createdAt.Valid {
			m.CreatedAt = createdAt.Time
		}
		if updatedAt.Valid {
			m.UpdatedAt = updatedAt.Time
		}
		if accessedAt.Valid {
			m.AccessedAt = accessedAt.Time
		}

		// Store explicit heap copy for clarity (var m inside loop is already per-iteration,
		// but this makes the heap allocation explicit and consistent with Store())
		memoryCopy := new(Memory)
		*memoryCopy = m
		ms.memories[m.ID] = memoryCopy
	}

	return rows.Err()
}

// Store saves a new memory, generating an ID and embedding if not provided.
func (ms *Store) Store(m *Memory) error {
	// Generate ID if not provided
	if m.ID == "" {
		m.ID = uuid.New().String()
	}

	// Set timestamps
	now := time.Now()
	m.CreatedAt = now
	m.UpdatedAt = now
	m.AccessedAt = now

	// Set default importance if not specified
	if m.Importance <= 0 {
		m.Importance = 0.5
	}

	// Generate embedding for the content
	if ms.embedder != nil && len(m.Embedding) == 0 {
		embedding, err := ms.embedder.Embed(m.Content)
		if err != nil {
			ms.logger.Warn("Failed to generate embedding for memory", zap.String("id", m.ID), zap.Error(err))
			// Continue without embedding - memory can still be stored
		} else {
			m.Embedding = embedding
		}
	}

	// Serialize tags and metadata
	tagsJSON, err := json.Marshal(m.Tags)
	if err != nil {
		return fmt.Errorf("failed to marshal tags: %w", err)
	}
	metadataJSON, err := json.Marshal(m.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	embeddingJSON, err := json.Marshal(m.Embedding)
	if err != nil {
		return fmt.Errorf("failed to marshal embedding: %w", err)
	}

	// Insert into database
	_, err = ms.db.Exec(`
		INSERT INTO memories (id, content, type, title, tags, context, importance, metadata,
		                      embedding, created_at, updated_at, accessed_at, access_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ID, m.Content, m.Type, m.Title, string(tagsJSON), m.Context,
		m.Importance, string(metadataJSON), embeddingJSON,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount,
	)
	if err != nil {
		return fmt.Errorf("failed to store memory: %w", err)
	}

	// Update cache (explicit heap allocation for clarity)
	ms.mu.Lock()
	memoryCopy := new(Memory)
	*memoryCopy = *m
	ms.memories[m.ID] = memoryCopy
	ms.mu.Unlock()

	ms.logger.Info("Memory stored",
		zap.String("id", m.ID),
		zap.String("type", string(m.Type)),
		zap.String("title", m.Title))

	return nil
}

// Recall searches memories by semantic similarity, applying filters and importance weighting.
func (ms *Store) Recall(query string, filters Filters, limit int) ([]*SearchResult, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	if len(ms.memories) == 0 {
		return nil, nil
	}

	// Generate query embedding
	var queryEmbedding []float32
	if ms.embedder != nil {
		var err error
		queryEmbedding, err = ms.embedder.EmbedQuery(query)
		if err != nil {
			ms.logger.Warn("Failed to embed query, falling back to text search", zap.Error(err))
		}
	}

	const minScore = 0.05

	var results []*SearchResult

	for _, m := range ms.memories {
		if !ms.matchFilters(m, filters) {
			continue
		}

		var score float64
		if len(queryEmbedding) > 0 && len(m.Embedding) > 0 {
			score = vectorstore.CosineSimilarity(queryEmbedding, m.Embedding)
		} else {
			score = ms.textMatchScore(query, m)
		}

		weightedScore := score * (0.5 + m.Importance*0.5)
		if weightedScore < minScore {
			continue
		}

		results = append(results, &SearchResult{
			Memory: copyMemory(m),
			Score:  weightedScore,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.Memory.ID
	}

	go ms.updateAccessStats(ids)

	return results, nil
}

// Filters specifies optional constraints for memory recall and listing.
type Filters struct {
	Type          Type      `json:"type,omitempty"`
	Context       string    `json:"context,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	MinImportance float64   `json:"min_importance,omitempty"`
	Since         time.Time `json:"since,omitempty"`
}

// SearchResult wraps a Memory with its relevance score from a search query.
type SearchResult struct {
	Memory *Memory `json:"memory"`
	Score  float64 `json:"score"`
}

// matchFilters checks if a memory matches the filters
func (ms *Store) matchFilters(m *Memory, filters Filters) bool {
	// Type filter
	if filters.Type != "" && m.Type != filters.Type {
		return false
	}

	// Context filter
	if filters.Context != "" && m.Context != filters.Context {
		return false
	}

	// Importance filter
	if filters.MinImportance > 0 && m.Importance < filters.MinImportance {
		return false
	}

	// Time filter
	if !filters.Since.IsZero() && m.CreatedAt.Before(filters.Since) {
		return false
	}

	// Tags filter (match any)
	if len(filters.Tags) > 0 {
		hasTag := false
		for _, filterTag := range filters.Tags {
			for _, memTag := range m.Tags {
				if memTag == filterTag {
					hasTag = true
					break
				}
			}
			if hasTag {
				break
			}
		}
		if !hasTag {
			return false
		}
	}

	return true
}

// textMatchScore calculates a simple text matching score
func (ms *Store) textMatchScore(query string, m *Memory) float64 {
	queryLower := strings.ToLower(query)
	contentLower := strings.ToLower(m.Content)
	titleLower := strings.ToLower(m.Title)

	score := 0.0

	// Exact content match
	if strings.Contains(contentLower, queryLower) {
		score += 0.5
	}

	// Title match (higher weight)
	if strings.Contains(titleLower, queryLower) {
		score += 0.7
	}

	// Word-level matching
	splitWords := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
	}
	queryWords := splitWords(queryLower)
	contentWords := splitWords(contentLower)

	matchCount := 0
	for _, qw := range queryWords {
		for _, cw := range contentWords {
			if qw == cw {
				matchCount++
				break
			}
		}
	}

	if len(queryWords) > 0 {
		score += float64(matchCount) / float64(len(queryWords)) * 0.3
	}

	return score
}

// updateAccessStats updates access statistics for retrieved memories by ID
// Uses IDs instead of memory pointers to avoid race conditions with concurrent modifications
func (ms *Store) updateAccessStats(ids []string) {
	if len(ids) == 0 {
		return
	}

	now := time.Now()

	// Update cache with a single lock acquisition
	ms.mu.Lock()
	for _, id := range ids {
		if m, exists := ms.memories[id]; exists {
			m.AccessedAt = now
			m.AccessCount++
		}
	}
	ms.mu.Unlock()

	// Update database (best-effort, log errors)
	for _, id := range ids {
		if _, err := ms.db.Exec(`
			UPDATE memories SET accessed_at = ?, access_count = access_count + 1
			WHERE id = ?
		`, now, id); err != nil {
			ms.logger.Warn("Failed to update access stats", zap.String("id", id), zap.Error(err))
		}
	}
}

// Update modifies an existing memory identified by id with the provided field updates.
func (ms *Store) Update(id string, updates Update) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	m, exists := ms.memories[id]
	if !exists {
		return fmt.Errorf("memory not found: %s", id)
	}

	// Apply updates
	if updates.Content != "" {
		m.Content = updates.Content
		// Re-generate embedding
		if ms.embedder != nil {
			embedding, err := ms.embedder.Embed(m.Content)
			if err == nil {
				m.Embedding = embedding
			}
		}
	}
	if updates.Title != "" {
		m.Title = updates.Title
	}
	if len(updates.Tags) > 0 {
		m.Tags = updates.Tags
	}
	if updates.Context != "" {
		m.Context = updates.Context
	}
	if updates.Importance != nil {
		m.Importance = *updates.Importance
	}
	if len(updates.Metadata) > 0 {
		if m.Metadata == nil {
			m.Metadata = make(map[string]string)
		}
		for k, v := range updates.Metadata {
			m.Metadata[k] = v
		}
	}

	m.UpdatedAt = time.Now()

	// Serialize
	tagsJSON, err := json.Marshal(m.Tags)
	if err != nil {
		return fmt.Errorf("failed to marshal tags: %w", err)
	}
	metadataJSON, err := json.Marshal(m.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	embeddingJSON, err := json.Marshal(m.Embedding)
	if err != nil {
		return fmt.Errorf("failed to marshal embedding: %w", err)
	}

	// Update database
	_, err = ms.db.Exec(`
		UPDATE memories SET content = ?, title = ?, tags = ?, context = ?,
		                    importance = ?, metadata = ?, embedding = ?, updated_at = ?
		WHERE id = ?
	`,
		m.Content, m.Title, string(tagsJSON), m.Context,
		m.Importance, string(metadataJSON), embeddingJSON, m.UpdatedAt, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update memory: %w", err)
	}

	ms.logger.Info("Memory updated", zap.String("id", id))
	return nil
}

// Update contains optional fields for modifying an existing memory.
type Update struct {
	Content    string            `json:"content,omitempty"`
	Title      string            `json:"title,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Context    string            `json:"context,omitempty"`
	Importance *float64          `json:"importance,omitempty"` // Pointer to distinguish nil (not set) from 0.0
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Delete removes a memory by ID from both the database and cache.
func (ms *Store) Delete(id string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if _, exists := ms.memories[id]; !exists {
		return fmt.Errorf("memory not found: %s", id)
	}

	// Delete from database
	_, err := ms.db.Exec("DELETE FROM memories WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete memory: %w", err)
	}

	// Remove from cache
	delete(ms.memories, id)

	ms.logger.Info("Memory deleted", zap.String("id", id))
	return nil
}

// Get retrieves a memory by ID from the in-memory cache (returns a copy).
func (ms *Store) Get(id string) (*Memory, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	m, exists := ms.memories[id]
	if !exists {
		return nil, fmt.Errorf("memory not found: %s", id)
	}

	return copyMemory(m), nil
}

// List returns memories matching the given filters, sorted by update time descending.
func (ms *Store) List(filters Filters, limit int) ([]*Memory, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	var results []*Memory

	for _, m := range ms.memories {
		if ms.matchFilters(m, filters) {
			results = append(results, copyMemory(m))
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// ExportAll returns all memories sorted by CreatedAt ascending.
func (ms *Store) ExportAll() ([]*Memory, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	result := make([]*Memory, 0, len(ms.memories))
	for _, m := range ms.memories {
		result = append(result, copyMemory(m))
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result, nil
}

// Count returns the total number of stored memories.
func (ms *Store) Count() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.memories)
}

// CountByType returns the number of memories grouped by Type.
func (ms *Store) CountByType() map[Type]int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	counts := make(map[Type]int)
	for _, m := range ms.memories {
		counts[m.Type]++
	}
	return counts
}

// Close closes the underlying database connection.
func (ms *Store) Close() error {
	return ms.db.Close()
}

// copyMemory creates a deep copy of a Memory, including slices and maps.
func copyMemory(m *Memory) *Memory {
	c := *m
	if len(m.Tags) > 0 {
		c.Tags = make([]string, len(m.Tags))
		copy(c.Tags, m.Tags)
	}
	if len(m.Metadata) > 0 {
		c.Metadata = make(map[string]string, len(m.Metadata))
		for k, v := range m.Metadata {
			c.Metadata[k] = v
		}
	}
	if len(m.Embedding) > 0 {
		c.Embedding = make([]float32, len(m.Embedding))
		copy(c.Embedding, m.Embedding)
	}
	return &c
}
