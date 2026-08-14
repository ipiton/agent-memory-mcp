package memory

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"time"
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
	embeddingBytes := marshalEmbeddingBinary(m.Embedding)
	return string(tags), string(metadata), embeddingBytes, nil
}

// marshalEmbeddingBinary encodes []float32 as little-endian binary (4 bytes per float).
func marshalEmbeddingBinary(embedding []float32) []byte {
	if len(embedding) == 0 {
		return nil
	}
	buf := make([]byte, len(embedding)*4)
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// unmarshalEmbeddingBinary decodes little-endian binary back to []float32,
// falling back to the legacy JSON array format.
//
// T109: the format used to be decided by one byte — data[0] == '[' meant JSON.
// A binary blob is 4 bytes per dimension of arbitrary float bits, so roughly one
// in 256 of them begins with 0x5B and was handed to the JSON parser, which
// failed. The record still loaded (the caller counts this as a soft error) but
// arrived in the cache with **no embedding**, silently dropping out of semantic
// recall. On the live bank that was 21 of 4536 blobs against 17.7 expected by
// chance — which is what identified the mechanism.
//
// The prefix is now a hint, not a verdict: JSON has to actually parse, and a
// blob that merely starts like it falls through to the binary path it belonged
// to all along. Nothing about the stored format changes, so the affected
// records recover on the next load without a re-embed.
func unmarshalEmbeddingBinary(data []byte) ([]float32, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var embedding []float32
		if err := json.Unmarshal(data, &embedding); err == nil {
			return embedding, nil
		}
	}
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("invalid embedding blob size %d (must be multiple of 4)", len(data))
	}
	embedding := make([]float32, len(data)/4)
	for i := range embedding {
		embedding[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return embedding, nil
}

func insertMemoryRow(exec sqlExecutor, m *Memory) error {
	tagsJSON, metadataJSON, embeddingBlob, err := marshalMemoryFields(m)
	if err != nil {
		return err
	}

	res, err := exec.Exec(`
		INSERT INTO memories (id, content, type, title, tags, context, importance, metadata,
		                      embedding_model, embedding, created_at, updated_at, accessed_at, access_count,
		                      valid_from, valid_until, superseded_by, replaces, observed_at, sediment_layer)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ID, m.Content, m.Type, m.Title, tagsJSON, m.Context,
		m.Importance, metadataJSON, m.EmbeddingModel, embeddingBlob,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount,
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
