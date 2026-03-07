package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/trust"
)

type ProjectBankView string

const (
	ProjectBankViewCanonicalOverview ProjectBankView = "canonical_overview"
	ProjectBankViewDecisions         ProjectBankView = "decisions"
	ProjectBankViewRunbooks          ProjectBankView = "runbooks"
	ProjectBankViewIncidents         ProjectBankView = "incidents"
	ProjectBankViewCaveats           ProjectBankView = "caveats"
	ProjectBankViewMigrations        ProjectBankView = "migrations"
	ProjectBankViewReviewQueue       ProjectBankView = "review_queue"
)

type ProjectBankOptions struct {
	Filters Filters  `json:"filters,omitempty"`
	Service string   `json:"service,omitempty"`
	Status  string   `json:"status,omitempty"`
	Owner   string   `json:"owner,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

type ProjectBankItem struct {
	ID             string          `json:"id"`
	SourceMemoryID string          `json:"source_memory_id,omitempty"`
	Title          string          `json:"title"`
	Summary        string          `json:"summary"`
	Entity         string          `json:"entity,omitempty"`
	Type           Type            `json:"type,omitempty"`
	Context        string          `json:"context,omitempty"`
	Service        string          `json:"service,omitempty"`
	Owner          string          `json:"owner,omitempty"`
	Status         string          `json:"status,omitempty"`
	Lifecycle      LifecycleStatus `json:"lifecycle,omitempty"`
	ReviewRequired bool            `json:"review_required,omitempty"`
	KnowledgeLayer string          `json:"knowledge_layer,omitempty"`
	RecordKind     string          `json:"record_kind,omitempty"`
	SessionMode    SessionMode     `json:"session_mode,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Importance     float64         `json:"importance,omitempty"`
	LastVerifiedAt time.Time       `json:"last_verified_at,omitempty"`
	UpdatedAt      time.Time       `json:"updated_at,omitempty"`
	Trust          *trust.Metadata  `json:"trust,omitempty"`
}

type ProjectBankSection struct {
	Key         string             `json:"key"`
	Title       string             `json:"title"`
	Description string             `json:"description,omitempty"`
	Items       []*ProjectBankItem `json:"items,omitempty"`
}

type ProjectBankViewResult struct {
	View          ProjectBankView      `json:"view"`
	Title         string               `json:"title"`
	Context       string               `json:"context,omitempty"`
	Service       string               `json:"service,omitempty"`
	Status        string               `json:"status,omitempty"`
	Owner         string               `json:"owner,omitempty"`
	Tags          []string             `json:"tags,omitempty"`
	TotalCount    int                  `json:"total_count,omitempty"`
	EntityCounts  map[string]int       `json:"entity_counts,omitempty"`
	SectionCounts map[string]int       `json:"section_counts,omitempty"`
	Sections      []ProjectBankSection `json:"sections,omitempty"`
}

func ValidateProjectBankView(value string) (ProjectBankView, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, " ", "_")
	if normalized == "" || normalized == "overview" {
		return ProjectBankViewCanonicalOverview, nil
	}

	switch ProjectBankView(normalized) {
	case ProjectBankViewCanonicalOverview,
		ProjectBankViewDecisions,
		ProjectBankViewRunbooks,
		ProjectBankViewIncidents,
		ProjectBankViewCaveats,
		ProjectBankViewMigrations,
		ProjectBankViewReviewQueue:
		return ProjectBankView(normalized), nil
	default:
		return "", &ErrValidation{Message: fmt.Sprintf("invalid project bank view %q", value)}
	}
}

