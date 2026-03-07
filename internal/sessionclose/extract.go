package sessionclose

import (
	"regexp"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/scoring"
)

var (
	filePathPattern    = regexp.MustCompile(`(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+(?:\.[A-Za-z0-9]+)?|[A-Za-z0-9_.-]+\.(?:go|ya?ml|tf|md|sh|json|toml|sql)`)
	serviceNamePattern = regexp.MustCompile(`(?i)\bservice[:= ]+([a-z0-9][a-z0-9_-]+)\b`)
)

type segment struct {
	title          string
	content        string
	entity         memory.EngineeringType
	status         string
	reviewRequired bool
	trace          []string
}

func extractSegments(summary memory.SessionSummary) []segment {
	lines := strings.Split(strings.ReplaceAll(summary.Summary, "\r\n", "\n"), "\n")
	segments := make([]segment, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*• \t"))
		if len(line) < 12 {
			continue
		}

		entity, trace := inferEngineeringType(line, summary.Mode)
		status := inferStatus(line)
		reviewRequired, reviewTrace := inferReviewRequired(line, summary.Mode)
		title := inferTitle(line)
		trace = append(trace, reviewTrace...)
		segments = append(segments, segment{
			title:          title,
			content:        line,
			entity:         entity,
			status:         status,
			reviewRequired: reviewRequired,
			trace:          memory.UnionStrings(trace),
		})
	}

	if len(segments) == 0 {
		entity, trace := inferEngineeringType(summary.Summary, summary.Mode)
		status := inferStatus(summary.Summary)
		reviewRequired, reviewTrace := inferReviewRequired(summary.Summary, summary.Mode)
		trace = append(trace, reviewTrace...)
		segments = append(segments, segment{
			title:          inferTitle(summary.Summary),
			content:        strings.TrimSpace(summary.Summary),
			entity:         entity,
			status:         status,
			reviewRequired: reviewRequired,
			trace:          memory.UnionStrings(trace),
		})
	}

	return segments
}

func inferEngineeringType(text string, mode memory.SessionMode) (memory.EngineeringType, []string) {
	lower := strings.ToLower(strings.TrimSpace(text))
	trace := make([]string, 0, 3)

	switch {
	case strings.HasPrefix(lower, "decision:"):
		return memory.EngineeringTypeDecision, append(trace, "matched_by_engineering_type", "decision_label")
	case strings.HasPrefix(lower, "incident:"):
		return memory.EngineeringTypeIncident, append(trace, "matched_by_engineering_type", "incident_label")
	case strings.HasPrefix(lower, "runbook:"):
		return memory.EngineeringTypeRunbook, append(trace, "matched_by_engineering_type", "runbook_label")
	case strings.HasPrefix(lower, "postmortem:"):
		return memory.EngineeringTypePostmortem, append(trace, "matched_by_engineering_type", "postmortem_label")
	case strings.HasPrefix(lower, "migration:"):
		return memory.EngineeringTypeMigrationNote, append(trace, "matched_by_engineering_type", "migration_label")
	case strings.HasPrefix(lower, "caveat:"):
		return memory.EngineeringTypeCaveat, append(trace, "matched_by_engineering_type", "caveat_label")
	case strings.HasPrefix(lower, "procedure:"):
		return memory.EngineeringTypeProcedure, append(trace, "matched_by_engineering_type", "procedure_label")
	}

	if entity, modeTrace := inferEngineeringTypeByMode(lower, mode); entity != "" {
		return entity, append(trace, modeTrace...)
	}

	switch {
	case scoring.ContainsAny(lower, "decided", "decision", "accepted", "agreed"):
		return memory.EngineeringTypeDecision, append(trace, "matched_by_engineering_type", "decision_keywords")
	case scoring.ContainsAny(lower, "incident", "outage", "sev", "degraded", "failure"):
		return memory.EngineeringTypeIncident, append(trace, "matched_by_engineering_type", "incident_keywords")
	case scoring.ContainsAny(lower, "runbook", "rollback", "restart", "verification", "verify"):
		return memory.EngineeringTypeRunbook, append(trace, "matched_by_engineering_type", "runbook_keywords")
	case scoring.ContainsAny(lower, "postmortem", "root cause", "action item", "follow-up"):
		return memory.EngineeringTypePostmortem, append(trace, "matched_by_engineering_type", "postmortem_keywords")
	case scoring.ContainsAny(lower, "migration", "cutover", "backfill", "migrate"):
		return memory.EngineeringTypeMigrationNote, append(trace, "matched_by_engineering_type", "migration_keywords")
	case scoring.ContainsAny(lower, "caveat", "workaround", "gotcha", "known issue"):
		return memory.EngineeringTypeCaveat, append(trace, "matched_by_engineering_type", "caveat_keywords")
	case scoring.ContainsAny(lower, "procedure", "playbook", "how to", "steps"):
		return memory.EngineeringTypeProcedure, append(trace, "matched_by_engineering_type", "procedure_keywords")
	}

	if !hasEngineeringSignal(lower) {
		return "", append(trace, "insufficient_engineering_signal")
	}

	policy := policyForMode(mode)
	return policy.fallbackEntity, append(trace, "session_mode_fallback:"+string(policy.mode))
}

