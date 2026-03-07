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
		return "", &ErrValidation{Message: fmt.Sprintf("invalid memory type %q (expected episodic, semantic, procedural, or working)", value)}
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

// NormalizeMetadata trims keys/values and drops empty entries.
func NormalizeMetadata(metadata map[string]string) map[string]string {
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
