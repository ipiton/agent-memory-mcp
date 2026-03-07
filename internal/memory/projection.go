package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/scoring"
	"github.com/ipiton/agent-memory-mcp/internal/trust"
)

func conflictSubject(m *Memory) string {
	if m == nil {
		return ""
	}
	base := strings.TrimSpace(m.Title)
	if base == "" {
		base = strings.TrimSpace(m.Content)
	}
	tokens := scoring.TokenizeWords(base)
	if len(tokens) > 10 {
		tokens = tokens[:10]
	}
	return strings.Join(tokens, " ")
}

func conflictGroupKey(m *Memory) string {
	return strings.Join([]string{
		memoryEntity(m),
		memoryService(m),
		strings.TrimSpace(m.Context),
		conflictSubject(m),
	}, "|")
}

func suggestedAction(reason string) string {
	switch reason {
	case "multiple_canonical":
		return "review canonical entries and demote or merge one of them"
	case "status_conflict":
		return "review statuses, then mark outdated or promote one entry to canonical"
	default:
		return "merge duplicates or archive older notes"
	}
}

func (ms *Store) ConflictsReport(ctx context.Context, filters Filters, limit int) ([]ConflictReportItem, error) {
	memories, err := ms.List(ctx, filters, 0)
	if err != nil {
		return nil, err
	}

	groups := make(map[string][]*Memory)
	for _, mem := range memories {
		if isArchivedMemory(mem) {
			continue
		}
		subject := conflictSubject(mem)
		if subject == "" {
			continue
		}
		key := conflictGroupKey(mem)
		groups[key] = append(groups[key], mem)
	}

	reports := make([]ConflictReportItem, 0)
	for key, group := range groups {
		if len(group) < 2 {
			continue
		}

		statusSet := make(map[string]struct{})
		titleSet := make(map[string]struct{})
		tagSet := make(map[string]struct{})
		titles := make([]string, 0, len(group))
		statuses := make([]string, 0, len(group))
		ids := make([]string, 0, len(group))
		canonicalCount := 0
		for _, mem := range group {
			ids = append(ids, mem.ID)
			titles = append(titles, strings.TrimSpace(mem.Title))
			if status := memoryStatus(mem); status != "" {
				statusSet[status] = struct{}{}
				statuses = append(statuses, status)
			}
			titleSet[conflictSubject(mem)] = struct{}{}
			for _, tag := range mem.Tags {
				tagSet[tag] = struct{}{}
			}
			if IsCanonicalMemory(mem) {
				canonicalCount++
			}
		}

		reason := ""
		switch {
		case canonicalCount > 1:
			reason = "multiple_canonical"
		case len(statusSet) > 1:
			reason = "status_conflict"
		case len(titleSet) == 1:
			reason = "duplicate_candidates"
		default:
			continue
		}

		report := ConflictReportItem{
			GroupKey:        key,
			Entity:          memoryEntity(group[0]),
			Service:         memoryService(group[0]),
			Context:         strings.TrimSpace(group[0].Context),
			Subject:         conflictSubject(group[0]),
			Reason:          reason,
			SuggestedAction: suggestedAction(reason),
			MemoryIDs:       ids,
			Titles:          titles,
			Statuses:        unionStrings(statuses),
			Tags:            unionStrings(mapKeysToSlice(tagSet)),
		}
		reports = append(reports, report)
	}

	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Reason == reports[j].Reason {
			return reports[i].GroupKey < reports[j].GroupKey
		}
		return reports[i].Reason < reports[j].Reason
	})
	if limit > 0 && len(reports) > limit {
		reports = reports[:limit]
	}
	return reports, nil
}

func mapKeysToSlice(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	return result
}

// ToCanonicalKnowledge projects a Memory into a CanonicalKnowledge entry.
// If trust is nil, it is derived from the memory's metadata.
func ToCanonicalKnowledge(m *Memory, tm *trust.Metadata) *CanonicalKnowledge {
	if m == nil {
		return nil
	}
	entryTrust := tm
	if entryTrust == nil {
		entryTrust = deriveTrustMetadata(m, time.Now())
	}
	summary := strings.TrimSpace(m.Content)
	if len(summary) > 280 {
		summary = summary[:280] + "..."
	}
	return &CanonicalKnowledge{
		ID:             m.ID,
		SourceMemoryID: m.ID,
		Title:          strings.TrimSpace(DisplayTitle(m, 80)),
		Summary:        summary,
		Entity:         memoryEntity(m),
		Context:        strings.TrimSpace(m.Context),
		Service:        memoryService(m),
		Owner:          strings.TrimSpace(entryTrust.Owner),
		Status:         memoryStatus(m),
		Tags:           append([]string(nil), m.Tags...),
		Importance:     m.Importance,
		LastVerifiedAt: entryTrust.LastVerifiedAt,
		UpdatedAt:      m.UpdatedAt,
		Trust:          entryTrust,
	}
}

// ListCanonical returns canonical knowledge entries projected from canonical memories.
func (ms *Store) ListCanonical(ctx context.Context, filters Filters, limit int) ([]*CanonicalKnowledge, error) {
	memories, err := ms.List(ctx, filters, 0)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	result := make([]*CanonicalKnowledge, 0)
	for _, mem := range memories {
		if !IsCanonicalMemory(mem) {
			continue
		}
		result = append(result, ToCanonicalKnowledge(mem, deriveTrustMetadata(mem, now)))
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// RecallCanonical returns only canonical knowledge entries for a query.
func (ms *Store) RecallCanonical(ctx context.Context, query string, filters Filters, limit int) ([]*CanonicalSearchResult, error) {
	results, err := ms.Recall(ctx, query, filters, 0)
	if err != nil {
		return nil, err
	}

	canonical := make([]*CanonicalSearchResult, 0)
	for _, result := range results {
		if result == nil || result.Memory == nil || !IsCanonicalMemory(result.Memory) {
			continue
		}
		canonical = append(canonical, &CanonicalSearchResult{
			Entry: ToCanonicalKnowledge(result.Memory, result.Trust),
			Score: result.Score,
		})
		if limit > 0 && len(canonical) >= limit {
			break
		}
	}
	return canonical, nil
}
