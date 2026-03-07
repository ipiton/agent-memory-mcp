package memory

import (
	"fmt"
	"sort"
	"strings"
)

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

func unionStrings(values ...[]string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, group := range values {
		for _, value := range group {
			value = strings.TrimSpace(value)
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
	sort.Strings(result)
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
	return strings.Join(unionStrings(values...), ",")
}

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
			continue
		}
		content += "\n\nMerged note:\n" + duplicateContent
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
