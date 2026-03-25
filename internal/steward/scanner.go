package steward

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// ScanResult collects actions from all scanners in a single run.
type ScanResult struct {
	Scanned         int
	Actions         []Action
	Errors          []RunError
	CanonicalHealth *CanonicalHealth
}

// RunScanners executes the requested scans and returns combined results.
func RunScanners(ctx context.Context, store *memory.Store, policy Policy, scope RunScope, memContext, service string) *ScanResult {
	result := &ScanResult{}

	// Load all non-archived memories once.
	memories, err := store.List(ctx, memory.Filters{Context: memContext}, 0)
	if err != nil {
		result.Errors = append(result.Errors, RunError{Phase: "list", Message: fmt.Sprintf("failed to list memories: %v", err)})
		return result
	}

	// Filter by service if specified and exclude archived.
	var active []*memory.Memory
	for _, m := range memories {
		if memory.IsArchivedMemory(m) {
			continue
		}
		if service != "" && memory.MemoryService(m) != service {
			continue
		}
		active = append(active, m)
	}
	result.Scanned = len(active)

	now := time.Now().UTC()

	switch scope {
	case ScopeFull:
		scanDuplicates(active, policy, result)
		scanConflicts(active, result)
		scanStale(active, policy, now, result)
		scanCanonicalCandidates(active, policy, result)
	case ScopeDuplicates:
		scanDuplicates(active, policy, result)
	case ScopeConflicts:
		scanConflicts(active, result)
	case ScopeStale:
		scanStale(active, policy, now, result)
	case ScopeCanonical:
		scanCanonicalCandidates(active, policy, result)
		result.CanonicalHealth = scanCanonicalHealth(active, policy, now)
	}

	// Include canonical health for full scan as well.
	if scope == ScopeFull {
		result.CanonicalHealth = scanCanonicalHealth(active, policy, now)
	}

	return result
}

// scanDuplicates finds groups of memories with the same subject that are likely duplicates.
func scanDuplicates(memories []*memory.Memory, policy Policy, result *ScanResult) {
	type group struct {
		key     string
		members []*memory.Memory
	}

	groups := make(map[string]*group)
	for _, m := range memories {
		key := duplicateGroupKey(m)
		if key == "" {
			continue
		}
		g, ok := groups[key]
		if !ok {
			g = &group{key: key}
			groups[key] = g
		}
		g.members = append(g.members, m)
	}

	for _, g := range groups {
		if len(g.members) < 2 {
			continue
		}

		ids := make([]string, len(g.members))
		for i, m := range g.members {
			ids[i] = m.ID
		}

		title := g.members[0].Title
		if title == "" {
			title = truncate(g.members[0].Content, 60)
		}

		result.Actions = append(result.Actions, Action{
			Kind:       ActionMergeDuplicates,
			Handling:   HandlingReviewRequired,
			State:      StatePlanned,
			TargetIDs:  ids,
			Title:      fmt.Sprintf("Duplicate group: %s (%d entries)", title, len(g.members)),
			Rationale:  fmt.Sprintf("Memories share the same entity/service/context/subject key: %s", g.key),
			Evidence:   []string{fmt.Sprintf("group_size=%d", len(g.members))},
			Confidence: 0.75,
		})
	}
}

