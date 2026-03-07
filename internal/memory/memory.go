// Package memory provides persistent agent memory storage with semantic vector search.
package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	// In-memory cache for fast search
	memories     map[string]*Memory
	contextIndex map[string]map[string]*Memory // context → id → *Memory
	loadErrors   int                           // count of unmarshal errors during cache load
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
		memories:     make(map[string]*Memory),
		contextIndex: make(map[string]map[string]*Memory),
	}

	// Load memories into cache
	if err := store.loadMemoriesToCache(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to load memories: %w", err)
	}

	store.accessWG.Add(1)
	go store.accessStatsWorker()

	return store, nil
}

// loadMemoriesToCache loads all memories from SQLite into memory cache
func (ms *Store) loadMemoriesToCache() error {
	rows, err := ms.db.Query(`
		SELECT id, content, type, title, tags, context, importance, metadata, embedding_model,
		       embedding, created_at, updated_at, accessed_at, access_count
		FROM memories
	`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.memories = make(map[string]*Memory)
	ms.contextIndex = make(map[string]map[string]*Memory)
	ms.loadErrors = 0

	for rows.Next() {
		var m Memory
		var tagsJSON, metadataJSON, embeddingModel sql.NullString
		var embeddingBlob []byte
		var createdAt, updatedAt, accessedAt sql.NullTime

		err := rows.Scan(
			&m.ID, &m.Content, &m.Type, &m.Title, &tagsJSON, &m.Context,
			&m.Importance, &metadataJSON, &embeddingModel, &embeddingBlob,
			&createdAt, &updatedAt, &accessedAt, &m.AccessCount,
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

		// Parse metadata
		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &m.Metadata); err != nil {
				ms.logger.Warn("Failed to unmarshal metadata", zap.String("id", m.ID), zap.Error(err))
				ms.loadErrors++
			}
		}

		// Parse embedding
		if len(embeddingBlob) > 0 {
			if err := json.Unmarshal(embeddingBlob, &m.Embedding); err != nil {
				ms.logger.Warn("Failed to unmarshal embedding", zap.String("id", m.ID), zap.Error(err))
				ms.loadErrors++
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

		cp := copyMemory(&m)
		ms.cacheSetLocked(cp)
	}

	return rows.Err()
}

// cacheSetLocked adds or updates a memory in the cache and context index.
// Caller MUST hold ms.mu for writing.
func (ms *Store) cacheSetLocked(m *Memory) {
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
			ms.contextIndex[m.Context] = make(map[string]*Memory)
		}
		ms.contextIndex[m.Context][m.ID] = m
	}
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
	return nil
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