func inferStatus(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	switch {
	case scoring.ContainsAny(lower, "accepted", "approved", "confirmed", "verified", "resolved"):
		return "accepted"
	case scoring.ContainsAny(lower, "draft", "proposed", "investigating", "todo"):
		return "draft"
	case scoring.ContainsAny(lower, "superseded", "replaced by", "merged into"):
		return "superseded"
	case scoring.ContainsAny(lower, "outdated", "deprecated", "stale"):
		return "outdated"
	default:
		return ""
	}
}

func inferReviewRequired(text string, mode memory.SessionMode) (bool, []string) {
	lower := strings.ToLower(strings.TrimSpace(text))
	if scoring.ContainsAny(lower, "not sure", "unclear", "needs review", "investigating", "todo", "verify later", "to verify") {
		return true, []string{"review_required_signal"}
	}
	policy := policyForMode(mode)
	if len(policy.extraReviewSignals) > 0 && scoring.ContainsAny(lower, policy.extraReviewSignals...) {
		return true, []string{"mode_policy_review_signal:" + string(policy.mode)}
	}
	return false, nil
}

func inferTitle(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexByte(text, ':'); idx >= 0 && idx < len(text)-1 {
		text = strings.TrimSpace(text[idx+1:])
	}
	if idx := strings.IndexAny(text, ".;"); idx > 0 {
		text = strings.TrimSpace(text[:idx])
	}
	if len(text) > 80 {
		text = text[:80] + "..."
	}
	return text
}

func inferSessionMode(summary string, tags []string) memory.SessionMode {
	summary = strings.ToLower(strings.TrimSpace(summary))
	tagText := strings.ToLower(strings.Join(tags, " "))

	switch {
	case scoring.ContainsAny(summary, "incident", "outage", "sev", "mitigated", "pager", "alert", "degraded") || scoring.ContainsAny(tagText, "incident", "sev"):
		return memory.SessionModeIncident
	case scoring.ContainsAny(summary, "migration", "cutover", "backfill", "dual-write", "schema", "data move") || scoring.ContainsAny(tagText, "migration", "cutover"):
		return memory.SessionModeMigration
	case scoring.ContainsAny(summary, "research", "compare", "evaluated", "experiment", "benchmarked") || scoring.ContainsAny(tagText, "research", "analysis"):
		return memory.SessionModeResearch
	case scoring.ContainsAny(summary, "cleanup", "deprecated", "remove dead", "tidy up", "cleanup-only") || scoring.ContainsAny(tagText, "cleanup", "refactor"):
		return memory.SessionModeCleanup
	default:
		return memory.SessionModeCoding
	}
}

func inferEngineeringTypeByMode(text string, mode memory.SessionMode) (memory.EngineeringType, []string) {
	policy := policyForMode(mode)
	for _, hint := range policy.typeHints {
		if scoring.ContainsAny(text, hint.keywords...) {
			return hint.entity, []string{"mode_policy:" + string(policy.mode), hint.trace}
		}
	}
	return "", nil
}

