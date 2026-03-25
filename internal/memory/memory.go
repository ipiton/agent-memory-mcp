// Package memory provides persistent agent memory storage with semantic vector search.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
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
	ID             string            `json:"id"`
	Content        string            `json:"content"`
	Type           Type              `json:"type"`
	Title          string            `json:"title,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Context        string            `json:"context,omitempty"` // Task slug, session, etc.
	Importance     float64           `json:"importance"`        // 0.0 - 1.0
	Metadata       map[string]string `json:"metadata,omitempty"`
	Embedding      []float32         `json:"-"` // Vector embedding
	EmbeddingModel string            `json:"embedding_model,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	AccessedAt     time.Time         `json:"accessed_at"`  // Last retrieval time
	AccessCount    int               `json:"access_count"` // How many times retrieved

	// Temporal fields — when this knowledge was valid and supersession chain.
	ValidFrom    *time.Time `json:"valid_from,omitempty"`    // when this knowledge became true
	ValidUntil   *time.Time `json:"valid_until,omitempty"`   // when this knowledge stopped being true
	SupersededBy string     `json:"superseded_by,omitempty"` // ID of the entry that replaced this one
	Replaces     string     `json:"replaces,omitempty"`      // ID of the entry this one replaced
	ObservedAt   *time.Time `json:"observed_at,omitempty"`   // when first observed (may differ from created_at)
}

// Validate ensures the memory entry is consistent and ready for storage.
func (m *Memory) Validate() error {
	if m == nil {
		return &ErrValidation{Message: "memory is required"}
	}

	content := strings.TrimSpace(m.Content)
	if content == "" {
		return &ErrValidation{Message: "content parameter is required"}
	}
	m.Content = content

	normalizedType, err := ValidateType(m.Type, TypeSemantic)
	if err != nil {
		return err
	}
	m.Type = normalizedType

	m.Title = strings.TrimSpace(m.Title)
	m.Context = strings.TrimSpace(m.Context)
	m.Tags = NormalizeTags(m.Tags)

	normalizedMetadata, err := normalizeEngineeringMetadata(m.Metadata, m.Tags, m.Type)
	if err != nil {
		return err
	}
	m.Metadata = normalizedMetadata
	m.Tags = normalizeEngineeringTags(m.Tags, m.Metadata)

	if m.Importance < 0 || m.Importance > 1 {
		return &ErrValidation{Message: "importance must be between 0.0 and 1.0"}
	}

	return nil
}

// cachedMemory is a RAM-efficient representation of a Memory entry for fast filtering and search.
// Content is NOT cached in RAM to save space.
type cachedMemory struct {
	ID             string
	Content        string
	Type           Type
	Title          string
	Tags           []string
	Context        string
	Lifecycle      LifecycleStatus
	KnowledgeLayer string
	Owner          string
	Importance     float64
	Embedding      []float32
	EmbeddingModel string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	AccessedAt     time.Time
	AccessCount    int
	ValidFrom      *time.Time
	ValidUntil     *time.Time
	SupersededBy   string
}

// Store provides persistent memory storage backed by SQLite with in-memory vector search.
//
// Lock ordering: writeMu MUST be acquired before mu. Never hold mu while acquiring writeMu.
// Write operations acquire writeMu first, then mu for cache updates.
// Read operations acquire only mu.RLock for snapshot access.
type Store struct {
	db       *sql.DB
	logger   *zap.Logger
	embedder *embedder.Embedder
	writeMu  sync.Mutex   // serializes write operations (Store, Update, Delete, Merge, Promote)
	mu       sync.RWMutex // protects in-memory cache (memories, contextIndex)
	accessCh chan []string // batched access stats updates
	accessWG sync.WaitGroup
	// In-memory cache for fast search (minimal fields)
	memories     map[string]*cachedMemory
	contextIndex map[string]map[string]*cachedMemory // context → id → *cachedMemory
	loadErrors   int                                 // count of unmarshal errors during cache load
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
		_ = db.Close()
		return nil, fmt.Errorf("failed to create memory schema: %w", err)
	}
	if err := ensureMemorySchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to migrate memory schema: %w", err)
	}

	store := &Store{
		db:           db,
		logger:       logger,
		embedder:     embedder,
		accessCh:     make(chan []string, 64),
		memories:     make(map[string]*cachedMemory),
		contextIndex: make(map[string]map[string]*cachedMemory),
	}

	// Load memories into cache — must succeed before starting background workers.
	if err := store.loadMemoriesToCache(); err != nil {
		close(store.accessCh)
		_ = db.Close()
		return nil, fmt.Errorf("failed to load memories: %w", err)
	}
	if errs := store.LoadErrors(); errs > 0 {
		logger.Warn("Some memories failed to load from database",
			zap.Int("load_errors", errs),
		)
	}

	store.accessWG.Add(1)
	go store.accessStatsWorker()

	store.maybeStartBackgroundReembed()

	return store, nil
}

