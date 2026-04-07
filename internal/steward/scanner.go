package steward

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

// maxScanMemories is the safety limit for the number of memories loaded into RAM
// during a single scan pass. Prevents OOM on large knowledge bases.
const maxScanMemories = 5000

// loadActiveMemories loads non-archived memories filtered by context and service.
// Returns at most maxScanMemories entries; if truncated, the boolean is true.
func loadActiveMemories(ctx context.Context, store *memory.Store, memContext, service string) ([]*memory.Memory, bool, error) {
	memories, err := store.List(ctx, memory.Filters{Context: memContext}, 0)
	if err != nil {
		return nil, false, err
	}
	var active []*memory.Memory
	for _, m := range memories {
		if memory.IsArchivedMemory(m) {
			continue
		}
		if service != "" && memory.MemoryService(m) != service {
			continue
		}
		active = append(active, m)
		if len(active) >= maxScanMemories {
			return active, true, nil
		}
	}
	return active, false, nil
}

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

	active, truncated, err := loadActiveMemories(ctx, store, memContext, service)
	if err != nil {
		result.Errors = append(result.Errors, RunError{Phase: "list", Message: fmt.Sprintf("failed to list memories: %v", err)})
		return result
	}
	if truncated {
		result.Errors = append(result.Errors, RunError{
			Phase:   "list",
			Message: fmt.Sprintf("scan truncated at %d memories; results may be incomplete — consider narrowing context/service filters", maxScanMemories),
		})
	}
	result.Scanned = len(active)

	now := time.Now().UTC()

	switch scope {
	case ScopeFull:
		scanDuplicates(active, policy, result)
		scanConflicts(active, result)
		scanSemanticConflicts(active, policy, result)
		scanStale(active, policy, now, result)
		scanCanonicalCandidates(active, policy, result)
	case ScopeDuplicates:
		scanDuplicates(active, policy, result)
	case ScopeConflicts:
		scanConflicts(active, result)
	case ScopeSemanticConflicts:
		scanSemanticConflicts(active, policy, result)
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
		key := groupKey(m)
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

		title := displayTitle(g.members[0], 60)

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
		key := groupKey(m)
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

		title := displayTitle(g.members[0], 60)

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

		title := displayTitle(m, 60)

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

		title := displayTitle(m, 60)

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

	staleDays := policy.EffectiveStaleDays()
	staleThreshold := now.AddDate(0, 0, -staleDays*2) // 2x stale for canonical

	// Track canonical subjects for conflict detection.
	subjectCounts := make(map[string]int)

	for _, m := range memories {
		if !memory.IsCanonicalMemory(m) {
			continue
		}
		health.Total++

		title := displayTitle(m, 60)

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

// scanSemanticConflicts finds pairs of memories that are semantically similar
// but likely contradictory based on lifecycle status, temporal markers, or
// conflicting content signals.
func scanSemanticConflicts(memories []*memory.Memory, policy Policy, result *ScanResult) {
	const (
		similarityThreshold = 0.75
		maxPairsPerGroup    = 50 // prevent O(n^2) blow-up in large groups
	)

	// Group memories by subject key (engineering type + service + context).
	type subjectGroup struct {
		members []*memory.Memory
	}
	groups := make(map[string]*subjectGroup)
	for _, m := range memories {
		if len(m.Embedding) == 0 {
			continue
		}
		key := subjectKey(m)
		if key == "" {
			continue
		}
		g, ok := groups[key]
		if !ok {
			g = &subjectGroup{}
			groups[key] = g
		}
		g.members = append(g.members, m)
	}

	// Track already-flagged pairs to avoid duplicates.
	seen := make(map[string]struct{})

	for _, g := range groups {
		if len(g.members) < 2 {
			continue
		}
		pairs := 0
		for i := 0; i < len(g.members) && pairs < maxPairsPerGroup; i++ {
			for j := i + 1; j < len(g.members) && pairs < maxPairsPerGroup; j++ {
				a, b := g.members[i], g.members[j]

				sim := vectorstore.CosineSimilarity(a.Embedding, b.Embedding)
				if sim < similarityThreshold {
					continue
				}

				if !hasContradictionSignals(a, b) {
					continue
				}

				pairKey := a.ID + "|" + b.ID
				if a.ID > b.ID {
					pairKey = b.ID + "|" + a.ID
				}
				if _, exists := seen[pairKey]; exists {
					continue
				}
				seen[pairKey] = struct{}{}
				pairs++

				titleA := displayTitle(a, 40)
				titleB := displayTitle(b, 40)

				result.Actions = append(result.Actions, Action{
					Kind:      ActionFlagContradiction,
					Handling:  HandlingReviewRequired,
					State:     StatePlanned,
					TargetIDs: []string{a.ID, b.ID},
					Title:     fmt.Sprintf("Contradiction: %s vs %s", titleA, titleB),
					Rationale: fmt.Sprintf("Semantically similar (%.2f) but conflicting signals detected", sim),
					Evidence: []string{
						fmt.Sprintf("similarity=%.3f", sim),
						fmt.Sprintf("lifecycle_a=%s, lifecycle_b=%s", memory.LifecycleStatusOf(a), memory.LifecycleStatusOf(b)),
					},
					Confidence: contradictionConfidence(sim),
				})
			}
		}
	}
}

// hasContradictionSignals checks whether two semantically similar memories
// show signs of contradiction: different lifecycle, temporal supersession,
// or opposing content patterns.
func hasContradictionSignals(a, b *memory.Memory) bool {
	la := memory.LifecycleStatusOf(a)
	lb := memory.LifecycleStatusOf(b)

	// Different lifecycle statuses on same subject → likely contradiction.
	if la != "" && lb != "" && la != lb {
		return true
	}

	// One supersedes the other explicitly.
	if a.SupersededBy == b.ID || b.SupersededBy == a.ID {
		return true
	}
	if a.Replaces == b.ID || b.Replaces == a.ID {
		return true
	}

	// Temporal conflict: both have valid_from but different windows.
	if a.ValidFrom != nil && b.ValidFrom != nil && a.ValidUntil != nil {
		if b.ValidFrom.After(*a.ValidFrom) && b.ValidFrom.Before(*a.ValidUntil) {
			return true
		}
	}

	// Content-level contradiction signals.
	contentA := strings.ToLower(a.Content)
	contentB := strings.ToLower(b.Content)
	for _, signal := range contradictionKeywords {
		if strings.Contains(contentA, signal) || strings.Contains(contentB, signal) {
			return true
		}
	}

	return false
}

var contradictionKeywords = []string{
	"replaced by", "superseded", "deprecated", "no longer",
	"instead of", "migrated to", "switched to", "removed",
	"was changed to", "previously", "old approach",
}

func contradictionConfidence(similarity float64) float64 {
	// Higher similarity with contradiction signals → higher confidence.
	if similarity >= 0.90 {
		return 0.85
	}
	if similarity >= 0.80 {
		return 0.75
	}
	return 0.65
}

// --- helpers ---

// groupKey builds a composite key for grouping memories by entity, service, context, and subject.
// Used by both duplicate and conflict scanners.
func groupKey(m *memory.Memory) string {
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

// displayTitle returns a short display title for a memory, falling back to truncated content.
func displayTitle(m *memory.Memory, maxRunes int) string {
	if m.Title != "" {
		return m.Title
	}
	return truncate(m.Content, maxRunes)
}

func truncate(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
