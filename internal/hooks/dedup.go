// Package hooks provides pre-store dedup/filtering logic shared by the
// agent-memory-mcp hooks CLI entry points (auto-capture, checkpoint) and
// by the in-process server-side session_tracker pipeline.
//
// Both call sites apply Check before persisting session-checkpoint
// records. Programmatic memory.Store.Store() outside these paths remains
// unfiltered, preserving the MCP store_memory tool's transparent
// behaviour while fixing the observed flood (30-60 duplicate
// session-checkpoint records per 2h coding session) at every entry that
// produces them.
package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// Reason values returned in DedupResult.Reason and passed to
// Store.IncrementDedupSkipped. Exported so callers (hooks CLI, tests) do
// not hardcode string literals.
const (
	// ReasonSimilar indicates the candidate summary's Jaccard similarity
	// against the most recent session-checkpoint in the same context
	// was at or above cfg.Threshold.
	ReasonSimilar = "similar"
	// ReasonEmpty indicates the candidate summary was shorter than
	// cfg.MinContentChars after whitespace trimming, or was entirely
	// whitespace (which is always skipped regardless of MinContentChars).
	ReasonEmpty = "empty"
	// ReasonHookNoise indicates the candidate summary carried no reusable
	// content and must not be persisted. Two shapes qualify: a raw session-hook
	// JSON payload with only metadata fields like session_id / hook_event_name /
	// reason (T80), or a chore log whose every line is a maintenance-action
	// bullet like "- Document search: …" / "- Merged duplicates: …" (T85).
	// SessionEnd on /clear, a background session with an empty transcript, or a
	// session that only ran searches produces these; they were the largest
	// source of no-content episodic stubs and poisoned kNN (their embedding is
	// built from the query text, so the log ranks above the answer to it).
	ReasonHookNoise = "hook_noise"
)

// hookMetadataKeys are the fields present in a raw Claude Code session-hook
// JSON payload. A summary whose keys are entirely within this set carries no
// session content (T80).
var hookMetadataKeys = map[string]struct{}{
	"session_id":      {},
	"transcript_path": {},
	"cwd":             {},
	"prompt_id":       {},
	"hook_event_name": {},
	"reason":          {},
	"source":          {},
	"trigger":         {},
}

// isHookMetadataOnly reports whether summary is a raw session-hook JSON payload
// with no actual session content — a JSON object whose keys are all known
// hook-metadata fields (T80). A real prose summary, or any JSON carrying a
// content field, does not match.
func isHookMetadataOnly(summary string) bool {
	s := strings.TrimSpace(summary)
	if len(s) < 2 || s[0] != '{' {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	if len(obj) == 0 {
		return false
	}
	for k := range obj {
		if _, ok := hookMetadataKeys[strings.ToLower(k)]; !ok {
			return false
		}
	}
	return true
}

// choreLogPrefixes are the action-label prefixes of maintenance-log bullet
// lines that record what the agent did (searches, merges, inspections) but
// carry no reusable knowledge. Whitelist by design (T85): a summary whose every
// line is one of these is skipped, and anything not listed is preserved. A
// blacklist ("content lacks the word Stored") over-matched real task reports
// 12:1 on live data — 117 chore logs vs 1362 genuine closure reports.
//
// Every label here has a value that is a query / path / uuid / view-name — never
// a standalone knowledge statement, even when the underlying action really ran.
// "incident investigation" was deliberately EXCLUDED: unlike the others, a human
// bullet "- Incident investigation: root cause was X, fixed by Y" reads as real
// knowledge, so whitelisting it risked silently dropping a genuine writeup
// (loss-aversion is this guard's whole point). A session that only ran
// recall_similar_incidents therefore leaks one small record — acceptable noise
// versus losing a real root-cause report.
var choreLogPrefixes = map[string]struct{}{
	"document search":     {},
	"memory recall":       {},
	"repo search":         {},
	"inspected file":      {},
	"merged duplicates":   {},
	"marked outdated":     {},
	"project bank review": {},
}

// isChoreLogOnly reports whether summary consists solely of maintenance-action
// bullet lines whose labels are all in choreLogPrefixes (T85). Whitespace-only
// lines are ignored; a single unrecognised line (e.g. "- Stored memory: …")
// makes the summary real content and returns false.
func isChoreLogOnly(summary string) bool {
	sawBullet := false
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, ok := choreBulletLabel(line)
		if !ok {
			return false
		}
		if _, chore := choreLogPrefixes[label]; !chore {
			return false
		}
		sawBullet = true
	}
	return sawBullet
}

// choreBulletLabel extracts the lowercased label of a "- Label: value" bullet
// line. It requires a list-bullet marker ("- " or "* ") so a prose sentence
// containing a colon ("Fixed the search: it now works") is not treated as a
// labelled bullet. Returns ok=false when the line is not such a bullet.
func choreBulletLabel(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "- ")
	if !ok {
		rest, ok = strings.CutPrefix(line, "* ")
	}
	if !ok {
		return "", false
	}
	idx := strings.IndexByte(rest, ':')
	if idx <= 0 {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(rest[:idx])), true
}

// DedupResult describes the decision made by Check.
type DedupResult struct {
	// Skip is true when the caller should NOT persist the new record.
	Skip bool
	// Reason is one of ReasonEmpty | ReasonSimilar | "" (no skip).
	Reason string
	// SimilarID is the ID of the most recent previous record that
	// triggered a ReasonSimilar skip; empty for ReasonEmpty or no-skip.
	SimilarID string
	// Similarity is the Jaccard score vs. the previous record (0..1).
	Similarity float64
}

