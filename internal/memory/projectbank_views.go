package memory

import (
	"sort"
	"strings"
)

func buildProjectBankSections(view ProjectBankView, items []*ProjectBankItem, limit int) []ProjectBankSection {
	switch view {
	case ProjectBankViewSedimentCandidates:
		candidates := collectProjectBankItems(items, limit, projectBankIsSedimentCandidate, sortProjectBankRecentItems)
		return []ProjectBankSection{
			{
				Key:         "sediment_candidates",
				Title:       "Sediment candidates",
				Description: "Pending sediment-cycle promotions awaiting review (non-trivial transitions).",
				Items:       candidates,
			},
		}
	case ProjectBankViewCanonicalOverview:
		canonical := collectProjectBankItems(items, limit, projectBankIsCanonicalItem, sortProjectBankCanonicalItems)
		sessionDeltas := collectProjectBankItems(items, limit, projectBankIsSessionDelta, sortProjectBankRecentItems)
		reviewQueue := collectProjectBankItems(items, limit, projectBankIsReviewQueueItem, sortProjectBankRecentItems)
		attention := collectProjectBankItems(items, limit, projectBankNeedsAttention, sortProjectBankAttentionItems)
		return []ProjectBankSection{
			{
				Key:         "canonical_knowledge",
				Title:       "Canonical knowledge",
				Description: "Current confirmed project knowledge promoted into the canonical layer.",
				Items:       canonical,
			},
			{
				Key:         "recent_session_deltas",
				Title:       "Recent session deltas",
				Description: "Raw session summaries that recently changed the local project context.",
				Items:       sessionDeltas,
			},
			{
				Key:         "review_queue",
				Title:       "Review queue",
				Description: "Pending review items produced by background session consolidation.",
				Items:       reviewQueue,
			},
			{
				Key:         "needs_review_or_refresh",
				Title:       "Needs review or refresh",
				Description: "Outdated, superseded, draft, or review-required knowledge that may need consolidation.",
				Items:       attention,
			},
		}
	default:
		filtered := collectProjectBankItems(items, limit, func(item *ProjectBankItem) bool {
			return projectBankMatchesView(item, view)
		}, sortProjectBankEntityItems)
		return []ProjectBankSection{
			{
				Key:         string(view),
				Title:       projectBankViewTitle(view),
				Description: projectBankViewDescription(view),
				Items:       filtered,
			},
		}
	}
}

func collectProjectBankItems(items []*ProjectBankItem, limit int, keep func(*ProjectBankItem) bool, sorter func([]*ProjectBankItem)) []*ProjectBankItem {
	filtered := make([]*ProjectBankItem, 0, len(items))
	for _, item := range items {
		if !keep(item) {
			continue
		}
		filtered = append(filtered, item)
	}
	if sorter != nil {
		sorter(filtered)
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

func projectBankMatchesView(item *ProjectBankItem, view ProjectBankView) bool {
	if item == nil || projectBankIsSessionDelta(item) || projectBankIsSessionCheckpoint(item) {
		return false
	}
	if view == ProjectBankViewReviewQueue {
		return projectBankIsReviewQueueItem(item)
	}
	if projectBankIsReviewQueueItem(item) {
		return false
	}

	switch view {
	case ProjectBankViewDecisions:
		return item.Entity == string(EngineeringTypeDecision)
	case ProjectBankViewRunbooks:
		return item.Entity == string(EngineeringTypeRunbook) || item.Entity == string(EngineeringTypeProcedure)
	case ProjectBankViewIncidents:
		return item.Entity == string(EngineeringTypeIncident) || item.Entity == string(EngineeringTypePostmortem)
	case ProjectBankViewCaveats:
		return item.Entity == string(EngineeringTypeCaveat)
	case ProjectBankViewMigrations:
		return item.Entity == string(EngineeringTypeMigrationNote)
	default:
		return false
	}
}

func projectBankIsCanonicalItem(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.Lifecycle == LifecycleCanonical || item.KnowledgeLayer == "canonical"
}

func projectBankIsSessionDelta(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.RecordKind == RecordKindSessionSummary
}

func projectBankIsSessionCheckpoint(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.RecordKind == RecordKindSessionCheckpoint
}

func projectBankIsReviewQueueItem(item *ProjectBankItem) bool {
	if item == nil {
		return false
	}
	return item.RecordKind == RecordKindReviewQueueItem && item.ReviewRequired
}

// projectBankIsSedimentCandidate reports whether the item is a pending
// sediment-cycle review-queue item (i.e. review_source == sediment_cycle,
// review_required == true). Items are routed through the same
// review_queue_item record kind as archive-sweep promotion candidates;
// filtering by the explicit tag+source combination keeps them distinct.
func projectBankIsSedimentCandidate(item *ProjectBankItem) bool {
	if item == nil || item.RecordKind != RecordKindReviewQueueItem || !item.ReviewRequired {
		return false
	}
	for _, tag := range item.Tags {
		if strings.EqualFold(strings.TrimSpace(tag), "sediment-cycle") {
			return true
		}
	}
	return false
}

func projectBankNeedsAttention(item *ProjectBankItem) bool {
	if item == nil || projectBankIsSessionDelta(item) || projectBankIsSessionCheckpoint(item) || projectBankIsReviewQueueItem(item) {
		return false
	}
	if item.ReviewRequired {
		return true
	}
	switch item.Lifecycle {
	case LifecycleDraft, LifecycleOutdated, LifecycleSuperseded:
		return true
	default:
		return false
	}
}

func sortProjectBankCanonicalItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		return projectBankCompareByFreshness(items[i], items[j])
	})
}

func sortProjectBankRecentItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		if !items[i].LastVerifiedAt.Equal(items[j].LastVerifiedAt) {
			return items[i].LastVerifiedAt.After(items[j].LastVerifiedAt)
		}
		return strings.ToLower(items[i].Title) < strings.ToLower(items[j].Title)
	})
}

func sortProjectBankAttentionItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		leftRank := projectBankAttentionRank(items[i])
		rightRank := projectBankAttentionRank(items[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return projectBankCompareByFreshness(items[i], items[j])
	})
}

func sortProjectBankEntityItems(items []*ProjectBankItem) {
	sort.Slice(items, func(i, j int) bool {
		leftRank := projectBankLifecycleRank(items[i])
		rightRank := projectBankLifecycleRank(items[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return projectBankCompareByFreshness(items[i], items[j])
	})
}

func projectBankAttentionRank(item *ProjectBankItem) int {
	if item == nil {
		return 99
	}
	if item.ReviewRequired {
		return 0
	}
	switch item.Lifecycle {
	case LifecycleDraft:
		return 1
	case LifecycleOutdated:
		return 2
	case LifecycleSuperseded:
		return 3
	default:
		return 4
	}
}

func projectBankLifecycleRank(item *ProjectBankItem) int {
	if item == nil {
		return 99
	}
	switch item.Lifecycle {
	case LifecycleCanonical:
		return 0
	case LifecycleActive:
		return 1
	case "":
		return 2
	case LifecycleDraft:
		return 3
	case LifecycleOutdated:
		return 4
	case LifecycleSuperseded:
		return 5
	default:
		return 6
	}
}

func projectBankCompareByFreshness(left *ProjectBankItem, right *ProjectBankItem) bool {
	if left == nil || right == nil {
		return left != nil
	}
	if !left.LastVerifiedAt.Equal(right.LastVerifiedAt) {
		return left.LastVerifiedAt.After(right.LastVerifiedAt)
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	if left.Importance != right.Importance {
		return left.Importance > right.Importance
	}
	return strings.ToLower(left.Title) < strings.ToLower(right.Title)
}

func countProjectBankEntities(items []*ProjectBankItem) map[string]int {
	counts := make(map[string]int)
	for _, item := range items {
		if item == nil || projectBankIsSessionDelta(item) || projectBankIsSessionCheckpoint(item) || projectBankIsReviewQueueItem(item) {
			continue
		}
		switch {
		case item.Entity == string(EngineeringTypeDecision):
			counts["decisions"]++
		case item.Entity == string(EngineeringTypeRunbook) || item.Entity == string(EngineeringTypeProcedure):
			counts["runbooks"]++
		case item.Entity == string(EngineeringTypeIncident) || item.Entity == string(EngineeringTypePostmortem):
			counts["incidents"]++
		case item.Entity == string(EngineeringTypeCaveat):
			counts["caveats"]++
		case item.Entity == string(EngineeringTypeMigrationNote):
			counts["migrations"]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func uniqueProjectBankCount(sections []ProjectBankSection) int {
	seen := make(map[string]struct{})
	for _, section := range sections {
		for _, item := range section.Items {
			if item == nil || item.ID == "" {
				continue
			}
			seen[item.ID] = struct{}{}
		}
	}
	return len(seen)
}

func projectBankViewTitle(view ProjectBankView) string {
	switch view {
	case ProjectBankViewCanonicalOverview:
		return "Canonical overview"
	case ProjectBankViewDecisions:
		return "Decision bank"
	case ProjectBankViewRunbooks:
		return "Runbook bank"
	case ProjectBankViewIncidents:
		return "Incident bank"
	case ProjectBankViewCaveats:
		return "Caveat bank"
	case ProjectBankViewMigrations:
		return "Migration bank"
	case ProjectBankViewReviewQueue:
		return "Review queue"
	case ProjectBankViewSedimentCandidates:
		return "Sediment candidates"
	default:
		return "Project bank"
	}
}

func projectBankViewDescription(view ProjectBankView) string {
	switch view {
	case ProjectBankViewDecisions:
		return "Current engineering decisions across raw and canonical knowledge."
	case ProjectBankViewRunbooks:
		return "Operational runbooks and reusable procedures."
	case ProjectBankViewIncidents:
		return "Incident history and postmortem knowledge."
	case ProjectBankViewCaveats:
		return "Known caveats, gotchas, and workarounds."
	case ProjectBankViewMigrations:
		return "Migration notes, cutover guidance, and rollout constraints."
	case ProjectBankViewReviewQueue:
		return "Pending review items from background session consolidation."
	case ProjectBankViewSedimentCandidates:
		return "Pending sediment-cycle promotions awaiting review."
	default:
		return ""
	}
}
