package memory

import (
	"fmt"
	"strings"
)

const DefaultImportance = 0.5

func ValidateType(value Type, defaultType Type) (Type, error) {
	if value == "" {
		value = defaultType
	}
	switch value {
	case TypeEpisodic, TypeSemantic, TypeProcedural, TypeWorking:
		return value, nil
	default:
		return "", fmt.Errorf("invalid memory type %q (expected episodic, semantic, procedural, or working)", value)
	}
}

func NormalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func NormalizeMemoryForStore(m *Memory) error {
	if m == nil {
		return fmt.Errorf("memory is required")
	}

	m.Content = strings.TrimSpace(m.Content)
	if m.Content == "" {
		return fmt.Errorf("content parameter is required")
	}

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
		return fmt.Errorf("importance must be between 0.0 and 1.0")
	}

	return nil
}

func DisplayTitle(m *Memory, maxLen int) string {
	if m == nil {
		return ""
	}
	if title := strings.TrimSpace(m.Title); title != "" {
		return title
	}

	value := strings.TrimSpace(m.Content)
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		value = value[:idx]
	}
	value = strings.TrimSpace(value)
	if maxLen > 0 && len(value) > maxLen {
		return value[:maxLen] + "..."
	}
	return value
}

func normalizeMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}

	normalized := make(map[string]string, len(metadata))
	for key, value := range metadata {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		normalized[key] = value
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
