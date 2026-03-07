package memory

import (
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
)

func deriveTrustMetadata(m *Memory, now time.Time) *trust.Metadata {
	sourceType := memoryEntity(m)
	owner := ""
	layer := ""
	lifecycle := LifecycleStatusOf(m)
	reviewRequired := RequiresReview(m)
	lastVerifiedAt := m.UpdatedAt

	if len(m.Metadata) > 0 {
		owner = strings.TrimSpace(m.Metadata[MetadataOwner])
		layer = strings.ToLower(strings.TrimSpace(m.Metadata[MetadataKnowledgeLayer]))
		if verified := parseMetadataTime(m.Metadata[MetadataLastVerifiedAt]); !verified.IsZero() {
			lastVerifiedAt = verified
		}
	}

	if lastVerifiedAt.IsZero() {
		lastVerifiedAt = m.CreatedAt
	}

	if owner == "" {
		owner = defaultOwnerForMemorySource(sourceType)
	}
	if layer == "" && lifecycle == LifecycleCanonical {
		layer = "canonical"
	}
	if layer == "" {
		layer = "raw"
	}

	return &trust.Metadata{
		KnowledgeLayer: layer,
		SourceType:     sourceType,
		Confidence:     confidenceForMemory(sourceType, lifecycle, owner, layer, reviewRequired),
		LastVerifiedAt: lastVerifiedAt,
		Owner:          owner,
		FreshnessScore: scoring.FreshnessScore(lastVerifiedAt, now),
	}
}

func parseMetadataTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// confidenceForMemory computes a trust confidence score (0.20–0.99) for a memory entry.
//
// Base confidence by source type (higher = more authoritative):
//   - decision (0.90): approved engineering decisions carry highest weight
//   - runbook/procedure (0.88): reviewed operational docs
//   - postmortem (0.86): structured incident analysis
//   - migration_note (0.84): migration context is usually verified
//   - incident/episodic (0.78): event-based, may lack full context
//   - semantic (0.72): general facts, moderate baseline
//   - caveat (0.68): warnings/caveats may become stale
//   - working (0.55): ephemeral context, lowest baseline
//   - default (0.65): unclassified memories
//
// Adjustments by lifecycle, layer, review status, and ownership
// shift confidence up or down from the base. Final result is clamped to [0.20, 0.99].
func confidenceForMemory(sourceType string, lifecycle LifecycleStatus, owner string, layer string, reviewRequired bool) float64 {
	confidence := 0.65
	switch sourceType {
	case "decision":
		confidence = 0.90
	case "runbook", string(EngineeringTypeProcedure), string(TypeProcedural):
		confidence = 0.88
	case "postmortem":
		confidence = 0.86
	case "incident", string(TypeEpisodic):
		confidence = 0.78
	case string(EngineeringTypeMigrationNote):
		confidence = 0.84
	case string(EngineeringTypeCaveat):
		confidence = 0.68
	case string(TypeSemantic):
		confidence = 0.72
	case string(TypeWorking):
		confidence = 0.55
	}

	// Lifecycle adjustments: active/canonical boost, draft/outdated/superseded penalize.
	switch lifecycle {
	case LifecycleActive:
		confidence += 0.04
	case LifecycleDraft:
		confidence -= 0.05
	case LifecycleOutdated:
		confidence -= 0.10
	case LifecycleSuperseded:
		confidence -= 0.18
	case LifecycleCanonical:
		confidence += 0.05
	}

	// Canonical layer without canonical lifecycle: promoted but not yet fully verified.
	if layer == "canonical" && lifecycle != LifecycleCanonical {
		confidence += 0.05
	}

	// Pending review reduces trust — content not yet validated.
	if reviewRequired {
		confidence -= 0.08
	}

	// Known ownership adds small credibility boost.
	if owner != "" && owner != "unknown" {
		confidence += 0.02
	}

	return clampConfidence(confidence)
}

func defaultOwnerForMemorySource(sourceType string) string {
	switch sourceType {
	case "decision":
		return "engineering"
	case "runbook", string(EngineeringTypeProcedure), "incident", "postmortem":
		return "operations"
	case string(EngineeringTypeMigrationNote):
		return "platform"
	default:
		return "unknown"
	}
}

func metadataBool(metadata map[string]string, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(metadata[key])) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func clampConfidence(value float64) float64 {
	switch {
	case value < 0.20:
		return 0.20
	case value > 0.99:
		return 0.99
	default:
		return value
	}
}

func normalizeStatus(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func memoryEntity(m *Memory) string {
	if m == nil {
		return ""
	}
	if entity := EngineeringTypeOf(m); entity != "" {
		return string(entity)
	}
	return string(m.Type)
}

func memoryService(m *Memory) string {
	if m == nil {
		return ""
	}
	if len(m.Metadata) > 0 {
		if service := strings.TrimSpace(m.Metadata[MetadataService]); service != "" {
			return service
		}
	}
	for _, tag := range m.Tags {
		if strings.HasPrefix(strings.TrimSpace(tag), "service:") {
			return strings.TrimSpace(strings.TrimPrefix(tag, "service:"))
		}
	}
	return ""
}

func memoryStatus(m *Memory) string {
	if m == nil || len(m.Metadata) == 0 {
		return ""
	}
	return normalizeStatus(m.Metadata["status"])
}

func memoryKnowledgeLayer(m *Memory) string {
	if m == nil || len(m.Metadata) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m.Metadata[MetadataKnowledgeLayer]))
}

// IsCanonicalMemory returns true if the memory has canonical lifecycle status.
func IsCanonicalMemory(m *Memory) bool {
	return LifecycleStatusOf(m) == LifecycleCanonical
}

func isArchivedMemory(m *Memory) bool {
	if m == nil {
		return false
	}
	if LifecycleStatusOf(m) == LifecycleSuperseded {
		return true
	}
	return metadataBool(m.Metadata, "archived")
}
