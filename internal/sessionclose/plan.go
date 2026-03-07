package sessionclose

import (
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/scoring"
)

func buildSessionDelta(summary memory.SessionSummary) (memory.SessionDelta, []*extractedCandidate) {
	segments := extractSegments(summary)

	delta := memory.SessionDelta{
		Summary:             &summary,
		ExtractedEntities:   uniqueEngineeringTypes(collectEntities(segments)...),
		TouchedServices:     uniqueStrings(append([]string{summary.Service}, collectTouchedServices(summary.Summary)...)...),
		TouchedPaths:        uniqueStrings(collectPaths(summary.Summary)...),
		SuspectedChanges:    uniqueStrings(collectSuspectedChanges(segments)...),
		InferredTopics:      uniqueStrings(append([]string{string(summary.Mode), summary.Context}, collectTopics(segments)...)...),
		Risks:               uniqueStrings(collectRisks(segments)...),
		LinkedExistingItems: nil,
	}

	candidates := make([]*extractedCandidate, 0, len(segments))
	for _, segment := range segments {
		entity := segment.entity
		if entity == "" {
			continue
		}
		storageType := memory.DefaultStorageTypeForEngineeringType(entity)
		metadata := memory.BuildEngineeringMetadata(entity, summary.Service, "", segment.status, segment.reviewRequired, map[string]string{
			memory.MetadataRecordKind:  memory.RecordKindKnowledgeItem,
			memory.MetadataDerivedFrom: memory.RecordKindSessionSummary,
			memory.MetadataSessionMode: string(summary.Mode),
		})
		candidate := &memory.Memory{
			Title:      segment.title,
			Content:    segment.content,
			Type:       storageType,
			Context:    summary.Context,
			Importance: defaultImportance(entity),
			Tags:       memory.BuildEngineeringTags(entity, summary.Service, "", segment.status, segment.reviewRequired, append(summary.Tags, "session-close")),
			Metadata:   metadata,
		}
		if err := memory.NormalizeMemoryForStore(candidate); err != nil {
			continue
		}
		candidates = append(candidates, &extractedCandidate{
			memory: candidate,
			trace:  append([]string(nil), segment.trace...),
		})
	}

	return delta, candidates
}

func (s *Service) planActions(summary memory.SessionSummary, candidates []*extractedCandidate) ([]CandidateAction, []string, error) {
	if len(candidates) == 0 {
		return nil, nil, nil
	}

	existing, err := s.store.List(memory.Filters{Context: summary.Context}, 0)
	if err != nil {
		return nil, nil, err
	}

	filteredExisting := make([]*memory.Memory, 0, len(existing))
	for _, item := range existing {
		if memory.IsSessionSummaryMemory(item) {
			continue
		}
		filteredExisting = append(filteredExisting, item)
	}

	actions := make([]CandidateAction, 0, len(candidates))
	linked := make([]string, 0)
	for _, candidate := range candidates {
		bestMatch, bestScore, matchTrace := bestExistingMatch(candidate.memory, filteredExisting)
		action := CandidateAction{
			Kind:            ActionNew,
			Title:           candidate.memory.Title,
			EngineeringType: memory.EngineeringTypeOf(candidate.memory),
			StorageType:     candidate.memory.Type,
			Confidence:      0.60,
			Rationale:       "new engineering knowledge extracted from the session summary",
			DecisionTrace:   append([]string(nil), candidate.trace...),
			Candidate:       memoryPreview(candidate.memory),
		}

		if bestMatch != nil {
			action.TargetMemoryID = bestMatch.ID
			action.TargetTitle = memory.DisplayTitle(bestMatch, 80)
			action.DecisionTrace = append(action.DecisionTrace, matchTrace...)
			linked = append(linked, bestMatch.ID)
		}

		switch {
		case bestMatch != nil && isSupersedeCandidate(candidate.memory):
			action.Kind = ActionSupersede
			action.Confidence = max(bestScore, 0.82)
			action.Rationale = "candidate looks like a replacement or stale-note update for an existing engineering item"
			action.DecisionTrace = append(action.DecisionTrace, "supersede_keyword_detected")
		case bestMatch != nil && bestScore >= 0.95:
			action.Kind = ActionUpdate
			action.Confidence = bestScore
			action.Rationale = "candidate matches an existing engineering item almost exactly"
			action.DecisionTrace = append(action.DecisionTrace, "near_exact_match")
		case bestMatch != nil && bestScore >= 0.82:
			action.Kind = ActionMerge
			action.Confidence = bestScore
			action.Rationale = "candidate overlaps strongly with an existing engineering item and should be reviewed for merge"
			action.DecisionTrace = append(action.DecisionTrace, "high_lexical_overlap")
		case shouldPromoteCanonical(candidate.memory):
			action.Kind = ActionPromoteCanonical
			action.Confidence = 0.76
			action.Rationale = "candidate looks like stable engineering knowledge that may deserve canonical promotion after review"
			action.DecisionTrace = append(action.DecisionTrace, "high_value_engineering_item")
		}

		actions = append(actions, action)
	}

	return actions, uniqueStrings(linked...), nil
}