func policyForMode(mode memory.SessionMode) modePolicy {
	switch mode {
	case memory.SessionModeIncident:
		return modePolicy{
			mode:           memory.SessionModeIncident,
			fallbackEntity: memory.EngineeringTypeIncident,
			strictReview:   true,
			extraReviewSignals: []string{
				"temporary fix", "mitigated", "rolled back", "workaround", "follow-up", "hotfix",
			},
			typeHints: []modeTypeHint{
				{entity: memory.EngineeringTypeIncident, keywords: []string{"mitigated", "latency spike", "error spike", "alert", "pager", "degraded", "impact"}, trace: "incident_mode_priority"},
				{entity: memory.EngineeringTypeRunbook, keywords: []string{"step 1", "runbook", "playbook", "verification steps"}, trace: "incident_mode_runbook_hint"},
			},
		}
	case memory.SessionModeMigration:
		return modePolicy{
			mode:           memory.SessionModeMigration,
			fallbackEntity: memory.EngineeringTypeMigrationNote,
			strictReview:   true,
			extraReviewSignals: []string{
				"backfill pending", "manual verification", "follow-up migration", "partial cutover", "reconcile later",
			},
			typeHints: []modeTypeHint{
				{entity: memory.EngineeringTypeMigrationNote, keywords: []string{"schema", "cutover", "backfill", "dual-write", "reindex", "data move"}, trace: "migration_mode_priority"},
				{entity: memory.EngineeringTypeDecision, keywords: []string{"freeze", "migration plan", "accepted rollout window"}, trace: "migration_mode_decision_hint"},
			},
		}
	case memory.SessionModeResearch:
		return modePolicy{
			mode:           memory.SessionModeResearch,
			fallbackEntity: memory.EngineeringTypeDecision,
			typeHints: []modeTypeHint{
				{entity: memory.EngineeringTypeDecision, keywords: []string{"compare", "evaluated", "benchmarked", "experiment", "tradeoff"}, trace: "research_mode_priority"},
				{entity: memory.EngineeringTypeCaveat, keywords: []string{"limitation", "constraint", "known issue"}, trace: "research_mode_caveat_hint"},
			},
		}
	case memory.SessionModeCleanup:
		return modePolicy{
			mode:           memory.SessionModeCleanup,
			fallbackEntity: memory.EngineeringTypeCaveat,
			typeHints: []modeTypeHint{
				{entity: memory.EngineeringTypeCaveat, keywords: []string{"deprecated", "cleanup", "remove", "obsolete", "dead code"}, trace: "cleanup_mode_priority"},
				{entity: memory.EngineeringTypeProcedure, keywords: []string{"repeatable cleanup", "cleanup steps", "tidy workflow"}, trace: "cleanup_mode_procedure_hint"},
			},
		}
	default:
		return modePolicy{
			mode:           memory.SessionModeCoding,
			fallbackEntity: memory.EngineeringTypeCaveat,
			typeHints: []modeTypeHint{
				{entity: memory.EngineeringTypeProcedure, keywords: []string{"how to", "steps", "procedure"}, trace: "coding_mode_procedure_hint"},
			},
		}
	}
}

func hasEngineeringSignal(text string) bool {
	return filePathPattern.MatchString(text) || scoring.ContainsAny(
		text,
		"service",
		"deploy",
		"deployment",
		"rollout",
		"rollback",
		"helm",
		"terraform",
		"k8s",
		"kubernetes",
		"ingress",
		"hpa",
		"incident",
		"runbook",
		"procedure",
		"migration",
		"database",
		"schema",
		"config",
		"yaml",
		"sql",
		"api",
		"latency",
		"error",
		"timeout",
		"postmortem",
		"decision",
		"changed",
		"updated",
		"removed",
		"added",
	)
}

func collectEntities(segments []segment) []memory.EngineeringType {
	values := make([]memory.EngineeringType, 0, len(segments))
	for _, segment := range segments {
		if segment.entity != "" {
			values = append(values, segment.entity)
		}
	}
	return values
}

func collectTouchedServices(text string) []string {
	matches := serviceNamePattern.FindAllStringSubmatch(text, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 {
			values = append(values, strings.TrimSpace(match[1]))
		}
	}
	return values
}

func collectPaths(text string) []string {
	matches := filePathPattern.FindAllString(text, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		match = strings.TrimSpace(strings.TrimRight(match, ".,;:)]}"))
		if match == "" {
			continue
		}
		values = append(values, match)
	}
	return memory.UnionStrings(values)
}

func collectSuspectedChanges(segments []segment) []string {
	values := make([]string, 0, len(segments))
	for _, segment := range segments {
		lower := strings.ToLower(segment.content)
		if scoring.ContainsAny(lower, "changed", "updated", "migrated", "removed", "added", "rollback", "rotated") {
			values = append(values, segment.title)
		}
	}
	return values
}

func collectTopics(segments []segment) []string {
	values := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment.entity != "" {
			values = append(values, string(segment.entity))
		}
	}
	return values
}

func collectRisks(segments []segment) []string {
	values := make([]string, 0, len(segments))
	for _, segment := range segments {
		lower := strings.ToLower(segment.content)
		if scoring.ContainsAny(lower, "risk", "warning", "issue", "rollback", "outage", "degraded", "blocked") {
			values = append(values, segment.title)
		}
	}
	return values
}
