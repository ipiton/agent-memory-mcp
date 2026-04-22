package memory

import (
	"fmt"
	"strings"
	"time"
)

const (
	MetadataEntity             = "entity"
	MetadataService            = "service"
	MetadataSeverity           = "severity"
	MetadataStatus             = "status"
	MetadataLifecycleStatus    = "lifecycle_status"
	MetadataKnowledgeLayer     = "knowledge_layer"
	MetadataOwner              = "owner"
	MetadataLastVerifiedAt     = "last_verified_at"
	MetadataReviewRequired     = "review_required"
	MetadataReviewReason       = "review_reason"
	MetadataRecordKind         = "record_kind"
	MetadataSessionMode        = "session_mode"
	MetadataDerivedFrom        = "derived_from"
	MetadataSessionBoundary    = "session_boundary"
	MetadataSessionOrigin      = "session_origin"
	MetadataSourceSessionID    = "source_session_id"
	MetadataActionKind         = "action_kind"
	MetadataActionHandling     = "action_handling"
	MetadataVerifiedBy         = "verified_by"
	MetadataVerificationMethod = "verification_method"
	MetadataVerificationStatus = "verification_status"
)

const (
	RecordKindSessionSummary    = "session_summary"
	RecordKindSessionCheckpoint = "session_checkpoint"
	RecordKindKnowledgeItem     = "knowledge_item"
	RecordKindReviewQueueItem   = "review_queue_item"
)

type EngineeringType string

const (
	EngineeringTypeDecision      EngineeringType = "decision"
	EngineeringTypeIncident      EngineeringType = "incident"
	EngineeringTypeRunbook       EngineeringType = "runbook"
	EngineeringTypePostmortem    EngineeringType = "postmortem"
	EngineeringTypeMigrationNote EngineeringType = "migration-note"
	EngineeringTypeCaveat        EngineeringType = "caveat"
	EngineeringTypeProcedure     EngineeringType = "procedure"
	EngineeringTypeDeadEnd       EngineeringType = "dead_end"
)

type LifecycleStatus string

const (
	LifecycleDraft      LifecycleStatus = "draft"
	LifecycleActive     LifecycleStatus = "active"
	LifecycleOutdated   LifecycleStatus = "outdated"
	LifecycleSuperseded LifecycleStatus = "superseded"
	LifecycleCanonical  LifecycleStatus = "canonical"
)

type SessionMode string

const (
	SessionModeCoding    SessionMode = "coding"
	SessionModeIncident  SessionMode = "incident"
	SessionModeMigration SessionMode = "migration"
	SessionModeResearch  SessionMode = "research"
	SessionModeCleanup   SessionMode = "cleanup"
)