// scanConflicts finds memories with conflicting statuses or multiple canonical entries.
func scanConflicts(memories []*memory.Memory, result *ScanResult) {
	type groupInfo struct {
		members        []*memory.Memory
		statuses       map[string]struct{}
		canonicalCount int
	}

	groups := make(map[string]*groupInfo)
	for _, m := range memories {
		key := conflictGroupKey(m)
		if key == "" {
			continue
		}
		g, ok := groups[key]
		if !ok {
			g = &groupInfo{statuses: make(map[string]struct{})}
			groups[key] = g
		}
		g.members = append(g.members, m)
		if s := metadataStatus(m); s != "" {
			g.statuses[s] = struct{}{}
		}
		if memory.IsCanonicalMemory(m) {
			g.canonicalCount++
		}
	}

	for key, g := range groups {
		if len(g.members) < 2 {
			continue
		}

		var reason string
		var kind ActionKind
		switch {
		case g.canonicalCount > 1:
			reason = "multiple canonical entries for the same subject"
			kind = ActionFlagConflict
		case len(g.statuses) > 1:
			reason = "conflicting lifecycle statuses within the same group"
			kind = ActionFlagConflict
		default:
			continue
		}

		ids := make([]string, len(g.members))
		for i, m := range g.members {
			ids[i] = m.ID
		}

		title := g.members[0].Title
		if title == "" {
			title = truncate(g.members[0].Content, 60)
		}

		result.Actions = append(result.Actions, Action{
			Kind:       kind,
			Handling:   HandlingReviewRequired,
			State:      StatePlanned,
			TargetIDs:  ids,
			Title:      fmt.Sprintf("Conflict: %s", title),
			Rationale:  fmt.Sprintf("%s (group key: %s)", reason, key),
			Evidence:   []string{fmt.Sprintf("canonical_count=%d, status_count=%d", g.canonicalCount, len(g.statuses))},
			Confidence: 0.80,
		})
	}
}

// scanStale finds memories that haven't been verified within the configured stale threshold.
func scanStale(memories []*memory.Memory, policy Policy, now time.Time, result *ScanResult) {
	if policy.StaleDays <= 0 {
		return
	}
	threshold := now.AddDate(0, 0, -policy.StaleDays)

	for _, m := range memories {
		verified := memory.LastVerifiedAt(m)
		if verified.After(threshold) {
			continue
		}

		handling := HandlingReviewRequired
		if policy.AutoMarkStaleBeyondDays > 0 {
			staleSince := now.Sub(verified)
			autoThreshold := time.Duration(policy.AutoMarkStaleBeyondDays) * 24 * time.Hour
			if staleSince >= autoThreshold {
				handling = HandlingSafeAutoApply
			}
		}

		title := m.Title
		if title == "" {
			title = truncate(m.Content, 60)
		}

		daysSince := int(now.Sub(verified).Hours() / 24)
		result.Actions = append(result.Actions, Action{
			Kind:       ActionMarkStale,
			Handling:   handling,
			State:      StatePlanned,
			TargetIDs:  []string{m.ID},
			Title:      fmt.Sprintf("Stale: %s", title),
			Rationale:  fmt.Sprintf("Last verified %d days ago (threshold: %d days)", daysSince, policy.StaleDays),
			Evidence:   []string{fmt.Sprintf("last_verified=%s", verified.Format(time.DateOnly))},
			Confidence: 0.70,
		})
	}
}

// scanCanonicalCandidates finds memories that could be promoted to canonical.
func scanCanonicalCandidates(memories []*memory.Memory, policy Policy, result *ScanResult) {
	// Gather existing canonical subjects to avoid promoting duplicates.
	canonicalSubjects := make(map[string]struct{})
	for _, m := range memories {
		if memory.IsCanonicalMemory(m) {
			canonicalSubjects[subjectKey(m)] = struct{}{}
		}
	}

	for _, m := range memories {
		if memory.IsCanonicalMemory(m) {
			continue
		}
		if memory.RequiresReview(m) {
			continue
		}
		if m.Importance < policy.CanonicalMinConfidence {
			continue
		}

		// Skip if there's already a canonical entry for this subject.
		if _, exists := canonicalSubjects[subjectKey(m)]; exists {
			continue
		}

		// Must be a recognized engineering type.
		entity := memory.EngineeringTypeOf(m)
		if entity == "" {
			continue
		}

		// Must be active lifecycle.
		lifecycle := memory.LifecycleStatusOf(m)
		if lifecycle != memory.LifecycleActive && lifecycle != memory.LifecycleDraft {
			continue
		}

		title := m.Title
		if title == "" {
			title = truncate(m.Content, 60)
		}

		result.Actions = append(result.Actions, Action{
			Kind:       ActionPromoteCanonical,
			Handling:   HandlingReviewRequired,
			State:      StatePlanned,
			TargetIDs:  []string{m.ID},
			Title:      fmt.Sprintf("Promote: %s", title),
			Rationale:  fmt.Sprintf("High-importance %s (%.2f) with active lifecycle, no existing canonical", entity, m.Importance),
			Evidence:   []string{fmt.Sprintf("entity=%s, importance=%.2f, lifecycle=%s", entity, m.Importance, lifecycle)},
			Confidence: 0.65,
		})
	}
}

