package rag

import (
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

// TestApplyPerDocumentCap (T127) pins the three properties the cap has to
// have: it spreads the list across documents, it never returns fewer rows than
// the caller asked for when candidates exist, and zero means "do nothing".
func TestApplyPerDocumentCap(t *testing.T) {
	// Ranking order, adjacent chunks of one file next to each other — the
	// shape measured on the eval corpus.
	rows := []string{"a#1", "a#2", "a#3", "b#1", "a#4", "c#1", "b#2"}
	docOf := func(s string) string { return strings.SplitN(s, "#", 2)[0] }

	t.Run("cap 1 spreads the list across documents", func(t *testing.T) {
		got := applyPerDocumentCap(rows, 1, 3, docOf)
		want := []string{"a#1", "b#1", "c#1"}
		assertRows(t, got, want)
	})

	t.Run("cap 2 keeps a second chunk of the same document", func(t *testing.T) {
		got := applyPerDocumentCap(rows, 2, 4, docOf)
		want := []string{"a#1", "a#2", "b#1", "c#1"}
		assertRows(t, got, want)
	})

	t.Run("the list is filled even when documents run out", func(t *testing.T) {
		// Only two documents exist, so a cap of 1 cannot fill four slots on its
		// own; the skipped rows come back in ranking order rather than the
		// caller getting a short list.
		got := applyPerDocumentCap([]string{"a#1", "a#2", "a#3", "b#1", "a#4"}, 1, 4, docOf)
		want := []string{"a#1", "b#1", "a#2", "a#3"}
		assertRows(t, got, want)
	})

	t.Run("nothing is reordered when the list fits", func(t *testing.T) {
		// The cap decides who is dropped at truncation; with nothing to drop it
		// leaves the ranker's order alone rather than shuffling for variety.
		fits := []string{"a#1", "a#2", "b#1"}
		assertRows(t, applyPerDocumentCap(fits, 1, 3, docOf), fits)
	})

	t.Run("zero disables the pass", func(t *testing.T) {
		got := applyPerDocumentCap(rows, 0, 3, docOf)
		assertRows(t, got, []string{"a#1", "a#2", "a#3"})
	})

	t.Run("a list shorter than the limit is untouched", func(t *testing.T) {
		short := []string{"a#1", "a#2"}
		assertRows(t, applyPerDocumentCap(short, 1, 5, docOf), short)
	})
}

func assertRows(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestBuildHybridSearchResults_PerDocumentCap (T127) checks the cap where it
// actually runs: inside the ranker, on the truncation that decides what a
// caller sees. Four chunks of one file outrank the single chunk of another;
// without the cap a top-3 is that one file three times over.
func TestBuildHybridSearchResults_PerDocumentCap(t *testing.T) {
	now := time.Now()
	chunks := []vectorstore.Chunk{
		{ID: "big-1", DocPath: "docs/big.md", Title: "Big", Content: "cache invalidation part one", LastModified: now, Embedding: []float32{1, 0}},
		{ID: "big-2", DocPath: "docs/big.md", Title: "Big", Content: "cache invalidation part two", LastModified: now, Embedding: []float32{1, 0}},
		{ID: "big-3", DocPath: "docs/big.md", Title: "Big", Content: "cache invalidation part three", LastModified: now, Embedding: []float32{1, 0}},
		{ID: "other-1", DocPath: "docs/other.md", Title: "Other", Content: "cache invalidation elsewhere", LastModified: now, Embedding: []float32{1, 0}},
	}
	// Adjacent chunks of one file carry nearly the same score — the shape the
	// measurement found on the real corpus.
	scores := map[string]float64{"big-1": 0.90, "big-2": 0.89, "big-3": 0.88, "other-1": 0.87}

	run := func(cap int) []SearchResult {
		results, _, _ := buildHybridSearchResults(
			"cache invalidation", "",
			searchResultsWithScores(chunks, scores),
			nil, len(chunks), 3, false,
			newFusionSettings("weighted", 60),
			cap,
		)
		return results
	}

	uncapped := run(0)
	if len(uncapped) != 3 {
		t.Fatalf("uncapped len = %d, want 3", len(uncapped))
	}
	if distinctPaths(uncapped) != 1 {
		t.Fatalf("uncapped top-3 spans %d documents, want 1 (the defect this pins)", distinctPaths(uncapped))
	}

	capped := run(1)
	if len(capped) != 3 {
		t.Fatalf("capped len = %d, want 3 — the cap must not shorten the list", len(capped))
	}
	if capped[0].ID != "big-1" {
		t.Errorf("capped top = %q, want the best-scoring chunk big-1 unchanged", capped[0].ID)
	}
	if capped[1].Path != "docs/other.md" {
		t.Errorf("second result = %q, want the other document to be pulled up", capped[1].Path)
	}
	if distinctPaths(capped) != 2 {
		t.Errorf("capped top-3 spans %d documents, want 2 (only two exist)", distinctPaths(capped))
	}
}

func distinctPaths(results []SearchResult) int {
	seen := map[string]struct{}{}
	for _, r := range results {
		seen[r.Path] = struct{}{}
	}
	return len(seen)
}
