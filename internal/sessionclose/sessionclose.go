package sessionclose

import (
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

type ActionKind string

const (
	ActionNew              ActionKind = "new"
	ActionUpdate           ActionKind = "update"
	ActionMerge            ActionKind = "merge"
	ActionSupersede        ActionKind = "supersede"
	ActionPromoteCanonical ActionKind = "promote_canonical"
	ActionRawOnly          ActionKind = "raw_only"
)

type ActionHandling string

const (
	ActionHandlingAutoApply  ActionHandling = "safe_auto_apply"
	ActionHandlingSoftReview ActionHandling = "soft_review"
	ActionHandlingHardReview ActionHandling = "hard_review"
)

type AnalyzeRequest struct {
	Summary          memory.SessionSummary `json:"summary"`
	DryRun           bool                  `json:"dry_run"`
	SaveRaw          bool                  `json:"save_raw"`
	AutoApplyLowRisk bool                  `json:"auto_apply_low_risk"`
}

type ActionState string

const (
	ActionStatePlanned        ActionState = "planned"
	ActionStateApplied        ActionState = "applied"
	ActionStateReviewRequired ActionState = "review_required"
	ActionStateSkipped        ActionState = "skipped"
)

type CandidateAction struct {
	Kind            ActionKind             `json:"kind"`
	Handling        ActionHandling         `json:"handling,omitempty"`
	State           ActionState            `json:"state,omitempty"`
	Title           string                 `json:"title"`
	EngineeringType memory.EngineeringType `json:"engineering_type,omitempty"`
	StorageType     memory.Type            `json:"storage_type,omitempty"`
	TargetMemoryID  string                 `json:"target_memory_id,omitempty"`
	TargetTitle     string                 `json:"target_title,omitempty"`
	AppliedMemoryID string                 `json:"applied_memory_id,omitempty"`
	Confidence      float64                `json:"confidence,omitempty"`
	Rationale       string                 `json:"rationale,omitempty"`
	ExecutionNote   string                 `json:"execution_note,omitempty"`
	DecisionTrace   []string               `json:"decision_trace,omitempty"`
	Candidate       *memory.Memory         `json:"candidate,omitempty"`
}

type AnalysisResult struct {
	DryRun           bool                   `json:"dry_run"`
	Summary          memory.SessionSummary  `json:"summary"`
	Delta            memory.SessionDelta    `json:"delta"`
	Actions          []CandidateAction      `json:"actions"`
	ActionCounts     map[ActionKind]int     `json:"action_counts,omitempty"`
	HandlingCounts   map[ActionHandling]int `json:"handling_counts,omitempty"`
	StateCounts      map[ActionState]int    `json:"state_counts,omitempty"`
	Stats            ConsolidationStats     `json:"stats,omitempty"`
	Review           ReviewSummary          `json:"review,omitempty"`
	AvailableActions []AvailableAction      `json:"available_actions,omitempty"`
	RawSummarySaved  string                 `json:"raw_summary_saved,omitempty"`
}

type ConsolidationStats struct {
	NewCount        int `json:"new_count,omitempty"`
	UpdatedCount    int `json:"updated_count,omitempty"`
	MergedCount     int `json:"merged_count,omitempty"`
	SupersededCount int `json:"superseded_count,omitempty"`
	CanonicalCount  int `json:"canonical_count,omitempty"`
	RawOnlyCount    int `json:"raw_only_count,omitempty"`
	LinkedCount     int `json:"linked_count,omitempty"`
	AppliedCount    int `json:"applied_count,omitempty"`
	PlannedCount    int `json:"planned_count,omitempty"`
	SkippedCount    int `json:"skipped_count,omitempty"`
	OutdatedCount   int `json:"outdated_count,omitempty"`
	ReviewItemCount int `json:"review_item_count,omitempty"`
}

type ReviewSummary struct {
	PendingCount int `json:"pending_count,omitempty"`
	SoftCount    int `json:"soft_count,omitempty"`
	HardCount    int `json:"hard_count,omitempty"`
	AppliedCount int `json:"applied_count,omitempty"`
	LinkedCount  int `json:"linked_count,omitempty"`
}

type AvailableAction struct {
	Key         string `json:"key"`
	Tool        string `json:"tool,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type Service struct {
	store *memory.Store
	now   func() time.Time
}

type RawSaveOptions struct {
	RecordKind string
	ExtraTags  []string
	Metadata   map[string]string
}

type extractedCandidate struct {
	memory *memory.Memory
	trace  []string
}

type modePolicy struct {
	mode               memory.SessionMode
	fallbackEntity     memory.EngineeringType
	strictReview       bool
	extraReviewSignals []string
	typeHints          []modeTypeHint
}

type modeTypeHint struct {
	entity   memory.EngineeringType
	keywords []string
	trace    string
}

func New(store *memory.Store) *Service {
	return &Service{
		store: store,
		now:   time.Now,
	}
}

func (s *Service) Analyze(req AnalyzeRequest) (*AnalysisResult, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("memory store is required")
	}

	summary, err := normalizeSummary(req.Summary, s.now())
	if err != nil {
		return nil, err
	}

	delta, candidates := buildSessionDelta(summary)
	actions, linked, err := s.planActions(summary, candidates)
	if err != nil {
		return nil, err
	}
	delta.LinkedExistingItems = linked

	actions = append(actions, CandidateAction{
		Kind:          ActionRawOnly,
		Title:         rawSummaryTitle(summary),
		Confidence:    1,
		Rationale:     "save only the raw session summary without mutating project knowledge",
		DecisionTrace: []string{"raw_session_summary_available"},
	})
	actions = annotateHandling(actions, summary.Mode)
	actions = initializeActionStates(actions)

	result := &AnalysisResult{
		DryRun:         req.DryRun,
		Summary:        summary,
		Delta:          delta,
		Actions:        actions,
		ActionCounts:   countActions(actions),
		HandlingCounts: countHandling(actions),
	}

	if !req.DryRun {
		if err := s.executeActions(result, req); err != nil {
			return nil, err
		}
	}
	result.StateCounts = countStates(result.Actions)
	result.Stats = buildConsolidationStats(result)
	result.Review = buildReviewSummary(result)
	result.AvailableActions = buildAvailableActions(result)

	return result, nil
}

func (s *Service) SaveRawSummary(summary memory.SessionSummary) (string, error) {
	return s.SaveRawSummaryWithOptions(summary, RawSaveOptions{})
}

func (s *Service) SaveRawSummaryWithOptions(summary memory.SessionSummary, opts RawSaveOptions) (string, error) {
	if s == nil || s.store == nil {
		return "", fmt.Errorf("memory store is required")
	}

	summary, err := normalizeSummary(summary, s.now())
	if err != nil {
		return "", err
	}

	recordKind := strings.TrimSpace(opts.RecordKind)
	if recordKind == "" {
		recordKind = memory.RecordKindSessionSummary
	}
	mem := &memory.Memory{
		Title:      rawSummaryTitle(summary),
		Content:    strings.TrimSpace(summary.Summary),
		Type:       memory.TypeEpisodic,
		Context:    summary.Context,
		Importance: 0.20,
		Tags:       memory.NormalizeTags(append(rawSummaryTags(summary), opts.ExtraTags...)),
		Metadata: map[string]string{
			memory.MetadataRecordKind:     recordKind,
			memory.MetadataSessionMode:    string(summary.Mode),
			memory.MetadataLastVerifiedAt: summary.EndedAt.UTC().Format(time.RFC3339),
		},
	}
	if summary.Service != "" {
		mem.Metadata[memory.MetadataService] = summary.Service
	}
	for key, value := range summary.Metadata {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" || isProtectedSessionMetadataKey(key) {
			continue
		}
		mem.Metadata[key] = value
	}
	for key, value := range normalizeStringMap(opts.Metadata) {
		if isProtectedSessionMetadataKey(key) && key != memory.MetadataRecordKind {
			continue
		}
		mem.Metadata[key] = value
	}

	if err := s.store.Store(mem); err != nil {
		return "", err
	}
	return mem.ID, nil
}

func normalizeSummary(summary memory.SessionSummary, now time.Time) (memory.SessionSummary, error) {
	summary.Summary = strings.TrimSpace(summary.Summary)
	if summary.Summary == "" {
		return memory.SessionSummary{}, fmt.Errorf("session summary is required")
	}
	summary.Metadata = normalizeStringMap(summary.Metadata)
	modeValue := strings.TrimSpace(string(summary.Mode))
	if modeValue == "" {
		modeValue = strings.TrimSpace(summary.Metadata[memory.MetadataSessionMode])
	}
	inferredMode := inferSessionMode(summary.Summary, summary.Tags)
	defaultMode := memory.SessionModeCoding
	if inferredMode != "" {
		defaultMode = inferredMode
	}
	mode, err := memory.ValidateSessionMode(modeValue, defaultMode)
	if err != nil {
		return memory.SessionSummary{}, err
	}
	summary.Mode = mode
	summary.Context = strings.TrimSpace(summary.Context)
	summary.Service = strings.TrimSpace(summary.Service)
	if summary.Service == "" {
		summary.Service = strings.TrimSpace(summary.Metadata[memory.MetadataService])
	}
	summary.Tags = memory.NormalizeTags(summary.Tags)
	if summary.StartedAt.IsZero() {
		if !summary.EndedAt.IsZero() {
			summary.StartedAt = summary.EndedAt
		} else {
			summary.StartedAt = now
		}
	}
	if summary.EndedAt.IsZero() {
		summary.EndedAt = now
	}
	if summary.StartedAt.After(summary.EndedAt) {
		summary.StartedAt = summary.EndedAt
	}
	return summary, nil
}
