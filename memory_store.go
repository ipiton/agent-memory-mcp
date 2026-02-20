package main

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
	"go.uber.org/zap"
	_ "modernc.org/sqlite" // SQLite driver
)

// MemoryType represents different types of memories
type MemoryType string

const (
		MemoryTypeEpisodic   MemoryType = "episodic"   // Events and actions
	MemoryTypeSemantic   MemoryType = "semantic"   // Facts and knowledge
	MemoryTypeProcedural MemoryType = "procedural" // Patterns and skills
	MemoryTypeWorking    MemoryType = "working"    // Short-term working memory
)

// Memory represents a stored memory entry
type Memory struct {
	ID          string            `json:"id"`
	Content     string            `json:"content"`
	Type        MemoryType        `json:"type"`
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

// MemoryStore provides persistent memory storage with vector search
type MemoryStore struct {
	db       *sql.DB
	logger   *zap.Logger
	embedder *Embedder
	mu       sync.RWMutex
	// In-memory cache for fast search
	memories map[string]*Memory
}

// NewMemoryStore creates a new memory store
func NewMemoryStore(dbPath string, embedder *Embedder, logger *zap.Logger) (*MemoryStore, error) {
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

	store := &MemoryStore{
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
func (ms *MemoryStore) loadMemoriesToCache() error {
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

// Store saves a new memory
func (ms *MemoryStore) Store(m *Memory) error {
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
	tagsJSON, _ := json.Marshal(m.Tags)
	metadataJSON, _ := json.Marshal(m.Metadata)
	embeddingJSON, _ := json.Marshal(m.Embedding)

	// Insert into database
	_, err := ms.db.Exec(`
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

// Recall searches memories by semantic similarity
func (ms *MemoryStore) Recall(query string, filters MemoryFilters, limit int) ([]*MemorySearchResult, error) {
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

	var results []*MemorySearchResult

	for _, m := range ms.memories {
		// Apply filters
		if !ms.matchFilters(m, filters) {
			continue
		}

		var score float64
		if len(queryEmbedding) > 0 && len(m.Embedding) > 0 {
			// Vector similarity search
			score = cosineSimilarity(queryEmbedding, m.Embedding)
		} else {
			// Fallback to text matching score
			score = ms.textMatchScore(query, m)
		}

		// Apply importance weight
		weightedScore := score * (0.5 + m.Importance*0.5)

		results = append(results, &MemorySearchResult{
			Memory: m,
			Score:  weightedScore,
		})
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Limit results
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	// Collect IDs for access stats update (safe copy, not pointers to map values)
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.Memory.ID
	}

	// Update access stats for top results (async with IDs, not memory pointers)
	go ms.updateAccessStats(ids)

	return results, nil
}

// MemoryFilters for filtering memories during recall
type MemoryFilters struct {
	Type          MemoryType `json:"type,omitempty"`
	Context       string     `json:"context,omitempty"`
	Tags          []string   `json:"tags,omitempty"`
	MinImportance float64    `json:"min_importance,omitempty"`
	Since         time.Time  `json:"since,omitempty"`
}

// MemorySearchResult wraps a memory with its search score
type MemorySearchResult struct {
	Memory *Memory `json:"memory"`
	Score  float64 `json:"score"`
}

// matchFilters checks if a memory matches the filters
func (ms *MemoryStore) matchFilters(m *Memory, filters MemoryFilters) bool {
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
func (ms *MemoryStore) textMatchScore(query string, m *Memory) float64 {
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
func (ms *MemoryStore) updateAccessStats(ids []string) {
	if len(ids) == 0 {
		return
	}

	now := time.Now()
	for _, id := range ids {
		// Update database first (doesn't need lock)
		ms.db.Exec(`
			UPDATE memories SET accessed_at = ?, access_count = access_count + 1
			WHERE id = ?
		`, now, id)

		// Update cache with lock
		ms.mu.Lock()
		if m, exists := ms.memories[id]; exists {
			m.AccessedAt = now
			m.AccessCount++
		}
		ms.mu.Unlock()
	}
}

// Update modifies an existing memory
func (ms *MemoryStore) Update(id string, updates MemoryUpdate) error {
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
	tagsJSON, _ := json.Marshal(m.Tags)
	metadataJSON, _ := json.Marshal(m.Metadata)
	embeddingJSON, _ := json.Marshal(m.Embedding)

	// Update database
	_, err := ms.db.Exec(`
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

// MemoryUpdate contains fields that can be updated
type MemoryUpdate struct {
	Content    string            `json:"content,omitempty"`
	Title      string            `json:"title,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Context    string            `json:"context,omitempty"`
	Importance *float64          `json:"importance,omitempty"` // Pointer to distinguish nil (not set) from 0.0
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Delete removes a memory by ID
func (ms *MemoryStore) Delete(id string) error {
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

// Get retrieves a memory by ID
func (ms *MemoryStore) Get(id string) (*Memory, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	m, exists := ms.memories[id]
	if !exists {
		return nil, fmt.Errorf("memory not found: %s", id)
	}

	return m, nil
}

// List returns all memories with optional filtering
func (ms *MemoryStore) List(filters MemoryFilters, limit int) ([]*Memory, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	var results []*Memory

	for _, m := range ms.memories {
		if ms.matchFilters(m, filters) {
			results = append(results, m)
		}
	}

	// Sort by updated_at descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// Count returns the total number of memories
func (ms *MemoryStore) Count() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.memories)
}

// CountByType returns memory counts grouped by type
func (ms *MemoryStore) CountByType() map[MemoryType]int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	counts := make(map[MemoryType]int)
	for _, m := range ms.memories {
		counts[m.Type]++
	}
	return counts
}

// Close closes the memory store
func (ms *MemoryStore) Close() error {
	return ms.db.Close()
}

