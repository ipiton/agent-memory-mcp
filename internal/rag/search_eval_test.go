//go:build eval

package rag

import (
	"context"
	"reflect"
	"testing"
)

func TestSearchSemanticForEvalUsesPerCallMode(t *testing.T) {
	server := newTestEmbeddingServer(t)
	t.Cleanup(server.Close)
	engine := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), false)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := engine.Search(context.Background(), "rollback ingress", 5, "", true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.SearchSemanticForEval(context.Background(), "rollback ingress", 5, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) == 0 || result.Retrieval.Degraded || result.Debug.Phases.KeywordMs != 0 {
		t.Fatalf("semantic evaluation response = %+v", result)
	}
	for _, row := range result.Results {
		score := row.Debug.Breakdown
		if score.KeywordRaw != 0 || score.RecencyBoost != 0 || score.SourceBoost != 0 || score.ConfidenceBoost != 0 {
			t.Fatalf("nonsemantic score contribution = %+v", score)
		}
	}
	after, err := engine.Search(context.Background(), "rollback ingress", 5, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Debug.RankingSignals, after.Debug.RankingSignals) {
		t.Fatalf("evaluation changed normal search: before=%v after=%v", before.Debug.RankingSignals, after.Debug.RankingSignals)
	}
	if reflect.DeepEqual(result.Debug.RankingSignals, after.Debug.RankingSignals) {
		t.Fatal("semantic evaluation used the normal ranking signals")
	}
}

func TestSearchSemanticForEvalSharesLimitsAndNilGuard(t *testing.T) {
	var absent *Engine
	if _, err := absent.SearchSemanticForEval(context.Background(), "query", 1, "", false); err == nil {
		t.Fatal("nil engine accepted")
	}
	server := newBatchEmbeddingServer(t)
	t.Cleanup(server.Close)
	engine := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), false)
	writeCorpusFile(t, engine.repoRoot, "docs/other.md", "# Other\n\nAnother procedure.\n")
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.config.RAG.MaxResults = 1
	for _, limit := range []int{-1, 0, 100} {
		result, err := engine.SearchSemanticForEval(context.Background(), "rollback ingress", limit, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Results) != 1 {
			t.Fatalf("limit=%d returned %d results", limit, len(result.Results))
		}
	}
	result, err := engine.SearchSemanticForEval(context.Background(), "rollback ingress", 1, "adr", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 0 {
		t.Fatal("evaluation ignored the source-type filter")
	}
}

func TestSearchSemanticForEvalRefusesLoadingVectors(t *testing.T) {
	server := newTestEmbeddingServer(t)
	t.Cleanup(server.Close)
	engine := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), true)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.vecService.config.BackgroundVectorLoad = true
	engine.vecService.store = loadingStore{Store: engine.vecService.store}
	if _, err := engine.SearchSemanticForEval(context.Background(), "rollback ingress", 5, "", false); err == nil {
		t.Fatal("semantic comparison silently used keywords while vectors were loading")
	}
}
