// Package steward provides automated knowledge maintenance for the memory store.
// It detects duplicates, conflicts, stale entries, and canonical promotion candidates,
// then applies safe actions or surfaces review items.
package steward

import (
	"fmt"
	"time"
)

// RunScope controls which scanners run during a steward cycle.
type RunScope string

const (
	ScopeFull              RunScope = "full"
	ScopeDuplicates        RunScope = "duplicates"
	ScopeConflicts         RunScope = "conflicts"
	ScopeStale             RunScope = "stale"
	ScopeCanonical         RunScope = "canonical"
	ScopeSemanticConflicts RunScope = "semantic_conflicts"
	ScopeWorkingTTL        RunScope = "working_ttl"
)

// PolicyMode controls when stewardship runs execute.
type PolicyMode string

const (
	PolicyModeOff         PolicyMode = "off"
	PolicyModeManual      PolicyMode = "manual"
	PolicyModeScheduled   PolicyMode = "scheduled"
	PolicyModeEventDriven PolicyMode = "event_driven"
)

// ActionKind describes what a steward action does.
type ActionKind string

const (
	ActionMergeDuplicates      ActionKind = "merge_duplicates"
	ActionMarkStale            ActionKind = "mark_stale"
	ActionPromoteCanonical     ActionKind = "promote_canonical"
	ActionRefreshFreshness     ActionKind = "refresh_freshness"
	ActionFlagConflict         ActionKind = "flag_conflict"
	ActionFlagContradiction    ActionKind = "flag_contradiction"
	ActionDeleteExpiredWorking ActionKind = "delete_expired_working"
)

// AllActionKinds lists every kind a scan can produce. It exists so the
// resolution side can be checked for coverage (T104): ActionKind and the
// resolution verbs are two independent string vocabularies, and nothing in the
// compiler ties them together — a new kind with no verb able to carry it out
// yields inbox items that cannot be resolved by their own intent, only
// suppressed or deferred. TestEveryActionKindHasAResolution enforces the link.
var AllActionKinds = []ActionKind{
	ActionMergeDuplicates,
	ActionMarkStale,
	ActionPromoteCanonical,
	ActionRefreshFreshness,
	ActionFlagConflict,
	ActionFlagContradiction,
	ActionDeleteExpiredWorking,
}

// ResolutionAction is the verb an operator applies to an inbox item.
type ResolutionAction string

const (
	ResolveMerge          ResolutionAction = "merge"
	ResolveMarkOutdated   ResolutionAction = "mark_outdated"
	ResolveMarkSuperseded ResolutionAction = "mark_superseded"
	ResolvePromote        ResolutionAction = "promote"
	ResolveVerify         ResolutionAction = "verify"
	ResolveDelete         ResolutionAction = "delete"
	ResolveSuppress       ResolutionAction = "suppress"
	ResolveDefer          ResolutionAction = "defer"
)

// ResolutionForActionKind names the verb that carries out each scanned kind —
// the action a reviewer would pick to say "yes, do what the scan proposed".
// Other verbs remain available on any item; this map only records that at least
// one verb exists per kind.
var ResolutionForActionKind = map[ActionKind]ResolutionAction{
	ActionMergeDuplicates:      ResolveMerge,
	ActionMarkStale:            ResolveMarkOutdated,
	ActionPromoteCanonical:     ResolvePromote,
	ActionRefreshFreshness:     ResolveVerify,
	ActionFlagConflict:         ResolveMarkSuperseded,
	ActionFlagContradiction:    ResolveMarkSuperseded,
	ActionDeleteExpiredWorking: ResolveDelete,
}

// ActionHandling indicates whether an action can be auto-applied.
type ActionHandling string

const (
	HandlingSafeAutoApply  ActionHandling = "safe_auto_apply"
	HandlingReviewRequired ActionHandling = "review_required"
)

// ActionState tracks the lifecycle of a steward action.
type ActionState string

