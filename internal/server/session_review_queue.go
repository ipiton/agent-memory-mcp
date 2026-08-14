package server

// Rendering of session-close review-queue entries. T91 M7: split out of
// session_tracker.go; moved verbatim.

import (
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
)

func reviewQueueTitle(summary memory.SessionSummary, action sessionclose.CandidateAction) string {
	base := firstNonEmpty(action.Title, string(action.Kind))
	parts := []string{"Review queue", base}
	if summary.Service != "" {
		parts = append(parts, summary.Service)
	}
	return strings.Join(parts, " / ")
}

func reviewQueueContent(action sessionclose.CandidateAction) string {
	lines := []string{
		fmt.Sprintf("Action: %s", action.Kind),
		fmt.Sprintf("Handling: %s", action.Handling),
	}
	if action.TargetTitle != "" {
		lines = append(lines, fmt.Sprintf("Target: %s", action.TargetTitle))
	} else if action.TargetMemoryID != "" {
		lines = append(lines, fmt.Sprintf("Target memory: %s", action.TargetMemoryID))
	}
	if action.Rationale != "" {
		lines = append(lines, fmt.Sprintf("Why: %s", action.Rationale))
	}
	if len(action.DecisionTrace) > 0 {
		lines = append(lines, fmt.Sprintf("Trace: %s", strings.Join(action.DecisionTrace, ", ")))
	}
	if action.Candidate != nil && strings.TrimSpace(action.Candidate.Content) != "" {
		lines = append(lines, fmt.Sprintf("Candidate: %s", truncateText(strings.TrimSpace(action.Candidate.Content), 220)))
	}
	return strings.Join(lines, "\n")
}

func reviewQueueImportance(action sessionclose.CandidateAction) float64 {
	switch action.Handling {
	case sessionclose.ActionHandlingHardReview:
		return 0.55
	case sessionclose.ActionHandlingSoftReview:
		return 0.40
	default:
		return 0.35
	}
}