func bestExistingMatch(candidate *memory.Memory, existing []*memory.Memory) (*memory.Memory, float64, []string) {
	if candidate == nil {
		return nil, 0, nil
	}

	bestScore := 0.0
	var best *memory.Memory
	var bestTrace []string
	for _, item := range existing {
		if item == nil {
			continue
		}

		entity := memory.EngineeringTypeOf(candidate)
		if entity != "" && memory.EngineeringTypeOf(item) != entity {
			continue
		}
		if service := strings.TrimSpace(candidate.Metadata[memory.MetadataService]); service != "" {
			existingService := strings.TrimSpace(item.Metadata[memory.MetadataService])
			if existingService != "" && !strings.EqualFold(existingService, service) {
				continue
			}
		}

		score := 0.0
		trace := make([]string, 0, 6)
		if entity != "" && memory.EngineeringTypeOf(item) == entity {
			score += 0.30
			trace = append(trace, "matched_by_engineering_type")
		}
		if candidate.Context != "" && item.Context == candidate.Context {
			score += 0.15
			trace = append(trace, "matched_by_context")
		}
		if service := strings.TrimSpace(candidate.Metadata[memory.MetadataService]); service != "" && strings.EqualFold(strings.TrimSpace(item.Metadata[memory.MetadataService]), service) {
			score += 0.20
			trace = append(trace, "matched_by_service")
		}

		titleScore := lexicalOverlap(candidate.Title, item.Title)
		contentScore := lexicalOverlap(candidate.Content, item.Content)
		if titleScore > 0 {
			score += titleScore * 0.20
		}
		if contentScore > 0 {
			score += contentScore * 0.15
		}
		if titleScore >= 0.95 || contentScore >= 0.95 {
			trace = append(trace, "exact_or_near_exact_text")
		}
		if contentScore >= 0.80 {
			trace = append(trace, "high_lexical_overlap")
		}
		if memory.LifecycleStatusOf(item) == memory.LifecycleCanonical {
			score += 0.05
			trace = append(trace, "existing_canonical")
		}
		score = min(score, 0.99)
		if score > bestScore {
			bestScore = score
			best = item
			bestTrace = trace
		}
	}
	return best, bestScore, bestTrace
}

func shouldPromoteCanonical(candidate *memory.Memory) bool {
	if candidate == nil {
		return false
	}
	entity := memory.EngineeringTypeOf(candidate)
	switch entity {
	case memory.EngineeringTypeDecision, memory.EngineeringTypeRunbook, memory.EngineeringTypeProcedure:
	default:
		return false
	}
	status := strings.ToLower(strings.TrimSpace(candidate.Metadata[memory.MetadataStatus]))
	return status == "accepted" || status == "approved" || status == "confirmed" || memory.LifecycleStatusOf(candidate) == memory.LifecycleCanonical
}

func isSupersedeCandidate(candidate *memory.Memory) bool {
	if candidate == nil {
		return false
	}
	lower := strings.ToLower(candidate.Content)
	return scoring.ContainsAny(lower, "replaced by", "superseded", "deprecated", "outdated", "stale")
}