const (
	StatePlanned        ActionState = "planned"
	StateApplied        ActionState = "applied"
	StateReviewRequired ActionState = "review_required"
	StateSkipped        ActionState = "skipped"
)

// Policy configures stewardship behavior and thresholds.
type Policy struct {
	Mode             PolicyMode `json:"mode"`
	ScheduleInterval string     `json:"schedule_interval"` // e.g. "24h"

	// Event triggers that can start a steward run.
	EventTriggers []string `json:"event_triggers,omitempty"`

	// Detection thresholds.
	DuplicateSimilarity    float64 `json:"duplicate_similarity"`     // default 0.85; reserved for future semantic similarity detection
	StaleDays              int     `json:"stale_days"`               // default 30
	CanonicalMinConfidence float64 `json:"canonical_min_confidence"` // default 0.80
	CanonicalMinEvidence   int     `json:"canonical_min_evidence"`   // default 2

	// Working memory has a separate, more aggressive TTL because working entries
	// are short-lived by design (transient task state, session-extracted noise).
	// T106: ≤0 does NOT disable the TTL — EffectiveWorkingTTLDays falls back to
	// 14 days. There is no value of this field that turns working-memory expiry
	// off; use AutoDeleteExpiredWorking to stop acting on it.
	WorkingMemoryTTLDays          int     `json:"working_memory_ttl_days"`          // ≤0 → fallback 14
	WorkingDeleteImportanceCutoff float64 `json:"working_delete_importance_cutoff"` // entries above are sent to review, not auto-deleted

	// Auto-apply rules — only applied when dry_run=false.
	AutoMergeExactDuplicates   bool `json:"auto_merge_exact_duplicates"`   // default false
	AutoMarkStaleBeyondDays    int  `json:"auto_mark_stale_beyond_days"`   // 0 = disabled; default 30
	AutoRefreshFreshnessScores bool `json:"auto_refresh_freshness_scores"` // default true
	AutoDeleteExpiredWorking   bool `json:"auto_delete_expired_working"`   // default true

	// AutoMarkStaleImportanceCutoff caps which stale entries auto-mark (T72):
	// only entries below this importance are auto-staled; at or above it they go
	// to review even when past AutoMarkStaleBeyondDays. <=0 falls back to 0.6.
	AutoMarkStaleImportanceCutoff float64 `json:"auto_mark_stale_importance_cutoff"`

	// Auto-merge of subject-key duplicate groups (T69). Opt-in and guarded:
	// a group auto-merges only when its detection confidence >= MinConfidence
	// AND every non-primary member is textually near-identical to the primary
	// (Jaccard >= the content-similarity threshold) AND no member is canonical.
	// MinConfidence <= 0 disables auto-merge entirely (default and the safe value
	// for policies loaded from DB before these fields existed).
	AutoMergeDuplicateMinConfidence   float64 `json:"auto_merge_duplicate_min_confidence"`   // default 0.95; <=0 = disabled
	AutoMergeRequireContentSimilarity float64 `json:"auto_merge_require_content_similarity"` // default 0.85; min Jaccard(primary,dup)

	UpdatedAt time.Time `json:"updated_at"`
}

// EffectiveStaleDays returns StaleDays with a fallback to 30 if not set.
func (p Policy) EffectiveStaleDays() int {
	if p.StaleDays <= 0 {
		return 30
	}
	return p.StaleDays
}

// EffectiveWorkingTTLDays returns WorkingMemoryTTLDays, falling back to 14 days
// when it is ≤0.
//
// T106: the field's old comment claimed 0 meant "disabled". It never did — this
// fallback fires for 0 exactly as it does for a negative value, so the TTL
// cannot be switched off through this field at all. The claim cost debugging
// time on 2026-08-12, when a policy reading working_memory_ttl_days: 0 sat next
// to an inbox reporting "TTL: 14 days" and the pair read as a counter bug.
func (p Policy) EffectiveWorkingTTLDays() int {
	if p.WorkingMemoryTTLDays <= 0 {
		return 14
	}
	return p.WorkingMemoryTTLDays
}