func (ms *Store) ProjectBankView(ctx context.Context, view ProjectBankView, options ProjectBankOptions) (*ProjectBankViewResult, error) {
	normalizedView, err := ValidateProjectBankView(string(view))
	if err != nil {
		return nil, err
	}
	options = normalizeProjectBankOptions(options)

	memories, err := ms.List(ctx, options.Filters, 0)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	items := make([]*ProjectBankItem, 0, len(memories))
	for _, mem := range memories {
		item := projectBankItemFromMemory(mem, deriveTrustMetadata(mem, now))
		if !projectBankMatchesFilters(item, options) {
			continue
		}
		items = append(items, item)
	}

	result := &ProjectBankViewResult{
		View:          normalizedView,
		Title:         projectBankViewTitle(normalizedView),
		Context:       options.Filters.Context,
		Service:       options.Service,
		Status:        options.Status,
		Owner:         options.Owner,
		Tags:          append([]string(nil), options.Tags...),
		EntityCounts:  countProjectBankEntities(items),
		SectionCounts: make(map[string]int),
	}
	result.Sections = buildProjectBankSections(normalizedView, items, options.Limit)
	for _, section := range result.Sections {
		result.SectionCounts[section.Key] = len(section.Items)
	}
	result.TotalCount = uniqueProjectBankCount(result.Sections)
	if len(result.SectionCounts) == 0 {
		result.SectionCounts = nil
	}
	if len(result.EntityCounts) == 0 {
		result.EntityCounts = nil
	}

	return result, nil
}

func normalizeProjectBankOptions(options ProjectBankOptions) ProjectBankOptions {
	options.Filters.Context = strings.TrimSpace(options.Filters.Context)
	options.Service = strings.TrimSpace(options.Service)
	options.Status = normalizeStatus(options.Status)
	options.Owner = strings.TrimSpace(options.Owner)
	options.Tags = NormalizeTags(options.Tags)
	if options.Limit <= 0 {
		options.Limit = 10
	}
	return options
}

func projectBankItemFromMemory(mem *Memory, trust *trust.Metadata) *ProjectBankItem {
	if mem == nil {
		return nil
	}
	metadata := mem.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	item := &ProjectBankItem{
		ID:             mem.ID,
		SourceMemoryID: mem.ID,
		Title:          DisplayTitle(mem, 120),
		Summary:        strings.TrimSpace(mem.Content),
		Entity:         memoryEntity(mem),
		Type:           mem.Type,
		Context:        strings.TrimSpace(mem.Context),
		Service:        memoryService(mem),
		Status:         memoryStatus(mem),
		Lifecycle:      LifecycleStatusOf(mem),
		ReviewRequired: RequiresReview(mem),
		KnowledgeLayer: memoryKnowledgeLayer(mem),
		RecordKind:     strings.TrimSpace(metadata[MetadataRecordKind]),
		SessionMode:    SessionMode(strings.TrimSpace(metadata[MetadataSessionMode])),
		Tags:           append([]string(nil), mem.Tags...),
		Importance:     mem.Importance,
		UpdatedAt:      mem.UpdatedAt,
		Trust:          trust,
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = mem.CreatedAt
	}
	if trust != nil {
		item.Owner = trust.Owner
		item.LastVerifiedAt = trust.LastVerifiedAt
		if item.KnowledgeLayer == "" {
			item.KnowledgeLayer = trust.KnowledgeLayer
		}
	}
	return item
}

func projectBankMatchesFilters(item *ProjectBankItem, options ProjectBankOptions) bool {
	if item == nil {
		return false
	}
	if options.Service != "" && !strings.EqualFold(item.Service, options.Service) {
		return false
	}
	if options.Owner != "" && !strings.EqualFold(item.Owner, options.Owner) {
		return false
	}
	if options.Status != "" && !projectBankStatusMatches(item, options.Status) {
		return false
	}
	return HasAllTags(item.Tags, options.Tags)
}

func projectBankStatusMatches(item *ProjectBankItem, status string) bool {
	if item == nil {
		return false
	}
	status = normalizeStatus(status)
	if status == "" {
		return true
	}
	if status == "review_required" {
		return item.ReviewRequired
	}
	return normalizeStatus(item.Status) == status || normalizeStatus(string(item.Lifecycle)) == status
}

// HasAllTags returns true if tags contains all required tags.
func HasAllTags(tags []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[strings.TrimSpace(tag)] = struct{}{}
	}
	for _, requiredTag := range required {
		if _, ok := tagSet[strings.TrimSpace(requiredTag)]; !ok {
			return false
		}
	}
	return true
}
