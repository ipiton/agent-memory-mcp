package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// parseMetadataJSON unmarshals a metadata blob from a sql.NullString into
// a non-nil map[string]string. Empty / "null" / invalid strings yield an
// empty map; the unmarshal error is returned for callers that care.
//
// Several read paths (loadMemoriesToCache, Get, getBatch) historically
// silently dropped unmarshal errors — those callers can `_ =` the error.
// IncrementReferencedByCount and similar write paths surface it.
func parseMetadataJSON(raw sql.NullString) (map[string]string, error) {
	metadata := map[string]string{}
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		return metadata, nil
	}
	if err := json.Unmarshal([]byte(raw.String), &metadata); err != nil {
		return map[string]string{}, fmt.Errorf("unmarshal metadata: %w", err)
	}
	if metadata == nil {
		// json.Unmarshal of "null" onto &map resets to nil; defensive.
		metadata = map[string]string{}
	}
	return metadata, nil
}

// updateCachedField runs fn against the cachedMemory for id under the cache
// mutex. No-op when id is absent. Centralises the lock-look-up-mutate trio
// that write paths (PromoteSediment, DemoteSediment, IncrementReferencedBy,
// RecountReferences) repeated four times.
//
// Callers should capture `now := time.Now()` once and pass it to BOTH the
// SQL UPDATE and fn — the previous pattern called time.Now() in two places
// per write, causing microsecond drift between SQL.updated_at and the
// cached UpdatedAt field.
func (ms *Store) updateCachedField(id string, fn func(*cachedMemory)) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if cm, ok := ms.memories[id]; ok && cm != nil {
		fn(cm)
	}
}

// cachedMemoryToMemory builds a *Memory from a cachedMemory without
// hitting the database. Replaces and ObservedAt are not in the cache and
// remain zero — see ListLightweight godoc.
func cachedMemoryToMemory(cm *cachedMemory) *Memory {
	if cm == nil {
		return nil
	}
	m := &Memory{
		ID:             cm.ID,
		Content:        cm.Content,
		Type:           cm.Type,
		Title:          cm.Title,
		Tags:           append([]string(nil), cm.Tags...),
		Context:        cm.Context,
		Importance:     cm.Importance,
		Metadata:       copyMetadata(cm.Metadata),
		Embedding:      cm.Embedding,
		EmbeddingModel: cm.EmbeddingModel,
		CreatedAt:      cm.CreatedAt,
		UpdatedAt:      cm.UpdatedAt,
		AccessedAt:     cm.AccessedAt,
		AccessCount:    cm.AccessCount,
		ValidFrom:      cm.ValidFrom,
		ValidUntil:     cm.ValidUntil,
		SupersededBy:   cm.SupersededBy,
		SedimentLayer:  string(cm.SedimentLayer),
	}
	return m
}

func copyMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

// UnionStrings deduplicates and sorts values from one or more slices.
func UnionStrings[T ~string](values ...[]T) []T {
	seen := make(map[T]struct{})
	result := make([]T, 0)
	for _, group := range values {
		for _, value := range group {
			value = T(strings.TrimSpace(string(value)))
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func joinCSVUnique(values ...[]string) string {
	return strings.Join(UnionStrings(values...), ",")
}

// maxMergedContentLen limits merged content to prevent unbounded growth during merge operations.
const maxMergedContentLen = 256 * 1024

func mergeContent(primary string, duplicates []*Memory) string {
	content := strings.TrimSpace(primary)
	for _, duplicate := range duplicates {
		if duplicate == nil {
			continue
		}
		duplicateContent := strings.TrimSpace(duplicate.Content)
		if duplicateContent == "" {
			continue
		}
		if strings.Contains(strings.ToLower(content), strings.ToLower(duplicateContent)) {
			continue
		}
		title := strings.TrimSpace(duplicate.Title)
		if title != "" {
			content += fmt.Sprintf("\n\nMerged note from %s:\n%s", title, duplicateContent)
		} else {
			content += "\n\nMerged note:\n" + duplicateContent
		}
		if len(content) > maxMergedContentLen {
			content = content[:maxMergedContentLen] + "\n[truncated: merged content exceeded size limit]"
			break
		}
	}
	return content
}

// copyMemory creates a deep copy of a Memory, including slices and maps.
func copyMemory(m *Memory) *Memory {
	if m == nil {
		return nil
	}
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