// EffectiveWorkingDeleteImportanceCutoff returns the importance threshold above
// which expired working entries go to review queue instead of auto-delete.
func (p Policy) EffectiveWorkingDeleteImportanceCutoff() float64 {
	if p.WorkingDeleteImportanceCutoff <= 0 {
		return 0.6
	}
	return p.WorkingDeleteImportanceCutoff
}

// EffectiveAutoMarkStaleImportanceCutoff returns the importance ceiling below
// which stale entries may auto-mark (T72), falling back to 0.6 when unset so
// stores with a policy persisted before this field still guard high-importance
// knowledge from unattended auto-staling.
func (p Policy) EffectiveAutoMarkStaleImportanceCutoff() float64 {
	if p.AutoMarkStaleImportanceCutoff <= 0 {
		return 0.6
	}
	return p.AutoMarkStaleImportanceCutoff
}

// EffectiveAutoMergeContentSimilarity returns the minimum Jaccard similarity a
// non-primary duplicate must share with the primary before an auto-merge is
// allowed, falling back to 0.85 when unset (so enabling auto-merge via
// MinConfidence alone still keeps a high content guard).
func (p Policy) EffectiveAutoMergeContentSimilarity() float64 {
	if p.AutoMergeRequireContentSimilarity <= 0 {
		return 0.85
	}
	return p.AutoMergeRequireContentSimilarity
}

// PolicyPatch is the wire form of a `steward_policy set` request: every field
// is optional, and only the fields actually present in the JSON are applied.
//
// T103: the tool used to unmarshal straight into Policy and save the result
// whole. Policy's fields are value types, so "absent" and "zero" are the same
// bit pattern — a caller sending one field silently reset every other one to
// 0/false. That is how a live store lost its T72/T73 operator decisions
// (auto_merge_duplicate_min_confidence → 0 disables auto-merge outright,
// auto_delete_expired_working → false diverts the whole expiry stream into the
// review queue) with no error and no audit trail. Pointers make the two states
// distinguishable at the type level, which no amount of handler-side validation
// can do.
type PolicyPatch struct {
	Mode             *PolicyMode `json:"mode,omitempty"`
	ScheduleInterval *string     `json:"schedule_interval,omitempty"`
	EventTriggers    *[]string   `json:"event_triggers,omitempty"`

	DuplicateSimilarity    *float64 `json:"duplicate_similarity,omitempty"`
	StaleDays              *int     `json:"stale_days,omitempty"`
	CanonicalMinConfidence *float64 `json:"canonical_min_confidence,omitempty"`
	CanonicalMinEvidence   *int     `json:"canonical_min_evidence,omitempty"`

	WorkingMemoryTTLDays          *int     `json:"working_memory_ttl_days,omitempty"`
	WorkingDeleteImportanceCutoff *float64 `json:"working_delete_importance_cutoff,omitempty"`

	AutoMergeExactDuplicates   *bool `json:"auto_merge_exact_duplicates,omitempty"`
	AutoMarkStaleBeyondDays    *int  `json:"auto_mark_stale_beyond_days,omitempty"`
	AutoRefreshFreshnessScores *bool `json:"auto_refresh_freshness_scores,omitempty"`
	AutoDeleteExpiredWorking   *bool `json:"auto_delete_expired_working,omitempty"`

	AutoMarkStaleImportanceCutoff *float64 `json:"auto_mark_stale_importance_cutoff,omitempty"`

	AutoMergeDuplicateMinConfidence   *float64 `json:"auto_merge_duplicate_min_confidence,omitempty"`
	AutoMergeRequireContentSimilarity *float64 `json:"auto_merge_require_content_similarity,omitempty"`
}

