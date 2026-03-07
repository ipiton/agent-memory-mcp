// Package review provides shared review queue resolution logic
// used by both the CLI and the MCP server.
package review

import (
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// NormalizeResolution validates and normalizes a review resolution value.
// Returns "resolved" for empty input. Valid values: resolved, dismissed, deferred.
func NormalizeResolution(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "resolved", nil
	}
	switch value {
	case "resolved", "dismissed", "deferred":
		return value, nil
	default:
		return "", fmt.Errorf("resolution must be resolved, dismissed, or deferred")
	}
}

// ResolvedTags removes review-related tags and appends the resolution status tags.
func ResolvedTags(tags []string, resolution string) []string {
	filtered := make([]string, 0, len(tags)+2)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		switch {
		case tag == "":
			continue
		case tag == "review:required":
			continue
		case strings.HasPrefix(tag, "review:"):
			continue
		case tag == "status:review_required":
			continue
		case tag == "status:resolved" || tag == "status:dismissed" || tag == "status:deferred":
			continue
		default:
			filtered = append(filtered, tag)
		}
	}
	filtered = append(filtered, "review:"+resolution, "status:"+resolution)
	return memory.NormalizeTags(filtered)
}
