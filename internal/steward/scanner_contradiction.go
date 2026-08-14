package steward

// Contradiction detection. T91: split out of scanner.go, which had grown to 817
// lines covering six unrelated scans. This block is the one with real internal
// structure — four layers of false-positive suppression accumulated over T60,
// T71, T72 and T82 — and it is the block most likely to be edited next (T105
// rewrites its keyword layer). It moved verbatim; no behaviour change.

import (
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/scoring"
)

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

				sim := scoring.CosineSimilarity(a.Embedding, b.Embedding)
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
	// T72: a "Task complete: X" ↔ "Session close / X" pair is the T71 dual-write
	// class — two terminal episodics of the same task, not a contradiction. The
	// scanner only pairs same-subject memories, so both being terminal records
	// identifies this class. Suppress before any keyword/lifecycle signal fires,
	// otherwise a task-complete summary mentioning "removed"/"switched to" gets
	// mis-flagged and buries real conflicts in the review inbox.
	if memory.IsTerminalRecord(a) && memory.IsTerminalRecord(b) {
		return false
	}

	// T82: suppress three non-terminal false-positive classes that are kinship,
	// time-series, or lifecycle relationships of the same subject — not
	// contradictions. These dominated the residual contradiction FPs after T72.
	if terminalVsProceduralKin(a, b) || // (a) terminal record vs its Pattern:/Lesson: extraction
		periodicSameType(a, b) || // (b) successive Strategy review* snapshots
		taskLifecyclePair(a, b) { // (c) Task started: ↔ Task complete: of one slug
		return false
	}

	la := memory.LifecycleStatusOf(a)
	lb := memory.LifecycleStatusOf(b)

	// A bare lifecycle difference is NOT a contradiction. A raw session summary
	// and the extracted/canonical entity for the same subject (dual encoding of
	// one event) naturally sit at different lifecycle/knowledge layers, and
	// draft→active→canonical is normal maturation. Only an explicit invalidation
	// — one side outdated/superseded while the other is still live — is a real
	// conflict. This collapses the dual-encoding false-positive class (T60).
	if lifecycleInvalidationConflict(la, lb) {
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

func titleHasPrefix(m *memory.Memory, prefix string) bool {
	return m != nil && strings.HasPrefix(strings.TrimSpace(m.Title), prefix)
}

// sameContext reports whether two memories share the same non-empty context.
func sameContext(a, b *memory.Memory) bool {
	ca := strings.TrimSpace(a.Context)
	return ca != "" && strings.EqualFold(ca, strings.TrimSpace(b.Context))
}

// terminalVsProceduralKin (T82 class a): a terminal record (Task complete /
// Session close) paired with a Pattern:/Lesson: procedural extracted from the
// same context is kinship — the pattern is derived from the task — not a
// contradiction.
func terminalVsProceduralKin(a, b *memory.Memory) bool {
	if !sameContext(a, b) {
		return false
	}
	procedural := func(m *memory.Memory) bool {
		return titleHasPrefix(m, "Pattern:") || titleHasPrefix(m, "Lesson:")
	}
	return (memory.IsTerminalRecord(a) && procedural(b)) ||
		(memory.IsTerminalRecord(b) && procedural(a))
}

// periodicSameType (T82 class b): two periodic snapshots of the same series
// (e.g. successive "Strategy review" entries with different dates) are a time
// series, not a contradiction.
func periodicSameType(a, b *memory.Memory) bool {
	const periodicPrefix = "Strategy review"
	return titleHasPrefix(a, periodicPrefix) && titleHasPrefix(b, periodicPrefix)
}

// taskLifecyclePair (T82 class c): a "Task started: X" ↔ completion pair for the
// same slug is a lifecycle progression, not a contradiction. T83 widened the
// completion side to every sibling prefix ("Implementation/Epic/Deploy/Research
// complete:", "Task S<N> complete:") via memory.CompletionSubject. Completion ↔
// completion pairs are already suppressed one gate earlier by the T72 both-
// terminal check in hasContradictionSignals.
func taskLifecyclePair(a, b *memory.Memory) bool {
	_, completeA := memory.CompletionSubject(a.Title)
	_, completeB := memory.CompletionSubject(b.Title)
	startedA := titleHasPrefix(a, "Task started:")
	startedB := titleHasPrefix(b, "Task started:")
	if !((startedA && completeB) || (completeA && startedB)) {
		return false
	}
	// Same slug: identical task subject after the prefix, or same context.
	return sameContext(a, b) || (taskSubject(a) != "" && taskSubject(a) == taskSubject(b))
}

// taskSubject returns the slug after a "Task started:" or any completion prefix.
func taskSubject(m *memory.Memory) string {
	if m == nil {
		return ""
	}
	t := strings.TrimSpace(m.Title)
	if strings.HasPrefix(t, "Task started:") {
		return strings.TrimSpace(strings.TrimPrefix(t, "Task started:"))
	}
	if subject, ok := memory.CompletionSubject(t); ok {
		return subject
	}
	return ""
}

// lifecycleInvalidationConflict reports whether two lifecycle statuses on the
// same subject represent a genuine conflict: one entry has been explicitly
// invalidated (outdated or superseded) while the other is still live (active,
// canonical, or draft). Differences purely among the live statuses
// (draft↔active↔canonical) are dual encoding / maturation of the same subject,
// not contradictions, and must not be flagged.
func lifecycleInvalidationConflict(a, b memory.LifecycleStatus) bool {
	invalidated := func(s memory.LifecycleStatus) bool {
		return s == memory.LifecycleOutdated || s == memory.LifecycleSuperseded
	}
	live := func(s memory.LifecycleStatus) bool {
		return s == memory.LifecycleActive || s == memory.LifecycleCanonical || s == memory.LifecycleDraft
	}
	return (invalidated(a) && live(b)) || (invalidated(b) && live(a))
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
