package sessionclose

import (
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func uniqueEngineeringTypes(values ...memory.EngineeringType) []memory.EngineeringType {
	return memory.UnionStrings(values)
}

func lexicalOverlap(a string, b string) float64 {
	a = normalizeText(a)
	b = normalizeText(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	aWords := strings.Fields(a)
	bWords := strings.Fields(b)
	if len(aWords) == 0 || len(bWords) == 0 {
		return 0
	}
	bSet := make(map[string]struct{}, len(bWords))
	for _, word := range bWords {
		bSet[word] = struct{}{}
	}
	matches := 0
	for _, word := range aWords {
		if _, ok := bSet[word]; ok {
			matches++
		}
	}
	denominator := max(len(aWords), len(bWords))
	return float64(matches) / float64(denominator)
}

var punctuationReplacer = strings.NewReplacer(
	":", " ",
	";", " ",
	",", " ",
	".", " ",
	"(", " ",
	")", " ",
	"/", " ",
)

func normalizeText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = punctuationReplacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func defaultImportance(entity memory.EngineeringType) float64 {
	switch entity {
	case memory.EngineeringTypeIncident:
		return 0.90
	case memory.EngineeringTypeDecision, memory.EngineeringTypeRunbook:
		return 0.85
	case memory.EngineeringTypePostmortem, memory.EngineeringTypeMigrationNote:
		return 0.80
	default:
		return 0.65
	}
}

func mergeTags(base []string, extra []string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := append([]string(nil), base...)
	merged = append(merged, extra...)
	return memory.NormalizeTags(merged)
}

func mergeMetadata(base map[string]string, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	merged := memory.NormalizeMetadata(base)
	if merged == nil {
		merged = make(map[string]string)
	}
	for key, value := range memory.NormalizeMetadata(extra) {
		merged[key] = value
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func sanitizeKnowledgeCandidate(candidate *memory.Memory) *memory.Memory {
	if candidate == nil {
		return nil
	}
	sanitized := *candidate
	sanitized.ID = ""
	sanitized.Embedding = nil
	sanitized.EmbeddingModel = ""
	sanitized.Tags = make([]string, 0, len(candidate.Tags))
	for _, tag := range candidate.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == "session-close" || tag == "auto-session" {
			continue
		}
		sanitized.Tags = append(sanitized.Tags, tag)
	}
	sanitized.Tags = memory.NormalizeTags(sanitized.Tags)
	sanitized.Metadata = memory.NormalizeMetadata(candidate.Metadata)
	delete(sanitized.Metadata, memory.MetadataRecordKind)
	delete(sanitized.Metadata, memory.MetadataDerivedFrom)
	delete(sanitized.Metadata, memory.MetadataSessionMode)
	delete(sanitized.Metadata, memory.MetadataReviewRequired)
	delete(sanitized.Metadata, memory.MetadataReviewReason)
	if len(sanitized.Metadata) == 0 {
		sanitized.Metadata = nil
	}
	return &sanitized
}

func shouldReplaceContent(current string, candidate string) bool {
	current = strings.TrimSpace(current)
	candidate = strings.TrimSpace(candidate)
	switch {
	case candidate == "", candidate == current:
		return false
	case current == "":
		return true
	}

	if lexicalOverlap(current, candidate) < 0.95 {
		return false
	}
	normalizedCurrent := normalizeText(current)
	normalizedCandidate := normalizeText(candidate)
	return len(normalizedCandidate) > len(normalizedCurrent) || strings.Contains(normalizedCandidate, normalizedCurrent)
}

func containsTrace(trace []string, value string) bool {
	value = strings.TrimSpace(value)
	for _, item := range trace {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}

func memoryPreview(mem *memory.Memory) *memory.Memory {
	if mem == nil {
		return nil
	}
	preview := *mem
	preview.Embedding = nil
	return &preview
}

func rawSummaryTitle(summary memory.SessionSummary) string {
	parts := []string{"Session close"}
	if summary.Context != "" {
		parts = append(parts, summary.Context)
	}
	if summary.Service != "" {
		parts = append(parts, summary.Service)
	}
	return strings.Join(parts, " / ")
}

func rawSummaryTags(summary memory.SessionSummary) []string {
	tags := append([]string{"session-summary", "session-close", "mode:" + string(summary.Mode)}, summary.Tags...)
	if summary.Service != "" {
		tags = append(tags, "service:"+summary.Service)
	}
	return memory.NormalizeTags(tags)
}

func isProtectedSessionMetadataKey(key string) bool {
	switch strings.TrimSpace(key) {
	case memory.MetadataRecordKind,
		memory.MetadataSessionMode,
		memory.MetadataLastVerifiedAt,
		memory.MetadataService:
		return true
	default:
		return false
	}
}
