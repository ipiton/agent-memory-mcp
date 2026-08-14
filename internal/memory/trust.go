package memory

import (
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
)

// trustInput is what both derivation paths reduce to. T90 D5: they used to be
// two hand-written copies whose answers had drifted apart, so the same entry
// got a different confidence depending on whether it was served from the cache
// or from a full row. Two concrete divergences:
//
//   - The cached path tested `Lifecycle == "review_required"`, which is dead
//     code: LifecycleStatus only ever holds draft/active/outdated/superseded/
//     canonical. Its review detection therefore came down to the tag alone,
//     while the full path looked only at metadata and ignored the tag.
//   - The cached path never read last_verified_at, so it scored freshness off
//     UpdatedAt even for entries carrying an explicit verification stamp.
//     Metadata has been cache-resident since T52, so there was nothing left to
//     justify the difference.
type trustInput struct {
	sourceType string
	metadata   map[string]string
	tags       []string
	lifecycle  LifecycleStatus
	owner      string
	layer      string
	updatedAt  time.Time
	createdAt  time.Time
}

func deriveTrust(in trustInput, now time.Time) *trust.Metadata {
	owner := strings.TrimSpace(in.owner)
	layer := strings.ToLower(strings.TrimSpace(in.layer))
	lastVerifiedAt := in.updatedAt

	if len(in.metadata) > 0 {
		if owner == "" {
			owner = strings.TrimSpace(in.metadata[MetadataOwner])
		}
		if layer == "" {
			layer = strings.ToLower(strings.TrimSpace(in.metadata[MetadataKnowledgeLayer]))
		}
		if verified := parseMetadataTime(in.metadata[MetadataLastVerifiedAt]); !verified.IsZero() {
			lastVerifiedAt = verified
		}
	}

	if lastVerifiedAt.IsZero() {
		lastVerifiedAt = in.createdAt
	}
	if owner == "" {
		owner = defaultOwnerForMemorySource(in.sourceType)
	}
	if layer == "" && in.lifecycle == LifecycleCanonical {
		layer = "canonical"
	}
	if layer == "" {
		layer = "raw"
	}

	reviewRequired := requiresReview(in.metadata, in.tags)

	return &trust.Metadata{
		KnowledgeLayer: layer,
		SourceType:     in.sourceType,
		Confidence:     confidenceForMemory(in.sourceType, in.lifecycle, owner, layer, reviewRequired),
		LastVerifiedAt: lastVerifiedAt,
		Owner:          owner,
		FreshnessScore: scoring.FreshnessScore(lastVerifiedAt, now),
	}
}

func deriveTrustMetadata(m *Memory, now time.Time) *trust.Metadata {
	return deriveTrust(trustInput{
		sourceType: memoryEntity(m),
		metadata:   m.Metadata,
		tags:       m.Tags,
		lifecycle:  LifecycleStatusOf(m),
		updatedAt:  m.UpdatedAt,
		createdAt:  m.CreatedAt,
	}, now)
}

func deriveTrustMetadataFromCached(m *cachedMemory, now time.Time) *trust.Metadata {
	return deriveTrust(trustInput{
		sourceType: cachedMemoryEntity(m),
		metadata:   m.Metadata,
		tags:       m.Tags,
		lifecycle:  m.Lifecycle,
		owner:      m.Owner,
		layer:      m.KnowledgeLayer,
		updatedAt:  m.UpdatedAt,
		createdAt:  m.CreatedAt,
	}, now)
}

func hasTag(tags []string, target string) bool {
	for _, t := range tags {
		if t == target {
			return true
		}
	}
	return false
}

func cachedMemoryEntity(m *cachedMemory) string {
	if m == nil {
		return ""
	}
	// Simplified entity detection for cached objects
	for _, tag := range m.Tags {
		t := strings.ToLower(strings.TrimSpace(tag))
		switch t {
		case "decision", "runbook", "postmortem", "incident":
			return t
		}
	}
	return string(m.Type)
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

// TruncateRunes truncates a string to maxRunes runes, appending "..." if truncated.
func TruncateRunes(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// MemoryEntity returns the entity type (engineering type or memory type) for external consumers.
func MemoryEntity(m *Memory) string { return memoryEntity(m) }

// MemoryService returns the service name extracted from metadata or tags.
func MemoryService(m *Memory) string { return memoryService(m) }

// IsArchivedMemory returns true if the memory is superseded or explicitly archived.
func IsArchivedMemory(m *Memory) bool { return isArchivedMemory(m) }

// LastVerifiedAt returns the last verification timestamp for a memory.
func LastVerifiedAt(m *Memory) time.Time {
	if m == nil {
		return time.Time{}
	}
	if len(m.Metadata) > 0 {
		if t := parseMetadataTime(m.Metadata[MetadataLastVerifiedAt]); !t.IsZero() {
			return t
		}
	}
	return m.UpdatedAt
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