// SessionSummary is the raw end-of-session capture before consolidation planning.
type SessionSummary struct {
	ID        string            `json:"id,omitempty"`
	Mode      SessionMode       `json:"mode,omitempty"`
	Context   string            `json:"context,omitempty"`
	Service   string            `json:"service,omitempty"`
	Summary   string            `json:"summary"`
	StartedAt time.Time         `json:"started_at,omitempty"`
	EndedAt   time.Time         `json:"ended_at,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// SessionDelta is the normalized bridge between raw session capture and consolidation decisions.
type SessionDelta struct {
	Summary             *SessionSummary   `json:"summary,omitempty"`
	ExtractedEntities   []EngineeringType `json:"extracted_entities,omitempty"`
	TouchedServices     []string          `json:"touched_services,omitempty"`
	TouchedPaths        []string          `json:"touched_paths,omitempty"`
	SuspectedChanges    []string          `json:"suspected_changes,omitempty"`
	InferredTopics      []string          `json:"inferred_topics,omitempty"`
	Risks               []string          `json:"risks,omitempty"`
	LinkedExistingItems []string          `json:"linked_existing_items,omitempty"`
}

func ValidateEngineeringType(value string, allowEmpty bool) (EngineeringType, error) {
	switch normalizeEngineeringType(value) {
	case "":
		if allowEmpty {
			return "", nil
		}
		return "", &ErrValidation{Message: "engineering type is required"}
	case EngineeringTypeDecision,
		EngineeringTypeIncident,
		EngineeringTypeRunbook,
		EngineeringTypePostmortem,
		EngineeringTypeMigrationNote,
		EngineeringTypeCaveat,
		EngineeringTypeProcedure,
		EngineeringTypeDeadEnd:
		return normalizeEngineeringType(value), nil
	default:
		return "", &ErrValidation{Message: fmt.Sprintf("invalid engineering type %q", strings.TrimSpace(value))}
	}
}

func ValidateSessionMode(value string, defaultMode SessionMode) (SessionMode, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultMode, nil
	}
	switch SessionMode(value) {
	case SessionModeCoding, SessionModeIncident, SessionModeMigration, SessionModeResearch, SessionModeCleanup:
		return SessionMode(value), nil
	default:
		return "", &ErrValidation{Message: fmt.Sprintf("invalid session mode %q", value)}
	}
}

func DefaultStorageTypeForEngineeringType(entity EngineeringType) Type {
	switch entity {
	case EngineeringTypeIncident, EngineeringTypePostmortem:
		return TypeEpisodic
	case EngineeringTypeRunbook, EngineeringTypeProcedure:
		return TypeProcedural
	case EngineeringTypeDecision, EngineeringTypeMigrationNote, EngineeringTypeCaveat, EngineeringTypeDeadEnd:
		return TypeSemantic
	default:
		return TypeSemantic
	}
}

func EngineeringTypeOf(m *Memory) EngineeringType {
	if m == nil {
		return ""
	}
	if len(m.Metadata) > 0 {
		if entity, err := ValidateEngineeringType(m.Metadata[MetadataEntity], true); err == nil && entity != "" {
			return entity
		}
	}
	if entity := inferEngineeringTypeFromTags(m.Tags); entity != "" {
		return entity
	}
	return ""
}

func LifecycleStatusOf(m *Memory) LifecycleStatus {
	if m == nil {
		return LifecycleDraft
	}
	if len(m.Metadata) > 0 {
		if metadataBool(m.Metadata, "canonical") || strings.EqualFold(strings.TrimSpace(m.Metadata[MetadataKnowledgeLayer]), "canonical") {
			return LifecycleCanonical
		}
		if lifecycle := normalizeLifecycleStatus(m.Metadata[MetadataLifecycleStatus]); lifecycle != "" {
			return lifecycle
		}
		if metadataBool(m.Metadata, "archived") {
			return LifecycleSuperseded
		}
		if lifecycle := normalizeLifecycleStatus(m.Metadata[MetadataStatus]); lifecycle != "" {
			return lifecycle
		}
	}
	if m.Type == TypeWorking {
		return LifecycleDraft
	}
	return LifecycleActive
}

func RequiresReview(m *Memory) bool {
	if m == nil || len(m.Metadata) == 0 {
		return false
	}
	if metadataBool(m.Metadata, MetadataReviewRequired) {
		return true
	}
	return normalizeStatus(m.Metadata[MetadataStatus]) == "review_required"
}

func IsSessionSummaryMemory(m *Memory) bool {
	if m == nil || len(m.Metadata) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(m.Metadata[MetadataRecordKind]), RecordKindSessionSummary)
}

func IsSessionCheckpointMemory(m *Memory) bool {
	if m == nil || len(m.Metadata) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(m.Metadata[MetadataRecordKind]), RecordKindSessionCheckpoint)
}

func IsReviewQueueMemory(m *Memory) bool {
	if m == nil || len(m.Metadata) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(m.Metadata[MetadataRecordKind]), RecordKindReviewQueueItem)
}

func BuildEngineeringTags(entity EngineeringType, service string, severity string, status string, reviewRequired bool, extra []string) []string {
	tags := append([]string(nil), extra...)
	if entity != "" {
		tags = append(tags, string(entity))
	}
	if service = strings.TrimSpace(service); service != "" {
		tags = append(tags, "service:"+service)
	}
	if severity = strings.TrimSpace(severity); severity != "" {
		tags = append(tags, "severity:"+severity)
	}
	if status = strings.TrimSpace(status); status != "" {
		tags = append(tags, "status:"+status)
	}
	if reviewRequired {
		tags = append(tags, "review:required")
	}
	return NormalizeTags(tags)
}

func BuildEngineeringMetadata(entity EngineeringType, service string, severity string, status string, reviewRequired bool, extra map[string]string) map[string]string {
	metadata := make(map[string]string, len(extra)+6)
	if entity != "" {
		metadata[MetadataEntity] = string(entity)
	}
	metadata[MetadataLastVerifiedAt] = time.Now().UTC().Format(time.RFC3339)
	if service = strings.TrimSpace(service); service != "" {
		metadata[MetadataService] = service
	}
	if severity = strings.TrimSpace(severity); severity != "" {
		metadata[MetadataSeverity] = severity
	}
	if status = strings.TrimSpace(status); status != "" {
		metadata[MetadataStatus] = status
	}
	if reviewRequired {
		metadata[MetadataReviewRequired] = "true"
	}
	for key, value := range extra {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		metadata[key] = value
	}

	normalized, err := normalizeEngineeringMetadata(metadata, nil, DefaultStorageTypeForEngineeringType(entity))
	if err != nil {
		return NormalizeMetadata(metadata)
	}
	return normalized
}

func normalizeEngineeringMetadata(metadata map[string]string, tags []string, storageType Type) (map[string]string, error) {
	normalized := NormalizeMetadata(metadata)
	if normalized == nil {
		normalized = make(map[string]string)
	}

	entity, err := ValidateEngineeringType(normalized[MetadataEntity], true)
	if err != nil {
		return nil, err
	}
	if entity == "" {
		entity = inferEngineeringTypeFromTags(tags)
	}
	if entity != "" {
		normalized[MetadataEntity] = string(entity)
	}

	if rawStatus := normalizeStatus(normalized[MetadataStatus]); rawStatus == "review_required" {
		normalized[MetadataReviewRequired] = "true"
		delete(normalized, MetadataStatus)
	}

	if metadataBool(normalized, "canonical") || strings.EqualFold(strings.TrimSpace(normalized[MetadataKnowledgeLayer]), "canonical") {
		normalized[MetadataLifecycleStatus] = string(LifecycleCanonical)
	}

	if lifecycle := normalizeLifecycleStatus(normalized[MetadataLifecycleStatus]); lifecycle != "" {
		normalized[MetadataLifecycleStatus] = string(lifecycle)
	} else if strings.TrimSpace(normalized[MetadataLifecycleStatus]) != "" {
		return nil, &ErrValidation{Message: fmt.Sprintf("invalid lifecycle status %q", normalized[MetadataLifecycleStatus])}
	}

	if normalized[MetadataLifecycleStatus] == "" {
		if lifecycle := normalizeLifecycleStatus(normalized[MetadataStatus]); lifecycle != "" {
			normalized[MetadataLifecycleStatus] = string(lifecycle)
		}
	}
	if normalized[MetadataLifecycleStatus] == string(LifecycleCanonical) {
		normalized["canonical"] = "true"
		normalized[MetadataKnowledgeLayer] = "canonical"
	}

	switch normalizeStatus(normalized[MetadataReviewRequired]) {
	case "", "0", "false", "no":
		delete(normalized, MetadataReviewRequired)
	case "1", "true", "yes":
		normalized[MetadataReviewRequired] = "true"
	default:
		return nil, &ErrValidation{Message: fmt.Sprintf("invalid review_required value %q", normalized[MetadataReviewRequired])}
	}

	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func normalizeEngineeringTags(tags []string, metadata map[string]string) []string {
	extra := append([]string(nil), tags...)
	if entity := strings.TrimSpace(metadata[MetadataEntity]); entity != "" {
		extra = append(extra, entity)
	}
	if service := strings.TrimSpace(metadata[MetadataService]); service != "" {
		extra = append(extra, "service:"+service)
	}
	if severity := strings.TrimSpace(metadata[MetadataSeverity]); severity != "" {
		extra = append(extra, "severity:"+severity)
	}
	if status := strings.TrimSpace(metadata[MetadataStatus]); status != "" {
		extra = append(extra, "status:"+status)
	}
	if metadataBool(metadata, MetadataReviewRequired) {
		extra = append(extra, "review:required")
	}
	return NormalizeTags(extra)
}

func inferEngineeringTypeFromTags(tags []string) EngineeringType {
	for _, tag := range tags {
		if entity, err := ValidateEngineeringType(tag, true); err == nil && entity != "" {
			return entity
		}
	}
	return ""
}

func normalizeEngineeringType(value string) EngineeringType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "decision", "decisions":
		return EngineeringTypeDecision
	case "incident", "incidents":
		return EngineeringTypeIncident
	case "runbook", "runbooks":
		return EngineeringTypeRunbook
	case "postmortem", "postmortems":
		return EngineeringTypePostmortem
	case "migration-note", "migration_note", "migrationnote", "migration":
		return EngineeringTypeMigrationNote
	case "caveat", "caveats", "workaround":
		return EngineeringTypeCaveat
	case "procedure", "procedures":
		return EngineeringTypeProcedure
	case "dead_end", "dead-end", "deadend", "dead_ends":
		return EngineeringTypeDeadEnd
	default:
		return ""
	}
}

func normalizeLifecycleStatus(value string) LifecycleStatus {
	switch normalizeStatus(value) {
	case "":
		return ""
	case "draft", "proposed", "investigating", "pending":
		return LifecycleDraft
	case "active", "accepted", "approved", "confirmed", "resolved", "verified", "current":
		return LifecycleActive
	case "outdated", "deprecated", "stale":
		return LifecycleOutdated
	case "superseded", "merged", "archived", "replaced":
		return LifecycleSuperseded
	case "canonical":
		return LifecycleCanonical
	default:
		return ""
	}
}
