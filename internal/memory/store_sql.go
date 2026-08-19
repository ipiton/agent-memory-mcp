package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/dbutil"
)

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func marshalMemoryFields(m *Memory) (tagsJSON, metadataJSON string, embeddingBlob []byte, err error) {
	tags, err := json.Marshal(m.Tags)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to marshal tags: %w", err)
	}
	metadata, err := json.Marshal(m.Metadata)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	embeddingBytes := dbutil.EncodeEmbedding(m.Embedding)
	return string(tags), string(metadata), embeddingBytes, nil
}

func insertMemoryRow(exec sqlExecutor, m *Memory) error {
	tagsJSON, metadataJSON, embeddingBlob, err := marshalMemoryFields(m)
	if err != nil {
		return err
	}

	res, err := exec.Exec(`
		INSERT INTO memories (id, content, type, title, tags, context, importance, metadata,
		                      embedding_model, embedding, created_at, updated_at, accessed_at, access_count,
		                      targeted_access_count,
		                      valid_from, valid_until, superseded_by, replaces, observed_at, sediment_layer)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ID, m.Content, m.Type, m.Title, tagsJSON, m.Context,
		m.Importance, metadataJSON, m.EmbeddingModel, embeddingBlob,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount,
		m.TargetedAccessCount,
		nullTime(m.ValidFrom), nullTime(m.ValidUntil), nullStr(m.SupersededBy), nullStr(m.Replaces), nullTime(m.ObservedAt),
		sedimentLayerValue(m.SedimentLayer),
	)
	if err != nil {
		return fmt.Errorf("failed to store memory: %w", err)
	}

	// T75: a plain INSERT writes exactly one row on success. A zero-row result
	// (e.g. a future switch to INSERT OR IGNORE hitting a PK collision) means
	// the write silently did not happen — surface it instead of a false success.
	if n, affErr := res.RowsAffected(); affErr == nil && n != 1 {
		return fmt.Errorf("store memory %s: expected 1 row written, got %d", m.ID, n)
	}

	return nil
}

// verifyMemoryPersisted confirms a just-written memory row is durably readable
// by id (T75 read-after-write veto). Callers must treat a returned error as a
// failed write, not a silent success — this is the inverse-verification guard
// against the "write reported success but did not happen" class (path/connection
// desync, driver anomaly, silent no-op).
func verifyMemoryPersisted(db *sql.DB, id string) error {
	var got string
	err := db.QueryRow(`SELECT id FROM memories WHERE id = ?`, id).Scan(&got)
	if err == sql.ErrNoRows {
		return fmt.Errorf("write verification failed: memory %s not found after write", id)
	}
	if err != nil {
		return fmt.Errorf("write verification failed for %s: %w", id, err)
	}
	return nil
}

// updateMemoryRow deliberately leaves targeted_access_count out of the SET
// list (T113). The counter is owned by flushAccessStats, which increments it
// in SQL; writing it back from an in-memory struct would let a content edit
// that started before a concurrent recall silently roll the count back — and
// the one thing this counter has to be is trustworthy.
func updateMemoryRow(exec sqlExecutor, m *Memory) error {
	tagsJSON, metadataJSON, embeddingBlob, err := marshalMemoryFields(m)
	if err != nil {
		return err
	}

	if _, err := exec.Exec(`
		UPDATE memories SET content = ?, type = ?, title = ?, tags = ?, context = ?,
		                    importance = ?, metadata = ?, embedding_model = ?, embedding = ?,
		                    created_at = ?, updated_at = ?, accessed_at = ?, access_count = ?,
		                    valid_from = ?, valid_until = ?, superseded_by = ?, replaces = ?, observed_at = ?,
		                    sediment_layer = ?
		WHERE id = ?
	`,
		m.Content, m.Type, m.Title, tagsJSON, m.Context,
		m.Importance, metadataJSON, m.EmbeddingModel, embeddingBlob,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount,
		nullTime(m.ValidFrom), nullTime(m.ValidUntil), nullStr(m.SupersededBy), nullStr(m.Replaces), nullTime(m.ObservedAt),
		sedimentLayerValue(m.SedimentLayer),
		m.ID,
	); err != nil {
		return fmt.Errorf("failed to update memory: %w", err)
	}

	return nil
}

// sedimentLayerValue returns the canonical string for the sediment_layer
// column, defaulting to "surface" when the layer is empty or invalid. Keeps
// the NOT NULL constraint satisfied.
func sedimentLayerValue(raw string) string {
	layer := NormalizeSedimentLayer(raw)
	if layer == "" {
		layer = DefaultSedimentLayer
	}
	return string(layer)
}

func nullTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
