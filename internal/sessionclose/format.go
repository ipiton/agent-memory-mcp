package sessionclose

import (
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func FormatAnalysis(result *AnalysisResult) string {
	if result == nil {
		return "Session analysis unavailable."
	}

	var buf strings.Builder
	state := "dry-run"
	if !result.DryRun {
		state = "write-enabled"
	}
	fmt.Fprintf(&buf, "Session analysis (%s)\n", state)
	if result.Summary.Mode != "" {
		fmt.Fprintf(&buf, "Mode: %s\n", result.Summary.Mode)
	}
	if result.Summary.Context != "" {
		fmt.Fprintf(&buf, "Context: %s\n", result.Summary.Context)
	}
	if result.Summary.Service != "" {
		fmt.Fprintf(&buf, "Service: %s\n", result.Summary.Service)
	}
	buf.WriteString("\n")

	if len(result.Delta.ExtractedEntities) > 0 {
		values := make([]string, 0, len(result.Delta.ExtractedEntities))
		for _, value := range result.Delta.ExtractedEntities {
			values = append(values, string(value))
		}
		fmt.Fprintf(&buf, "Extracted entities: %s\n", strings.Join(values, ", "))
	}
	if len(result.Delta.TouchedServices) > 0 {
		fmt.Fprintf(&buf, "Touched services: %s\n", strings.Join(result.Delta.TouchedServices, ", "))
	}
	if len(result.Delta.TouchedPaths) > 0 {
		fmt.Fprintf(&buf, "Touched paths: %s\n", strings.Join(result.Delta.TouchedPaths, ", "))
	}
	if len(result.Delta.Risks) > 0 {
		fmt.Fprintf(&buf, "Risks: %s\n", strings.Join(result.Delta.Risks, " | "))
	}
	buf.WriteString("\n")

	if len(result.ActionCounts) > 0 {
		buf.WriteString("Planned actions:\n")
		order := []ActionKind{ActionNew, ActionUpdate, ActionMerge, ActionSupersede, ActionPromoteCanonical, ActionRawOnly}
		for _, kind := range order {
			if count := result.ActionCounts[kind]; count > 0 {
				fmt.Fprintf(&buf, "- %s: %d\n", kind, count)
			}
		}
		buf.WriteString("\n")
	}
	if len(result.HandlingCounts) > 0 {
		buf.WriteString("Handling:\n")
		order := []ActionHandling{ActionHandlingAutoApply, ActionHandlingSoftReview, ActionHandlingHardReview}
		for _, handling := range order {
			if count := result.HandlingCounts[handling]; count > 0 {
				fmt.Fprintf(&buf, "- %s: %d\n", handling, count)
			}
		}
		buf.WriteString("\n")
	}
	if hasStats(result.Stats) {
		buf.WriteString("Summary:\n")
		for _, line := range formatStatsLines(result.Stats) {
			fmt.Fprintf(&buf, "- %s\n", line)
		}
		buf.WriteString("\n")
	}
	if len(result.StateCounts) > 0 {
		buf.WriteString("Execution state:\n")
		order := []ActionState{ActionStateApplied, ActionStateReviewRequired, ActionStatePlanned, ActionStateSkipped}
		for _, state := range order {
			if count := result.StateCounts[state]; count > 0 {
				fmt.Fprintf(&buf, "- %s: %d\n", state, count)
			}
		}
		buf.WriteString("\n")
	}
	if hasReviewSummary(result.Review) {
		buf.WriteString("Review summary:\n")
		for _, line := range formatReviewLines(result.Review) {
			fmt.Fprintf(&buf, "- %s\n", line)
		}
		buf.WriteString("\n")
	}
	if len(result.AvailableActions) > 0 {
		buf.WriteString("Next actions:\n")
		for _, action := range result.AvailableActions {
			if action.Enabled {
				fmt.Fprintf(&buf, "- %s via %s: %s\n", action.Key, action.Tool, action.Description)
				continue
			}
			fmt.Fprintf(&buf, "- %s: %s\n", action.Key, action.Description)
		}
		buf.WriteString("\n")
	}

	if len(result.Actions) == 0 {
		buf.WriteString("No candidate actions generated.")
	} else {
		for i, action := range result.Actions {
			fmt.Fprintf(&buf, "%d. %s", i+1, strings.ToUpper(string(action.Kind)))
			if action.Title != "" {
				fmt.Fprintf(&buf, " %s", action.Title)
			}
			buf.WriteString("\n")
			if action.EngineeringType != "" {
				fmt.Fprintf(&buf, "   Engineering type: %s\n", action.EngineeringType)
			}
			if action.StorageType != "" {
				fmt.Fprintf(&buf, "   Storage type: %s\n", action.StorageType)
			}
			if action.Handling != "" {
				fmt.Fprintf(&buf, "   Handling: %s\n", action.Handling)
			}
			if action.State != "" {
				fmt.Fprintf(&buf, "   State: %s\n", action.State)
			}
			if action.TargetMemoryID != "" {
				fmt.Fprintf(&buf, "   Target: %s", action.TargetMemoryID)
				if action.TargetTitle != "" {
					fmt.Fprintf(&buf, " (%s)", action.TargetTitle)
				}
				buf.WriteString("\n")
			}
			if action.AppliedMemoryID != "" {
				fmt.Fprintf(&buf, "   Applied memory: %s\n", action.AppliedMemoryID)
			}
			if action.Confidence > 0 {
				fmt.Fprintf(&buf, "   Confidence: %.2f\n", action.Confidence)
			}
			if action.Rationale != "" {
				fmt.Fprintf(&buf, "   Why: %s\n", action.Rationale)
			}
			if action.ExecutionNote != "" {
				fmt.Fprintf(&buf, "   Execution: %s\n", action.ExecutionNote)
			}
			if len(action.DecisionTrace) > 0 {
				fmt.Fprintf(&buf, "   Trace: %s\n", strings.Join(action.DecisionTrace, ", "))
			}
		}
	}

	if result.RawSummarySaved != "" {
		fmt.Fprintf(&buf, "\nRaw summary saved as memory %s\n", result.RawSummarySaved)
	}

	return strings.TrimSpace(buf.String())
}

func countActions(actions []CandidateAction) map[ActionKind]int {
	if len(actions) == 0 {
		return nil
	}
	counts := make(map[ActionKind]int)
	for _, action := range actions {
		counts[action.Kind]++
	}
	return counts
}

func countHandling(actions []CandidateAction) map[ActionHandling]int {
	if len(actions) == 0 {
		return nil
	}
	counts := make(map[ActionHandling]int)
	for _, action := range actions {
		if action.Handling == "" {
			continue
		}
		counts[action.Handling]++
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func annotateHandling(actions []CandidateAction, mode memory.SessionMode) []CandidateAction {
	for i := range actions {
		handling, modeTrace := classifyActionHandling(actions[i], mode)
		actions[i].Handling = handling
		if len(modeTrace) > 0 {
			actions[i].DecisionTrace = memory.UnionStrings(append(actions[i].DecisionTrace, modeTrace...))
		}
	}
	return actions
}

func initializeActionStates(actions []CandidateAction) []CandidateAction {
	for i := range actions {
		actions[i].State = ActionStatePlanned
	}
	return actions
}

func classifyActionHandling(action CandidateAction, mode memory.SessionMode) (ActionHandling, []string) {
	switch action.Kind {
	case ActionRawOnly:
		return ActionHandlingAutoApply, nil
	case ActionPromoteCanonical, ActionSupersede:
		return ActionHandlingHardReview, nil
	}
	if action.Candidate != nil && memory.RequiresReview(action.Candidate) {
		return ActionHandlingHardReview, nil
	}

	policy := policyForMode(mode)
	if policy.strictReview {
		switch action.Kind {
		case ActionUpdate, ActionMerge:
			return ActionHandlingHardReview, []string{"mode_policy:" + string(policy.mode), "strict_review_mode"}
		case ActionNew:
			return ActionHandlingSoftReview, []string{"mode_policy:" + string(policy.mode), "strict_review_mode"}
		}
	}

	if action.Kind == ActionUpdate && action.Confidence >= 0.95 {
		return ActionHandlingAutoApply, nil
	}
	if action.Kind == ActionMerge && action.Confidence >= 0.95 && containsTrace(action.DecisionTrace, "exact_or_near_exact_text") {
		return ActionHandlingAutoApply, nil
	}
	if action.Confidence > 0 && action.Confidence < 0.70 {
		return ActionHandlingHardReview, nil
	}
	return ActionHandlingSoftReview, nil
}

func countStates(actions []CandidateAction) map[ActionState]int {
	if len(actions) == 0 {
		return nil
	}
	counts := make(map[ActionState]int)
	for _, action := range actions {
		if action.State == "" {
			continue
		}
		counts[action.State]++
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func buildConsolidationStats(result *AnalysisResult) ConsolidationStats {
	if result == nil {
		return ConsolidationStats{}
	}

	stats := ConsolidationStats{
		LinkedCount: len(result.Delta.LinkedExistingItems),
	}
	for _, action := range result.Actions {
		switch action.Kind {
		case ActionNew:
			stats.NewCount++
		case ActionUpdate:
			stats.UpdatedCount++
		case ActionMerge:
			stats.MergedCount++
		case ActionSupersede:
			stats.SupersededCount++
			stats.OutdatedCount++
		case ActionPromoteCanonical:
			stats.CanonicalCount++
		case ActionRawOnly:
			stats.RawOnlyCount++
		}

		switch action.State {
		case ActionStateApplied:
			stats.AppliedCount++
		case ActionStatePlanned:
			stats.PlannedCount++
		case ActionStateSkipped:
			stats.SkippedCount++
		}

		if isReviewItem(action, result.DryRun) {
			stats.ReviewItemCount++
		}
	}
	return stats
}

func buildReviewSummary(result *AnalysisResult) ReviewSummary {
	if result == nil {
		return ReviewSummary{}
	}

	summary := ReviewSummary{
		LinkedCount: len(result.Delta.LinkedExistingItems),
	}
	for _, action := range result.Actions {
		if action.State == ActionStateApplied {
			summary.AppliedCount++
		}
		if !isReviewItem(action, result.DryRun) {
			continue
		}
		summary.PendingCount++
		switch action.Handling {
		case ActionHandlingHardReview:
			summary.HardCount++
		case ActionHandlingSoftReview:
			summary.SoftCount++
		}
	}
	return summary
}

func buildAvailableActions(result *AnalysisResult) []AvailableAction {
	if result == nil {
		return nil
	}

	canApplyPlan := hasKnowledgeActions(result.Actions)
	actions := []AvailableAction{
		{
			Key:         "accept_all",
			Tool:        "accept_session_changes",
			Description: "persist raw summary and apply the session plan under the current safety policy",
			Enabled:     canApplyPlan,
		},
		{
			Key:         "review_changes",
			Tool:        "review_session_changes",
			Description: "show only the pending review items with rationale and decision trace",
			Enabled:     result.Stats.ReviewItemCount > 0,
		},
		{
			Key:         "save_raw_only",
			Tool:        "analyze_session",
			Description: "persist only the raw session summary and leave project knowledge untouched",
			Enabled:     result.RawSummarySaved == "",
		},
	}
	if result.RawSummarySaved != "" {
		actions[2].Description = "raw session summary already saved for this run"
	}
	return actions
}

func isReviewItem(action CandidateAction, dryRun bool) bool {
	if action.Kind == ActionRawOnly {
		return false
	}
	if action.State == ActionStateReviewRequired {
		return true
	}
	if dryRun {
		return action.Handling != ActionHandlingAutoApply
	}
	return false
}

func hasKnowledgeActions(actions []CandidateAction) bool {
	for _, action := range actions {
		if action.Kind != ActionRawOnly {
			return true
		}
	}
	return false
}

func hasStats(stats ConsolidationStats) bool {
	return stats.NewCount > 0 ||
		stats.UpdatedCount > 0 ||
		stats.MergedCount > 0 ||
		stats.SupersededCount > 0 ||
		stats.CanonicalCount > 0 ||
		stats.RawOnlyCount > 0 ||
		stats.LinkedCount > 0 ||
		stats.AppliedCount > 0 ||
		stats.PlannedCount > 0 ||
		stats.SkippedCount > 0 ||
		stats.OutdatedCount > 0 ||
		stats.ReviewItemCount > 0
}

func formatStatsLines(stats ConsolidationStats) []string {
	lines := make([]string, 0, 8)
	if stats.NewCount > 0 {
		lines = append(lines, fmt.Sprintf("new: %d", stats.NewCount))
	}
	if stats.UpdatedCount > 0 {
		lines = append(lines, fmt.Sprintf("updated: %d", stats.UpdatedCount))
	}
	if stats.MergedCount > 0 {
		lines = append(lines, fmt.Sprintf("merged: %d", stats.MergedCount))
	}
	if stats.SupersededCount > 0 {
		lines = append(lines, fmt.Sprintf("superseded: %d", stats.SupersededCount))
	}
	if stats.CanonicalCount > 0 {
		lines = append(lines, fmt.Sprintf("canonical promotions: %d", stats.CanonicalCount))
	}
	if stats.RawOnlyCount > 0 {
		lines = append(lines, fmt.Sprintf("raw summaries: %d", stats.RawOnlyCount))
	}
	if stats.LinkedCount > 0 {
		lines = append(lines, fmt.Sprintf("linked existing items: %d", stats.LinkedCount))
	}
	if stats.ReviewItemCount > 0 {
		lines = append(lines, fmt.Sprintf("pending review items: %d", stats.ReviewItemCount))
	}
	return lines
}

func hasReviewSummary(summary ReviewSummary) bool {
	return summary.PendingCount > 0 || summary.SoftCount > 0 || summary.HardCount > 0 || summary.AppliedCount > 0 || summary.LinkedCount > 0
}

func formatReviewLines(summary ReviewSummary) []string {
	lines := make([]string, 0, 5)
	if summary.PendingCount > 0 {
		lines = append(lines, fmt.Sprintf("pending: %d", summary.PendingCount))
	}
	if summary.SoftCount > 0 {
		lines = append(lines, fmt.Sprintf("soft review: %d", summary.SoftCount))
	}
	if summary.HardCount > 0 {
		lines = append(lines, fmt.Sprintf("hard review: %d", summary.HardCount))
	}
	if summary.AppliedCount > 0 {
		lines = append(lines, fmt.Sprintf("already applied: %d", summary.AppliedCount))
	}
	if summary.LinkedCount > 0 {
		lines = append(lines, fmt.Sprintf("linked existing items: %d", summary.LinkedCount))
	}
	return lines
}