func (ms *Store) loadMemoriesToCache() error {
	rows, err := ms.db.Query(`
		SELECT id, content, type, title, tags, context, importance, metadata,
		       embedding_model, embedding, created_at, updated_at, accessed_at, access_count,
		       valid_from, valid_until, superseded_by
		FROM memories
	`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.memories = make(map[string]*cachedMemory)
	ms.contextIndex = make(map[string]map[string]*cachedMemory)
	ms.loadErrors = 0

	for rows.Next() {
		var m cachedMemory
		var tagsJSON, metadataJSON, embeddingModel sql.NullString
		var embeddingBlob []byte
		var createdAt, updatedAt, accessedAt sql.NullTime
		var validFrom, validUntil sql.NullTime
		var supersededBy sql.NullString

		err := rows.Scan(
			&m.ID, &m.Content, &m.Type, &m.Title, &tagsJSON, &m.Context,
			&m.Importance, &metadataJSON, &embeddingModel, &embeddingBlob,
			&createdAt, &updatedAt, &accessedAt, &m.AccessCount,
			&validFrom, &validUntil, &supersededBy,
		)
		if err != nil {
			ms.logger.Warn("Failed to scan memory", zap.Error(err))
			ms.loadErrors++
			continue
		}

		// Parse tags
		if tagsJSON.Valid && tagsJSON.String != "" {
			if err := json.Unmarshal([]byte(tagsJSON.String), &m.Tags); err != nil {
				ms.logger.Warn("Failed to unmarshal tags", zap.String("id", m.ID), zap.Error(err))
				ms.loadErrors++
			}
		}

		// Derived metadata for trust scoring
		metadata := make(map[string]string)
		if metadataJSON.Valid && metadataJSON.String != "" {
			_ = json.Unmarshal([]byte(metadataJSON.String), &metadata)
		}
		m.Lifecycle = LifecycleStatusOf(&Memory{Type: m.Type, Metadata: metadata})
		m.KnowledgeLayer = strings.ToLower(strings.TrimSpace(metadata[MetadataKnowledgeLayer]))
		m.Owner = strings.TrimSpace(metadata[MetadataOwner])
		if m.KnowledgeLayer == "" && m.Lifecycle == LifecycleCanonical {
			m.KnowledgeLayer = "canonical"
		}
		if m.KnowledgeLayer == "" {
			m.KnowledgeLayer = "raw"
		}
		if m.Owner == "" {
			m.Owner = defaultOwnerForMemorySource(cachedMemoryEntity(&m))
		}

		// Parse embedding (binary format)
		if len(embeddingBlob) > 0 {
			parsed, err := unmarshalEmbeddingBinary(embeddingBlob)
			if err != nil {
				ms.logger.Warn("Failed to unmarshal embedding", zap.String("id", m.ID), zap.Error(err))
				ms.loadErrors++
			} else {
				m.Embedding = parsed
			}
		}
		if embeddingModel.Valid {
			m.EmbeddingModel = embeddingModel.String
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
		if validFrom.Valid {
			m.ValidFrom = &validFrom.Time
		}
		if validUntil.Valid {
			m.ValidUntil = &validUntil.Time
		}
		if supersededBy.Valid {
			m.SupersededBy = supersededBy.String
		}

		ms.cacheSetLocked(&m)
	}

	return rows.Err()
}

// cacheSetLocked adds or updates a memory in the cache and context index.
// Caller MUST hold ms.mu for writing.
func (ms *Store) cacheSetLocked(m *cachedMemory) {
	if old, exists := ms.memories[m.ID]; exists && old.Context != m.Context {
		if idx, ok := ms.contextIndex[old.Context]; ok {
			delete(idx, m.ID)
			if len(idx) == 0 {
				delete(ms.contextIndex, old.Context)
			}
		}
	}
	ms.memories[m.ID] = m
	if m.Context != "" {
		if ms.contextIndex[m.Context] == nil {
			ms.contextIndex[m.Context] = make(map[string]*cachedMemory)
		}
		ms.contextIndex[m.Context][m.ID] = m
	}
}

func toCachedMemory(m *Memory) *cachedMemory {
	cm := &cachedMemory{
		ID:             m.ID,
		Content:        m.Content,
		Type:           m.Type,
		Title:          m.Title,
		Tags:           m.Tags,
		Context:        m.Context,
		Importance:     m.Importance,
		Embedding:      m.Embedding,
		EmbeddingModel: m.EmbeddingModel,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
		AccessedAt:     m.AccessedAt,
		AccessCount:    m.AccessCount,
		ValidFrom:      m.ValidFrom,
		ValidUntil:     m.ValidUntil,
		SupersededBy:   m.SupersededBy,
	}
	cm.Lifecycle = LifecycleStatusOf(m)
	cm.KnowledgeLayer = strings.ToLower(strings.TrimSpace(m.Metadata[MetadataKnowledgeLayer]))
	cm.Owner = strings.TrimSpace(m.Metadata[MetadataOwner])
	if cm.KnowledgeLayer == "" && cm.Lifecycle == LifecycleCanonical {
		cm.KnowledgeLayer = "canonical"
	}
	if cm.KnowledgeLayer == "" {
		cm.KnowledgeLayer = "raw"
	}
	if cm.Owner == "" {
		cm.Owner = defaultOwnerForMemorySource(cachedMemoryEntity(cm))
	}
	return cm
}

// cacheDeleteLocked removes a memory from the cache and context index.
// Caller MUST hold ms.mu for writing.
func (ms *Store) cacheDeleteLocked(id string) {
	if m, exists := ms.memories[id]; exists {
		if idx, ok := ms.contextIndex[m.Context]; ok {
			delete(idx, m.ID)
			if len(idx) == 0 {
				delete(ms.contextIndex, m.Context)
			}
		}
		delete(ms.memories, id)
	}
}

// LoadErrors returns the number of unmarshal/scan errors encountered during cache load.
func (ms *Store) LoadErrors() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.loadErrors
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
	Memory *Memory         `json:"memory"`
	Score  float64         `json:"score"`
	Trust  *trust.Metadata `json:"trust,omitempty"`
}

type CanonicalKnowledge struct {
	ID             string          `json:"id"`
	SourceMemoryID string          `json:"source_memory_id"`
	Title          string          `json:"title"`
	Summary        string          `json:"summary"`
	Entity         string          `json:"entity"`
	Context        string          `json:"context,omitempty"`
	Service        string          `json:"service,omitempty"`
	Owner          string          `json:"owner,omitempty"`
	Status         string          `json:"status,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Importance     float64         `json:"importance"`
	LastVerifiedAt time.Time       `json:"last_verified_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Trust          *trust.Metadata `json:"trust,omitempty"`
}

type CanonicalSearchResult struct {
	Entry *CanonicalKnowledge `json:"entry"`
	Score float64             `json:"score"`
}

type MergeDuplicatesResult struct {
	PrimaryID            string   `json:"primary_id"`
	DuplicateIDs         []string `json:"duplicate_ids"`
	ArchivedDuplicateIDs []string `json:"archived_duplicate_ids"`
	MergedFromCount      int      `json:"merged_from_count"`
}

type MarkOutdatedResult struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	SupersededBy string  `json:"superseded_by,omitempty"`
	Importance   float64 `json:"importance"`
}

