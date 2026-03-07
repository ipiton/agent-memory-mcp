package userio

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
)

const (
	MaxMemoryContentLen = 100000
	MaxQueryLen         = 10000
)

func ParseMemoryType(raw string, defaultType memory.Type, allowAll bool) (memory.Type, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultType, nil
	}
	if allowAll && value == "all" {
		return "", nil
	}
	return memory.ValidateType(memory.Type(value), defaultType)
}

func NormalizeImportance(value float64, defaultValue float64) (float64, error) {
	if math.IsNaN(value) {
		value = defaultValue
	}
	if value < 0 || value > 1 {
		return 0, fmt.Errorf("importance must be between 0.0 and 1.0")
	}
	return value, nil
}

func ValidateMemoryContent(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("content parameter is required")
	}
	if len(content) > MaxMemoryContentLen {
		return fmt.Errorf("content too long (max %d characters)", MaxMemoryContentLen)
	}
	return nil
}

func ValidateQuery(query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("query parameter is required")
	}
	if len(query) > MaxQueryLen {
		return fmt.Errorf("query too long (max %d characters)", MaxQueryLen)
	}
	return nil
}

func ParseCSVTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return NormalizeTags(strings.Split(raw, ","))
}

func NormalizeTags(tags []string) []string {
	return memory.NormalizeTags(tags)
}

func FormatTrust(tm *trust.Metadata) string {
	if tm == nil {
		return ""
	}
	return FormatTrustSummary(tm.KnowledgeLayer, tm.SourceType, tm.Confidence, tm.FreshnessScore, tm.Owner, tm.LastVerifiedAt)
}

func FormatTrustSummary(layer string, sourceType string, confidence float64, freshness float64, owner string, verifiedAt time.Time) string {
	parts := []string{
		fmt.Sprintf("layer=%s", ValueOrUnknown(layer)),
		fmt.Sprintf("source=%s", ValueOrUnknown(sourceType)),
		fmt.Sprintf("confidence=%.2f", confidence),
		fmt.Sprintf("freshness=%.2f", freshness),
		fmt.Sprintf("owner=%s", ValueOrUnknown(owner)),
	}
	if !verifiedAt.IsZero() {
		parts = append(parts, fmt.Sprintf("verified=%s", verifiedAt.UTC().Format(time.RFC3339)))
	}
	return strings.Join(parts, " ")
}

func ValueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}
