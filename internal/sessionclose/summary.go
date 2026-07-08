package sessionclose

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
