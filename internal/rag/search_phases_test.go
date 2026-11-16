package rag

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// T178: the time of a search has to be split by phase, in the debug answer and
// in the log, or a slow search cannot be attributed to anything.
func TestSearchReportsPhaseTimes(t *testing.T) {
	server := newTestEmbeddingServer(t)
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), false)
	if err := e.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}
	core, logs := observer.New(zapcore.InfoLevel)
	e.vecService.logger = zap.New(core)

	response, err := e.vecService.search(context.Background(), searchQuery{Query: "rollback ingress", Limit: 5, Debug: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if response.Debug == nil || response.Debug.Phases == nil {
		t.Fatal("debug answer carries no phases")
	}
	p := response.Debug.Phases
	if p.EmbedMs <= 0 {
		t.Errorf("embed_ms = %v, want > 0 — the query went to the embedder", p.EmbedMs)
	}
	sum := p.RefreshMs + p.EmbedMs + p.VectorLoadMs + p.VectorRankMs + p.VectorContentMs + p.KeywordMs + p.FusionMs + p.RerankMs
	if p.TotalMs <= 0 || sum > p.TotalMs {
		t.Errorf("phases sum to %.3f ms, total %.3f ms — the parts cannot exceed the whole", sum, p.TotalMs)
	}

	entries := logs.FilterMessage("Search phases").All()
	if len(entries) != 1 {
		t.Fatalf("%d \"Search phases\" log lines, want 1", len(entries))
	}
	if got := entries[0].ContextMap()["total_ms"]; got != p.TotalMs {
		t.Errorf("logged total_ms = %v, debug total_ms = %v", got, p.TotalMs)
	}

	// Without debug=true the answer stays as it was, the log line does not.
	response, err = e.vecService.search(context.Background(), searchQuery{Query: "rollback ingress", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if response.Debug != nil {
		t.Errorf("debug = %+v without debug=true", response.Debug)
	}
	if n := logs.FilterMessage("Search phases").Len(); n != 2 {
		t.Errorf("%d \"Search phases\" log lines after two searches, want 2", n)
	}
}
