// Package hooks provides pre-store dedup/filtering logic used by the
// agent-memory-mcp hooks CLI entry points (auto-capture, checkpoint).
//
// The hooks wrapper is the only place near-duplicate session-checkpoint
// records are filtered out. Programmatic memory.Store.Store() remains
// unfiltered: adding the logic only here preserves the MCP
// store_memory tool's transparent behaviour while fixing the observed
// flood (30-60 duplicate session-checkpoint records per 2h coding
// session) originating from the hook CLI.
package hooks

import (
	"context"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
)

// DedupResult describes the decision made by Check.
type DedupResult struct {
	// Skip is true when the caller should NOT persist the new record.
	Skip bool
	// Reason is one of "empty" | "similar" | "" (no skip).
	Reason string
	// SimilarID is the ID of the most recent previous record that
	// triggered a "similar" skip; empty for "empty" or no-skip.
	SimilarID string
	// Similarity is the Jaccard score vs. the previous record (0..1).
	Similarity float64
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

// Check decides whether a pre-store hook invocation should skip persisting
// the summary. It is cheap: a single context-indexed read + Jaccard over
// lowercased whitespace-and-punctuation tokens. No embedder calls.
//
// The function is read-only on the store (RLock via snapshotForContext).
//
// If cfg.Threshold <= 0 the similarity gate is disabled and Check only
// honours the empty-content filter (when MinContentChars > 0).
func Check(ctx context.Context, store *memory.Store, summary memory.SessionSummary, cfg DedupConfig) (DedupResult, error) {
	if store == nil {
		return DedupResult{}, nil
	}

	trimmed := strings.TrimSpace(summary.Summary)
	if cfg.MinContentChars > 0 && len(trimmed) < cfg.MinContentChars {
		return DedupResult{Skip: true, Reason: "empty"}, nil
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
			Reason:     "similar",
			SimilarID:  last.ID,
			Similarity: score,
		}, nil
	}
	return DedupResult{Similarity: score}, nil
}

// JaccardSimilarity computes the Jaccard index over lowercased word-like
// tokens extracted from a and b. Tokenisation splits on whitespace and
// the common punctuation characters ",.;:!?\"'()[]{}<>/\\`~" so that
// phrases like "foo, bar." tokenise to {"foo","bar"}.
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

func isTokenSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	case ',', '.', ';', ':', '!', '?', '"', '\'',
		'(', ')', '[', ']', '{', '}', '<', '>',
		'/', '\\', '`', '~':
		return true
	}
	return false
}
