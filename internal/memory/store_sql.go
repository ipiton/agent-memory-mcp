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

// unmarshalEmbeddingBinary decodes little-endian binary back to []float32.
// Falls back to JSON unmarshal for legacy data that starts with '['.
func unmarshalEmbeddingBinary(data []byte) ([]float32, error) {
	if len(data) == 0 {
		return nil, nil
	}
	// Legacy JSON format: starts with '['
	if data[0] == '[' {
		var embedding []float32
		if err := json.Unmarshal(data, &embedding); err != nil {
			return nil, err
		}
		return embedding, nil
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

	if _, err := exec.Exec(`
		INSERT INTO memories (id, content, type, title, tags, context, importance, metadata,
		                      embedding_model, embedding, created_at, updated_at, accessed_at, access_count,
		                      valid_from, valid_until, superseded_by, replaces, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ID, m.Content, m.Type, m.Title, tagsJSON, m.Context,
		m.Importance, metadataJSON, m.EmbeddingModel, embeddingBlob,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount,
		nullTime(m.ValidFrom), nullTime(m.ValidUntil), nullStr(m.SupersededBy), nullStr(m.Replaces), nullTime(m.ObservedAt),
	); err != nil {
		return fmt.Errorf("failed to store memory: %w", err)
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
		                    valid_from = ?, valid_until = ?, superseded_by = ?, replaces = ?, observed_at = ?
		WHERE id = ?
	`,
		m.Content, m.Type, m.Title, tagsJSON, m.Context,
		m.Importance, metadataJSON, m.EmbeddingModel, embeddingBlob,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount,
		nullTime(m.ValidFrom), nullTime(m.ValidUntil), nullStr(m.SupersededBy), nullStr(m.Replaces), nullTime(m.ObservedAt),
		m.ID,
	); err != nil {
		return fmt.Errorf("failed to update memory: %w", err)
	}

	return nil
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
