package scoring

import (
	"regexp"
	"strings"
)

// pitfallRegex matches queries that suggest the user is about to attempt an
// approach we may have already tried and rejected. Single-word keywords use
// word boundaries to avoid matches inside unrelated words (e.g. "retry" must
// not fire the "try" keyword, "unavoidable" must not fire "avoid").
var pitfallRegex = regexp.MustCompile(`(?i)(\bhow to\b|\bapproach\b|\btry\b|\bfailed\b|\bpitfall\b|\bwhy not\b|\blesson\b|\bavoid\b)`)

// IsPitfallQuery reports whether the query suggests an agent is revisiting
// a possibly-abandoned approach. Used to boost dead_end retrievals in
// semantic ranking and to blend a top-1 dead_end suggestion in recall_memory.
// The keyword list is intentionally narrow; add new terms only if retrieval
// evaluation (T43) shows demonstrable improvement.
func IsPitfallQuery(query string) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return false
	}
	return pitfallRegex.MatchString(trimmed)
}
