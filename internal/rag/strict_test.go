package rag

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// T99. A reranker that times out returns the hybrid order, and until now the
// response looked exactly like one the reranker had actually served. The eval
// case is the sharp one: a cold sidecar misses its first deadline and the first
// question of the QA set silently measures the no-rerank baseline under the
// reranker's name.
func TestSearchReportsRerankFailureWithoutDebugMode(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())
	engine.vecService.config.RerankTimeout = 10 * time.Millisecond
	engine.vecService.config.RerankerName = "jina"
	engine.SetReranker(&fakeReranker{blockFor: 200 * time.Millisecond})

	// debug=false — the default, and the mode an unattended run uses.
	resp, err := engine.Search(context.Background(), "alpha", 5, "", false)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Retrieval == nil {
		t.Fatal("no retrieval path on the response")
	}
	if !resp.Retrieval.Degraded {
		t.Error("Degraded = false after the reranker missed its deadline")
	}
	if resp.Retrieval.RerankSkipped != "timeout" {
		t.Errorf("RerankSkipped = %q, want \"timeout\"", resp.Retrieval.RerankSkipped)
	}
	if resp.Retrieval.Reranker != "" {
		t.Errorf("Reranker = %q, want empty — it did not serve this query", resp.Retrieval.Reranker)
	}
}

// The happy path must name the reranker, otherwise "did it run?" is still
// unanswerable and the flag above carries no information.
func TestSearchNamesTheRerankerThatServed(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())
	engine.vecService.config.RerankerName = "jina"
	engine.SetReranker(&fakeReranker{score: func(string) float64 { return 0.5 }})

	resp, err := engine.Search(context.Background(), "alpha", 5, "", false)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Retrieval.Degraded {
		t.Error("Degraded = true on a healthy path")
	}
	if resp.Retrieval.Reranker != "jina" {
		t.Errorf("Reranker = %q, want \"jina\"", resp.Retrieval.Reranker)
	}
}

// Strict mode is the "refuse, do not substitute" half. Without it an eval run
// cannot be trusted to have measured what it named.
func TestStrictModeTurnsRerankFallbackIntoAnError(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())
	engine.vecService.config.Strict = true
	engine.vecService.config.RerankTimeout = 10 * time.Millisecond
	engine.SetReranker(&fakeReranker{blockFor: 200 * time.Millisecond})

	_, err := engine.Search(context.Background(), "alpha", 5, "", false)
	if err == nil {
		t.Fatal("strict mode returned results where the reranker had failed")
	}
	if !errors.Is(err, ErrRetrievalDegraded) {
		t.Fatalf("err = %v, want ErrRetrievalDegraded", err)
	}
	// The message must say what broke *and* how to turn the mode off — one
	// without the other leaves the operator guessing.
	for _, want := range []string{"rerank", "MCP_RETRIEVAL_STRICT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message does not mention %q:\n%v", want, err)
		}
	}
}

// Strict mode must not fire on a healthy path — a flag that fails good queries
// would simply be turned off and the observability lost with it.
func TestStrictModeAllowsAnUndegradedSearch(t *testing.T) {
	engine := newRerankTestEngine(t, rerankTestChunks())
	engine.vecService.config.Strict = true
	engine.SetReranker(&fakeReranker{score: func(string) float64 { return 0.5 }})

	resp, err := engine.Search(context.Background(), "alpha", 5, "", false)
	if err != nil {
		t.Fatalf("strict mode rejected a healthy search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("no results")
	}
}