// scanCanonicalHealth produces a health summary of all canonical entries.
func scanCanonicalHealth(memories []*memory.Memory, policy Policy, now time.Time) *CanonicalHealth {
	health := &CanonicalHealth{}

	staleDays := policy.StaleDays
	if staleDays <= 0 {
		staleDays = 30
	}
	staleThreshold := now.AddDate(0, 0, -staleDays*2) // 2x stale for canonical

	// Track canonical subjects for conflict detection.
	subjectCounts := make(map[string]int)

	for _, m := range memories {
		if !memory.IsCanonicalMemory(m) {
			continue
		}
		health.Total++

		title := m.Title
		if title == "" {
			title = truncate(m.Content, 60)
		}

		verified := memory.LastVerifiedAt(m)
		vStatus := verificationStatusOf(m)

		// Stale canonical.
		if verified.Before(staleThreshold) {
			health.Stale++
			health.Issues = append(health.Issues, CanonicalIssue{
				MemoryID: m.ID,
				Title:    title,
				Issue:    fmt.Sprintf("Not verified in %d days (threshold: %d)", int(now.Sub(verified).Hours()/24), staleDays*2),
				Urgency:  "high",
			})
		}

		// Unverified canonical.
		if vStatus == StatusUnverified {
			health.Unverified++
			health.Issues = append(health.Issues, CanonicalIssue{
				MemoryID: m.ID,
				Title:    title,
				Issue:    "Promoted to canonical but never explicitly verified",
				Urgency:  "medium",
			})
		}

		// Low support — importance below canonical threshold.
		if m.Importance < policy.CanonicalMinConfidence {
			health.LowSupport++
			health.Issues = append(health.Issues, CanonicalIssue{
				MemoryID: m.ID,
				Title:    title,
				Issue:    fmt.Sprintf("Importance %.2f below canonical threshold %.2f", m.Importance, policy.CanonicalMinConfidence),
				Urgency:  "low",
			})
		}

		subjectCounts[subjectKey(m)]++
	}

	// Conflicting canonical — multiple canonical for same subject.
	for _, count := range subjectCounts {
		if count > 1 {
			health.Conflicting++
		}
	}

	return health
}

// --- helpers ---

func duplicateGroupKey(m *memory.Memory) string {
	subject := subjectWords(m, 10)
	if subject == "" {
		return ""
	}
	return strings.Join([]string{
		string(memory.EngineeringTypeOf(m)),
		memory.MemoryService(m),
		strings.TrimSpace(m.Context),
		subject,
	}, "|")
}

func conflictGroupKey(m *memory.Memory) string {
	subject := subjectWords(m, 10)
	if subject == "" {
		return ""
	}
	return strings.Join([]string{
		string(memory.EngineeringTypeOf(m)),
		memory.MemoryService(m),
		strings.TrimSpace(m.Context),
		subject,
	}, "|")
}

func subjectKey(m *memory.Memory) string {
	return strings.Join([]string{
		string(memory.EngineeringTypeOf(m)),
		memory.MemoryService(m),
		strings.TrimSpace(m.Context),
	}, "|")
}

func subjectWords(m *memory.Memory, maxWords int) string {
	base := strings.TrimSpace(m.Title)
	if base == "" {
		base = strings.TrimSpace(m.Content)
	}
	words := strings.Fields(base)
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	return strings.ToLower(strings.Join(words, " "))
}

func metadataStatus(m *memory.Memory) string {
	if m == nil || len(m.Metadata) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m.Metadata["status"]))
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