// NewDedupConfig builds a DedupConfig from the runtime knobs, honouring
// the disabled escape hatch. When disabled is true the returned config has
// Threshold=0 and MinContentChars=0, so Check short-circuits to a no-skip
// result for any non-whitespace candidate (whitespace-only summaries are
// still dropped as ReasonEmpty regardless — see Check godoc).
func NewDedupConfig(disabled bool, threshold float64, minChars int, window time.Duration) DedupConfig {
	if disabled {
		return DedupConfig{}
	}
	return DedupConfig{
		Threshold:       threshold,
		MinContentChars: minChars,
		Window:          window,
	}
}

// DedupConfig controls Check behaviour.
//
// Threshold <= 0 disables the similarity filter entirely (pass-through).
// The empty-content filter still applies when MinContentChars > 0.
type DedupConfig struct {
	// Threshold is the Jaccard similarity (0..1) at or above which a
	// new record is considered a near-duplicate of the last one.
	Threshold float64
	// MinContentChars is the minimum TrimSpace length of Summary
	// below which the record is skipped with reason="empty".
	MinContentChars int
	// Window bounds how far back to look for a near-duplicate.
	Window time.Duration
}

// Check compares the candidate summary against the most recent session-checkpoint
// in the same context within cfg.Window. It is cheap: a read-locked cache snapshot +
// a single Store.Get + Jaccard similarity on token sets. No embedder calls.
//
// If cfg.Threshold <= 0 the similarity gate is disabled and Check only
// honours the empty-content filter. Empty/whitespace-only summaries are
// always skipped with ReasonEmpty regardless of cfg.MinContentChars.
func Check(ctx context.Context, store *memory.Store, summary memory.SessionSummary, cfg DedupConfig) (DedupResult, error) {
	if store == nil {
		return DedupResult{}, nil
	}

	trimmed := strings.TrimSpace(summary.Summary)
	// Whitespace-only summaries are never useful to persist: short-circuit
	// regardless of MinContentChars so downstream Jaccard isn't forced to
	// handle an empty token set.
	if trimmed == "" {
		return DedupResult{Skip: true, Reason: ReasonEmpty}, nil
	}
	if cfg.MinContentChars > 0 && len(trimmed) < cfg.MinContentChars {
		return DedupResult{Skip: true, Reason: ReasonEmpty}, nil
	}
	// T80: a raw session-hook JSON payload (session_id/hook_event_name/reason,
	// no real content) passes the length check but must not be persisted — it
	// would create a no-content episodic stub. Applies regardless of the
	// similarity gate below.
	if isHookMetadataOnly(trimmed) {
		return DedupResult{Skip: true, Reason: ReasonHookNoise}, nil
	}
	// T85: a chore log whose every line is a maintenance-action bullet
	// ("- Document search: …", "- Merged duplicates: …") carries no knowledge
	// and self-poisons kNN. Whitelist match, also independent of the gate below.
	if isChoreLogOnly(trimmed) {
		return DedupResult{Skip: true, Reason: ReasonHookNoise}, nil
	}

	if cfg.Threshold <= 0 {
		// Filter disabled: return pass-through even when content is long.
		return DedupResult{}, nil
	}

	window := cfg.Window
	if window <= 0 {
		window = 10 * time.Minute
	}
	since := time.Now().Add(-window)

	last, err := store.LastInContext(ctx, summary.Context, since)
	if err != nil {
		return DedupResult{}, err
	}
	if last == nil {
		return DedupResult{}, nil
	}

	score := JaccardSimilarity(trimmed, last.Content)
	if score >= cfg.Threshold {
		return DedupResult{
			Skip:       true,
			Reason:     ReasonSimilar,
			SimilarID:  last.ID,
			Similarity: score,
		}, nil
	}
	return DedupResult{Similarity: score}, nil
}

// JaccardSimilarity computes the Jaccard index over lowercased word-like
// tokens extracted from a and b. Tokenisation splits on any Unicode
// whitespace, punctuation, or symbol rune — so phrases like "foo, bar."
// or "Исправил баг — обновил конфиг, закоммитил" tokenise to the
// expected word set without typographic punctuation leaking into tokens.
//
// Returns 0 when either side has no tokens.
func JaccardSimilarity(a, b string) float64 {
	setA := tokenSet(a)
	setB := tokenSet(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}

	// Iterate over the smaller set when computing intersection.
	small, big := setA, setB
	if len(big) < len(small) {
		small, big = big, small
	}
	inter := 0
	for tok := range small {
		if _, ok := big[tok]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func tokenSet(s string) map[string]struct{} {
	if s == "" {
		return nil
	}
	lower := strings.ToLower(s)
	tokens := strings.FieldsFunc(lower, isTokenSeparator)
	if len(tokens) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out[t] = struct{}{}
	}
	return out
}

// isTokenSeparator reports whether r should break token boundaries.
// Uses Unicode categories so typographic punctuation («»—„"‚‘' etc.),
// Cyrillic/other-script whitespace, and currency/math symbols are all
// treated as separators. This keeps Russian-language summaries with
// em-dashes and typographic quotes from producing spurious tokens.
func isTokenSeparator(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}
