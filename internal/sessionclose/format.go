package sessionclose

import (
	"fmt"
	"strings"
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