// Apply returns p with the patch's present fields overwritten. A nil field
// leaves the current value alone; a present field is applied verbatim, so an
// explicit 0 or false is honoured.
func (p Policy) Apply(patch PolicyPatch) Policy {
	applyPtr(&p.Mode, patch.Mode)
	applyPtr(&p.ScheduleInterval, patch.ScheduleInterval)
	applyPtr(&p.EventTriggers, patch.EventTriggers)
	applyPtr(&p.DuplicateSimilarity, patch.DuplicateSimilarity)
	applyPtr(&p.StaleDays, patch.StaleDays)
	applyPtr(&p.CanonicalMinConfidence, patch.CanonicalMinConfidence)
	applyPtr(&p.CanonicalMinEvidence, patch.CanonicalMinEvidence)
	applyPtr(&p.WorkingMemoryTTLDays, patch.WorkingMemoryTTLDays)
	applyPtr(&p.WorkingDeleteImportanceCutoff, patch.WorkingDeleteImportanceCutoff)
	applyPtr(&p.AutoMergeExactDuplicates, patch.AutoMergeExactDuplicates)
	applyPtr(&p.AutoMarkStaleBeyondDays, patch.AutoMarkStaleBeyondDays)
	applyPtr(&p.AutoRefreshFreshnessScores, patch.AutoRefreshFreshnessScores)
	applyPtr(&p.AutoDeleteExpiredWorking, patch.AutoDeleteExpiredWorking)
	applyPtr(&p.AutoMarkStaleImportanceCutoff, patch.AutoMarkStaleImportanceCutoff)
	applyPtr(&p.AutoMergeDuplicateMinConfidence, patch.AutoMergeDuplicateMinConfidence)
	applyPtr(&p.AutoMergeRequireContentSimilarity, patch.AutoMergeRequireContentSimilarity)
	return p
}

func applyPtr[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

// DefaultPolicy returns the starting policy for new installations.
func DefaultPolicy() Policy {
	return Policy{
		Mode:                              PolicyModeManual,
		ScheduleInterval:                  "24h",
		EventTriggers:                     []string{"session_close"},
		DuplicateSimilarity:               0.85,
		StaleDays:                         30,
		CanonicalMinConfidence:            0.80,
		CanonicalMinEvidence:              2,
		WorkingMemoryTTLDays:              14,
		WorkingDeleteImportanceCutoff:     0.6,
		AutoMergeExactDuplicates:          false,
		AutoMarkStaleBeyondDays:           30,
		AutoMarkStaleImportanceCutoff:     0.6,
		AutoRefreshFreshnessScores:        true,
		AutoDeleteExpiredWorking:          true,
		AutoMergeDuplicateMinConfidence:   0.95,
		AutoMergeRequireContentSimilarity: 0.85,
		UpdatedAt:                         time.Now().UTC(),
	}
}

// Action represents a single maintenance action proposed or applied by a steward run.
type Action struct {
	Kind       ActionKind     `json:"kind"`
	Handling   ActionHandling `json:"handling"`
	State      ActionState    `json:"state"`
	TargetIDs  []string       `json:"target_ids"`
	Title      string         `json:"title"`
	Rationale  string         `json:"rationale"`
	Evidence   []string       `json:"evidence,omitempty"`
	Confidence float64        `json:"confidence"`
}

// RunStats summarizes a steward run.
type RunStats struct {
	Scanned              int `json:"scanned"`
	DuplicatesFound      int `json:"duplicates_found"`
	ConflictsFound       int `json:"conflicts_found"`
	ContradictionsFound  int `json:"contradictions_found"`
	StaleFound           int `json:"stale_found"`
	ExpiredWorkingFound  int `json:"expired_working_found"`
	PromotionCandidates  int `json:"promotion_candidates"`
	ActionsApplied       int `json:"actions_applied"`
	ActionsPendingReview int `json:"actions_pending_review"`
}

// Report is the result of a steward run.
type Report struct {
	ID              string           `json:"id"`
	StartedAt       time.Time        `json:"started_at"`
	CompletedAt     time.Time        `json:"completed_at"`
	Scope           RunScope         `json:"scope"`
	DryRun          bool             `json:"dry_run"`
	Context         string           `json:"context,omitempty"`
	Service         string           `json:"service,omitempty"`
	Stats           RunStats         `json:"stats"`
	Actions         []Action         `json:"actions"`
	Errors          []RunError       `json:"errors,omitempty"`
	CanonicalHealth *CanonicalHealth `json:"canonical_health,omitempty"`
}

// CanonicalHealth summarizes the state of canonical knowledge entries.
type CanonicalHealth struct {
	Total       int              `json:"total"`
	Stale       int              `json:"stale"`
	Unverified  int              `json:"unverified"`
	Conflicting int              `json:"conflicting"`
	LowSupport  int              `json:"low_support"`
	Issues      []CanonicalIssue `json:"issues,omitempty"`
}

// CanonicalIssue describes a problem with a canonical entry.
type CanonicalIssue struct {
	MemoryID string `json:"memory_id"`
	Title    string `json:"title"`
	Issue    string `json:"issue"`
	Urgency  string `json:"urgency"` // high, medium, low
}

// RunError records a non-fatal error during a steward run.
type RunError struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// AuditEntry logs a single applied steward action for traceability.
type AuditEntry struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	Action     ActionKind `json:"action"`
	TargetIDs  []string   `json:"target_ids"`
	Handling   string     `json:"handling"`
	Rationale  string     `json:"rationale"`
	Evidence   []string   `json:"evidence,omitempty"`
	Confidence float64    `json:"confidence"`
	AppliedAt  time.Time  `json:"applied_at"`
	AppliedBy  string     `json:"applied_by"` // "steward_auto" | "user"
}

