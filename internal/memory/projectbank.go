package memory

import (
	"context"
	"fmt"
	"sort"
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
		return "", fmt.Errorf("invalid project bank view %q", value)
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

func buildProjectBankSections(view ProjectBankView, items []*ProjectBankItem, limit int) []ProjectBankSection {
	switch view {
	case ProjectBankViewCanonicalOverview:
		canonical := collectProjectBankItems(items, limit, projectBankIsCanonicalItem, sortProjectBankCanonicalItems)
		sessionDeltas := collectProjectBankItems(items, limit, projectBankIsSessionDelta, sortProjectBankRecentItems)
		reviewQueue := collectProjectBankItems(items, limit, projectBankIsReviewQueueItem, sortProjectBankRecentItems)
		attention := collectProjectBankItems(items, limit, projectBankNeedsAttention, sortProjectBankAttentionItems)
		return []ProjectBankSection{
			{
				Key:         "canonical_knowledge",
				Title:       "Canonical knowledge",
				Description: "Current confirmed project knowledge promoted into the canonical layer.",
				Items:       canonical,
			},
			{
				Key:         "recent_session_deltas",
				Title:       "Recent session deltas",
				Description: "Raw session summaries that recently changed the local project context.",
				Items:       sessionDeltas,
			},
			{
				Key:         "review_queue",
				Title:       "Review queue",
				Description: "Pending review items produced by background session consolidation.",
				Items:       reviewQueue,
			},
			{
				Key:         "needs_review_or_refresh",
				Title:       "Needs review or refresh",
				Description: "Outdated, superseded, draft, or review-required knowledge that may need consolidation.",
				Items:       attention,
			},
		}
	default:
		filtered := collectProjectBankItems(items, limit, func(item *ProjectBankItem) bool {
			return projectBankMatchesView(item, view)
		}, sortProjectBankEntityItems)
		return []ProjectBankSection{
			{
				Key:         string(view),
				Title:       projectBankViewTitle(view),
				Description: projectBankViewDescription(view),
				Items:       filtered,
			},
		}
	}
}

func collectProjectBankItems(items []*ProjectBankItem, limit int, keep func(*ProjectBankItem) bool, sorter func([]*ProjectBankItem)) []*ProjectBankItem {
	filtered := make([]*ProjectBankItem, 0, len(items))
	for _, item := range items {
		if !keep(item) {
			continue
		}
		filtered = append(filtered, item)
	}
	if sorter != nil {
		sorter(filtered)
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

func projectBankItemFromMemory(mem *Memory, trust *trust.Metadata) *ProjectBankItem {
	if mem == nil {
		return nil
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
		RecordKind:     strings.TrimSpace(mem.Metadata[MetadataRecordKind]),
		SessionMode:    SessionMode(strings.TrimSpace(mem.Metadata[MetadataSessionMode])),
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
	return projectBankHasAllTags(item.Tags, options.Tags)
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

func projectBankHasAllTags(tags []string, required []string) bool {
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

func projectBankMatchesView(item *ProjectBankItem, view ProjectBankView) bool {
	if item == nil || projectBankIsSessionDelta(item) || projectBankIsSessionCheckpoint(item) {
		return false
	}
	if view == ProjectBankViewReviewQueue {
		return projectBankIsReviewQueueItem(item)
	}
	if projectBankIsReviewQueueItem(item) {
		return false
	}

	switch view {
	case ProjectBankViewDecisions:
		return item.Entity == string(EngineeringTypeDecision)
	case ProjectBankViewRunbooks:
		return item.Entity == string(EngineeringTypeRunbook) || item.Entity == string(EngineeringTypeProcedure)
	case ProjectBankViewIncidents:
		return item.Entity == string(EngineeringTypeIncident) || item.Entity == string(EngineeringTypePostmortem)
	case ProjectBankViewCaveats:
		return item.Entity == string(EngineeringTypeCaveat)
	case ProjectBankViewMigrations:
		return item.Entity == string(EngineeringTypeMigrationNote)
	default:
		return false
	}
}

func projectBankIsCanonicalItem(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.Lifecycle == LifecycleCanonical || item.KnowledgeLayer == "canonical"
}

func projectBankIsSessionDelta(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.RecordKind == RecordKindSessionSummary
}

func projectBankIsSessionCheckpoint(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.RecordKind == RecordKindSessionCheckpoint
}

func projectBankIsReviewQueueItem(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.RecordKind == RecordKindReviewQueueItem && item.ReviewRequired
}

func projectBankNeedsAttention(item *ProjectBankItem) bool {
	if item == nil || projectBankIsSessionDelta(item) || projectBankIsSessionCheckpoint(item) || projectBankIsReviewQueueItem(item) {
		return false
	}
	if item.ReviewRequired {
		return true
	}
	switch item.Lifecycle {
	case LifecycleDraft, LifecycleOutdated, LifecycleSuperseded:
		return true
	default:
		return false
	}
}

func sortProjectBankCanonicalItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		return projectBankCompareByFreshness(items[i], items[j])
	})
}

func sortProjectBankRecentItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		if !items[i].LastVerifiedAt.Equal(items[j].LastVerifiedAt) {
			return items[i].LastVerifiedAt.After(items[j].LastVerifiedAt)
		}
		return strings.ToLower(items[i].Title) < strings.ToLower(items[j].Title)
	})
}

func sortProjectBankAttentionItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		leftRank := projectBankAttentionRank(items[i])
		rightRank := projectBankAttentionRank(items[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return projectBankCompareByFreshness(items[i], items[j])
	})
}

func sortProjectBankEntityItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		leftRank := projectBankLifecycleRank(items[i])
		rightRank := projectBankLifecycleRank(items[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return projectBankCompareByFreshness(items[i], items[j])
	})
}

func projectBankAttentionRank(item *ProjectBankItem) int {
	if item == nil {
		return 99
	}
	if item.ReviewRequired {
		return 0
	}
	switch item.Lifecycle {
	case LifecycleDraft:
		return 1
	case LifecycleOutdated:
		return 2
	case LifecycleSuperseded:
		return 3
	default:
		return 4
	}
}

func projectBankLifecycleRank(item *ProjectBankItem) int {
	if item == nil {
		return 99
	}
	switch item.Lifecycle {
	case LifecycleCanonical:
		return 0
	case LifecycleActive:
		return 1
	case "":
		return 2
	case LifecycleDraft:
		return 3
	case LifecycleOutdated:
		return 4
	case LifecycleSuperseded:
		return 5
	default:
		return 6
	}
}

func projectBankCompareByFreshness(left *ProjectBankItem, right *ProjectBankItem) bool {
	if left == nil || right == nil {
		return left != nil
	}
	if !left.LastVerifiedAt.Equal(right.LastVerifiedAt) {
		return left.LastVerifiedAt.After(right.LastVerifiedAt)
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	if left.Importance != right.Importance {
		return left.Importance > right.Importance
	}
	return strings.ToLower(left.Title) < strings.ToLower(right.Title)
}

func countProjectBankEntities(items []*ProjectBankItem) map[string]int {
	counts := make(map[string]int)
	for _, item := range items {
		if item == nil || projectBankIsSessionDelta(item) || projectBankIsSessionCheckpoint(item) || projectBankIsReviewQueueItem(item) {
			continue
		}
		switch {
		case item.Entity == string(EngineeringTypeDecision):
			counts["decisions"]++
		case item.Entity == string(EngineeringTypeRunbook) || item.Entity == string(EngineeringTypeProcedure):
			counts["runbooks"]++
		case item.Entity == string(EngineeringTypeIncident) || item.Entity == string(EngineeringTypePostmortem):
			counts["incidents"]++
		case item.Entity == string(EngineeringTypeCaveat):
			counts["caveats"]++
		case item.Entity == string(EngineeringTypeMigrationNote):
			counts["migrations"]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func uniqueProjectBankCount(sections []ProjectBankSection) int {
	seen := make(map[string]struct{})
	for _, section := range sections {
		for _, item := range section.Items {
			if item == nil || item.ID == "" {
				continue
			}
			seen[item.ID] = struct{}{}
		}
	}
	return len(seen)
}

func projectBankViewTitle(view ProjectBankView) string {
	switch view {
	case ProjectBankViewCanonicalOverview:
		return "Canonical overview"
	case ProjectBankViewDecisions:
		return "Decision bank"
	case ProjectBankViewRunbooks:
		return "Runbook bank"
	case ProjectBankViewIncidents:
		return "Incident bank"
	case ProjectBankViewCaveats:
		return "Caveat bank"
	case ProjectBankViewMigrations:
		return "Migration bank"
	case ProjectBankViewReviewQueue:
		return "Review queue"
	default:
		return "Project bank"
	}
}

func projectBankViewDescription(view ProjectBankView) string {
	switch view {
	case ProjectBankViewDecisions:
		return "Current engineering decisions across raw and canonical knowledge."
	case ProjectBankViewRunbooks:
		return "Operational runbooks and reusable procedures."
	case ProjectBankViewIncidents:
		return "Incident history and postmortem knowledge."
	case ProjectBankViewCaveats:
		return "Known caveats, gotchas, and workarounds."
	case ProjectBankViewMigrations:
		return "Migration notes, cutover guidance, and rollout constraints."
	case ProjectBankViewReviewQueue:
		return "Pending review items from background session consolidation."
	default:
		return ""
	}
}
