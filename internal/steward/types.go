// Package steward provides automated knowledge maintenance for the memory store.
// It detects duplicates, conflicts, stale entries, and canonical promotion candidates,
// then applies safe actions or surfaces review items.
package steward

import "time"

// RunScope controls which scanners run during a steward cycle.
type RunScope string

const (
	ScopeFull       RunScope = "full"
	ScopeDuplicates RunScope = "duplicates"
	ScopeConflicts  RunScope = "conflicts"
	ScopeStale      RunScope = "stale"
	ScopeCanonical  RunScope = "canonical"
)

// PolicyMode controls when stewardship runs execute.
type PolicyMode string

const (
	PolicyModeOff          PolicyMode = "off"
	PolicyModeManual       PolicyMode = "manual"
	PolicyModeScheduled    PolicyMode = "scheduled"
	PolicyModeEventDriven  PolicyMode = "event_driven"
)

// ActionKind describes what a steward action does.
type ActionKind string

const (
	ActionMergeDuplicates    ActionKind = "merge_duplicates"
	ActionMarkOutdated       ActionKind = "mark_outdated"
	ActionMarkStale          ActionKind = "mark_stale"
	ActionPromoteCanonical   ActionKind = "promote_canonical"
	ActionRefreshFreshness   ActionKind = "refresh_freshness"
	ActionFlagConflict       ActionKind = "flag_conflict"
)

// ActionHandling indicates whether an action can be auto-applied.
type ActionHandling string

const (
	HandlingSafeAutoApply ActionHandling = "safe_auto_apply"
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

// JobState tracks the lifecycle of a background steward job.
type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobCompleted JobState = "completed"
	JobFailed    JobState = "failed"
)

// Policy configures stewardship behavior and thresholds.
type Policy struct {
	Mode             PolicyMode `json:"mode"`
	ScheduleInterval string     `json:"schedule_interval"` // e.g. "24h"

	// Event triggers that can start a steward run.
	EventTriggers []string `json:"event_triggers,omitempty"`

	// Detection thresholds.
	DuplicateSimilarity    float64 `json:"duplicate_similarity"`     // default 0.85
	StaleDays              int     `json:"stale_days"`               // default 30
	CanonicalMinConfidence float64 `json:"canonical_min_confidence"` // default 0.80
	CanonicalMinEvidence   int     `json:"canonical_min_evidence"`   // default 2

	// Auto-apply rules — only applied when dry_run=false.
	AutoMergeExactDuplicates   bool `json:"auto_merge_exact_duplicates"`    // default false
	AutoMarkStaleBeyondDays    int  `json:"auto_mark_stale_beyond_days"`    // 0 = disabled
	AutoRefreshFreshnessScores bool `json:"auto_refresh_freshness_scores"`  // default true

	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultPolicy returns the starting policy for new installations.
func DefaultPolicy() Policy {
	return Policy{
		Mode:                       PolicyModeManual,
		ScheduleInterval:           "24h",
		EventTriggers:              []string{"session_close"},
		DuplicateSimilarity:        0.85,
		StaleDays:                  30,
		CanonicalMinConfidence:     0.80,
		CanonicalMinEvidence:       2,
		AutoMergeExactDuplicates:   false,
		AutoMarkStaleBeyondDays:    0,
		AutoRefreshFreshnessScores: true,
		UpdatedAt:                  time.Now().UTC(),
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
	StaleFound           int `json:"stale_found"`
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
	Total         int                `json:"total"`
	Stale         int                `json:"stale"`
	Unverified    int                `json:"unverified"`
	Conflicting   int                `json:"conflicting"`
	LowSupport    int                `json:"low_support"`
	Issues        []CanonicalIssue   `json:"issues,omitempty"`
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