// Status summarizes the current stewardship state.
type Status struct {
	PolicyMode    PolicyMode `json:"policy_mode"`
	LastRun       *RunBrief  `json:"last_run,omitempty"`
	PendingReview int        `json:"pending_review"`
	NextRun       *time.Time `json:"next_scheduled_run,omitempty"`
}

// RunBrief is a compact summary of a steward run for status display.
type RunBrief struct {
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	Duration  string    `json:"duration"`
	Stats     RunStats  `json:"stats"`
}

// ValidateRunScope validates and normalizes a scope string.
func ValidateRunScope(s string) (RunScope, error) {
	switch RunScope(s) {
	case ScopeFull, ScopeDuplicates, ScopeConflicts, ScopeStale, ScopeCanonical, ScopeSemanticConflicts, ScopeWorkingTTL:
		return RunScope(s), nil
	case "":
		return ScopeFull, nil
	default:
		return "", fmt.Errorf("invalid scope %q: expected full, duplicates, conflicts, stale, canonical, or semantic_conflicts", s)
	}
}

// ValidateVerificationMethod validates a verification method string.
func ValidateVerificationMethod(s string) (VerificationMethod, error) {
	switch VerificationMethod(s) {
	case VerifyManual, VerifySourceCheck, VerifyRepoScan, VerifyAgentVerified:
		return VerificationMethod(s), nil
	case "":
		return VerifyManual, nil
	default:
		return "", fmt.Errorf("invalid verification method %q: expected manual, source_check, repo_scan, or agent_verified", s)
	}
}

// ValidateVerificationStatus validates a verification status string.
func ValidateVerificationStatus(s string) (VerificationStatus, error) {
	switch VerificationStatus(s) {
	case StatusVerified, StatusVerificationFailed, StatusNeedsUpdate:
		return VerificationStatus(s), nil
	case "":
		return StatusVerified, nil
	default:
		return "", fmt.Errorf("invalid verification status %q: expected verified, verification_failed, or needs_update", s)
	}
}

// ValidateDriftScope validates a drift scan scope string.
func ValidateDriftScope(s string) (string, error) {
	switch s {
	case "", "all", "canonical", "decisions", "runbooks":
		if s == "" {
			return "all", nil
		}
		return s, nil
	default:
		return "", fmt.Errorf("invalid drift scope %q: expected all, canonical, decisions, or runbooks", s)
	}
}
