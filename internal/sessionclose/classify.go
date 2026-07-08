package sessionclose

import "github.com/ipiton/agent-memory-mcp/internal/memory"

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
