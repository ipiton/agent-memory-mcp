package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func marshalMemoryFields(m *Memory) (tagsJSON, metadataJSON, embeddingJSON string, err error) {
	tags, err := json.Marshal(m.Tags)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to marshal tags: %w", err)
	}
	metadata, err := json.Marshal(m.Metadata)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to marshal metadata: %w", err)
	}
	embedding, err := json.Marshal(m.Embedding)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to marshal embedding: %w", err)
	}
	return string(tags), string(metadata), string(embedding), nil
}

func insertMemoryRow(exec sqlExecutor, m *Memory) error {
	tagsJSON, metadataJSON, embeddingJSON, err := marshalMemoryFields(m)
	if err != nil {
		return err
	}

	if _, err := exec.Exec(`
		INSERT INTO memories (id, content, type, title, tags, context, importance, metadata,
		                      embedding_model, embedding, created_at, updated_at, accessed_at, access_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ID, m.Content, m.Type, m.Title, tagsJSON, m.Context,
		m.Importance, metadataJSON, m.EmbeddingModel, embeddingJSON,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount,
	); err != nil {
		return fmt.Errorf("failed to store memory: %w", err)
	}

	return nil
}

func updateMemoryRow(exec sqlExecutor, m *Memory) error {
	tagsJSON, metadataJSON, embeddingJSON, err := marshalMemoryFields(m)
	if err != nil {
		return err
	}

	if _, err := exec.Exec(`
		UPDATE memories SET content = ?, type = ?, title = ?, tags = ?, context = ?,
		                    importance = ?, metadata = ?, embedding_model = ?, embedding = ?,
		                    created_at = ?, updated_at = ?, accessed_at = ?, access_count = ?
		WHERE id = ?
	`,
		m.Content, m.Type, m.Title, tagsJSON, m.Context,
		m.Importance, metadataJSON, m.EmbeddingModel, embeddingJSON,
		m.CreatedAt, m.UpdatedAt, m.AccessedAt, m.AccessCount, m.ID,
	); err != nil {
		return fmt.Errorf("failed to update memory: %w", err)
	}

	return nil
}