type PromoteToCanonicalResult struct {
	ID         string  `json:"id"`
	Layer      string  `json:"layer"`
	Owner      string  `json:"owner"`
	Status     string  `json:"status"`
	Importance float64 `json:"importance"`
}

type ConflictReportItem struct {
	GroupKey        string   `json:"group_key"`
	Entity          string   `json:"entity"`
	Service         string   `json:"service,omitempty"`
	Context         string   `json:"context,omitempty"`
	Subject         string   `json:"subject"`
	Reason          string   `json:"reason"`
	SuggestedAction string   `json:"suggested_action"`
	MemoryIDs       []string `json:"memory_ids"`
	Titles          []string `json:"titles"`
	Statuses        []string `json:"statuses,omitempty"`
	Tags            []string `json:"tags,omitempty"`
}

// ConflictsReport scans stored memories for duplicate candidates and conflicting current knowledge.

func ensureMemorySchema(db *sql.DB) error {
	hasEmbeddingModel, err := memoryColumnExists(db, "embedding_model")
	if err != nil {
		return err
	}
	if !hasEmbeddingModel {
		if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN embedding_model TEXT`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_embedding_model ON memories(embedding_model)`); err != nil {
		return err
	}

	// Temporal columns migration.
	temporalCols := []struct {
		name string
		ddl  string
	}{
		{"valid_from", "ALTER TABLE memories ADD COLUMN valid_from DATETIME"},
		{"valid_until", "ALTER TABLE memories ADD COLUMN valid_until DATETIME"},
		{"superseded_by", "ALTER TABLE memories ADD COLUMN superseded_by TEXT"},
		{"replaces", "ALTER TABLE memories ADD COLUMN replaces TEXT"},
		{"observed_at", "ALTER TABLE memories ADD COLUMN observed_at DATETIME"},
	}
	for _, col := range temporalCols {
		exists, err := memoryColumnExists(db, col.name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_valid_from ON memories(valid_from)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_valid_until ON memories(valid_until)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_superseded_by ON memories(superseded_by)`); err != nil {
		return err
	}

	return nil
}

// maybeStartBackgroundReembed probes the current embedding model and compares it
// with models stored in cached memories. If a mismatch is detected, starts a
// background goroutine to re-embed all memories with the current model.
func (ms *Store) maybeStartBackgroundReembed() {
	if ms.embedder == nil {
		return
	}

	ms.mu.RLock()
	total := len(ms.memories)
	if total == 0 {
		ms.mu.RUnlock()
		return
	}

	// Collect model distribution from cache
	modelCounts := make(map[string]int)
	for _, m := range ms.memories {
		model := m.EmbeddingModel
		if model == "" {
			model = "(none)"
		}
		modelCounts[model]++
	}
	ms.mu.RUnlock()

	// Probe embedder for current model ID
	probeResult, err := ms.embedder.EmbedDetailed(context.Background(), "model probe")
	if err != nil {
		ms.logger.Warn("Failed to probe embedding model at startup, skipping auto-reembed check", zap.Error(err))
		return
	}
	currentModel := probeResult.ModelID

	// Check how many memories need re-embedding
	mismatchCount := 0
	for model, count := range modelCounts {
		if model != currentModel {
			mismatchCount += count
		}
	}
	if mismatchCount == 0 {
		ms.logger.Info("All memories use current embedding model", zap.String("model", currentModel), zap.Int("total", total))
		return
	}

	ms.logger.Info("Embedding model mismatch detected, starting background re-embed",
		zap.String("current_model", currentModel),
		zap.Int("mismatched", mismatchCount),
		zap.Int("total", total),
	)

	ms.accessWG.Add(1)
	go func() {
		defer ms.accessWG.Done()
		result, err := ms.ReembedAll(context.Background())
		if err != nil {
			ms.logger.Error("Background re-embed failed", zap.Error(err))
			return
		}
		ms.logger.Info("Background re-embed completed",
			zap.Int("reembedded", result.Reembedded),
			zap.Int("already_current", result.AlreadyCurrent),
			zap.Int("failed", result.Failed),
			zap.String("model", result.CurrentModel),
		)
	}()
}

func memoryColumnExists(db *sql.DB, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(memories)`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
