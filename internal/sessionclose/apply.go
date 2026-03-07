package sessionclose

import (
	"context"
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

func (s *Service) executeActions(ctx context.Context, result *AnalysisResult, req AnalyzeRequest) error {
	if result == nil {
		return nil
	}

	for i := range result.Actions {
		action := &result.Actions[i]
		if action.Kind == ActionRawOnly {
			if req.SaveRaw {
				rawID, err := s.SaveRawSummary(ctx, result.Summary)
				if err != nil {
					return err
				}
				result.RawSummarySaved = rawID
				action.State = ActionStateApplied
				action.AppliedMemoryID = rawID
				action.ExecutionNote = "raw session summary saved"
			} else {
				action.State = ActionStateSkipped
				action.ExecutionNote = "save_raw=false"
			}
			continue
		}

		if !req.AutoApplyLowRisk || action.Handling != ActionHandlingAutoApply {
			action.State = ActionStateReviewRequired
			if !req.AutoApplyLowRisk && action.Handling == ActionHandlingAutoApply {
				action.ExecutionNote = "auto-apply disabled by request"
			}
			continue
		}

		if err := s.applyAction(ctx, action); err != nil {
			action.State = ActionStateReviewRequired
			action.ExecutionNote = err.Error()
		}
	}

	return nil
}

func (s *Service) applyAction(ctx context.Context, action *CandidateAction) error {
	if action == nil {
		return fmt.Errorf("action is required")
	}

	switch action.Kind {
	case ActionUpdate:
		return s.applyUpdateAction(ctx, action)
	case ActionMerge:
		return s.applyMergeAction(ctx, action)
	default:
		action.State = ActionStateReviewRequired
		action.ExecutionNote = "auto-apply policy does not support this action kind"
		return nil
	}
}

func (s *Service) applyUpdateAction(ctx context.Context, action *CandidateAction) error {
	if strings.TrimSpace(action.TargetMemoryID) == "" {
		return fmt.Errorf("target memory id is required for update")
	}
	if action.Candidate == nil {
		return fmt.Errorf("candidate memory is required for update")
	}

	target, err := s.store.Get(action.TargetMemoryID)
	if err != nil {
		return err
	}
	candidate := sanitizeKnowledgeCandidate(action.Candidate)

	updates := memory.Update{
		Tags:     mergeTags(target.Tags, candidate.Tags),
		Metadata: mergeMetadata(target.Metadata, candidate.Metadata),
	}
	if target.Context == "" && candidate.Context != "" {
		updates.Context = candidate.Context
	}
	if strings.TrimSpace(target.Title) == "" && strings.TrimSpace(candidate.Title) != "" {
		updates.Title = candidate.Title
	}
	if shouldReplaceContent(target.Content, candidate.Content) {
		updates.Content = candidate.Content
	}
	if candidate.Importance > target.Importance {
		importance := candidate.Importance
		updates.Importance = &importance
	}

	if err := s.store.Update(ctx, action.TargetMemoryID, updates); err != nil {
		return err
	}

	action.State = ActionStateApplied
	action.AppliedMemoryID = action.TargetMemoryID
	action.ExecutionNote = "near-exact update auto-applied"
	return nil
}

func (s *Service) applyMergeAction(ctx context.Context, action *CandidateAction) error {
	if strings.TrimSpace(action.TargetMemoryID) == "" {
		return fmt.Errorf("target memory id is required for merge")
	}
	if action.Candidate == nil {
		return fmt.Errorf("candidate memory is required for merge")
	}

	candidate := sanitizeKnowledgeCandidate(action.Candidate)
	candidate.ID = ""
	if err := s.store.Store(ctx, candidate); err != nil {
		return err
	}

	result, err := s.store.MergeDuplicates(ctx, action.TargetMemoryID, []string{candidate.ID})
	if err != nil {
		_ = s.store.Delete(ctx, candidate.ID)
		return err
	}

	action.State = ActionStateApplied
	action.AppliedMemoryID = result.PrimaryID
	action.ExecutionNote = fmt.Sprintf("candidate merged into %s", result.PrimaryID)
	return nil
}
