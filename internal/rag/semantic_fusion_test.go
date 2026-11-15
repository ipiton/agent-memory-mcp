package rag

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

func TestSemanticFusionPreservesSemanticOrder(t *testing.T) {
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	strong := vectorstore.Chunk{ID: "strong", DocPath: "docs/meaning.md", Content: "Meaningful answer", LastModified: old}
	weak := vectorstore.Chunk{ID: "weak", DocPath: "docs/recent.md", Content: "Repeated query terms", LastModified: time.Now()}
	keywordOnly := vectorstore.Chunk{ID: "keyword-only", DocPath: "docs/words.md", Content: "Matching words", LastModified: time.Now()}
	semantic := []vectorstore.SearchResult{{Chunk: strong, Score: 0.62}, {Chunk: weak, Score: 0.15}}
	keyword := []vectorstore.SearchResult{{Chunk: weak, Score: 90}, {Chunk: keywordOnly, Score: 100}}
	rows, contents, debug := buildHybridSearchResults("query terms", "", semantic, keyword, 3, 3, true, newFusionSettings("semantic", 60), 1)
	if len(rows) != 2 || rows[0].ID != "strong" || rows[1].ID != "weak" {
		t.Fatalf("semantic order = %+v", rows)
	}
	if rows[0].Score != 0.62 || rows[1].Score != 0.15 || contents[0] != strong.Content {
		t.Fatalf("scores/content = %+v, %v", rows, contents)
	}
	for _, row := range rows {
		breakdown := row.Debug.Breakdown
		if breakdown.KeywordRaw != 0 || breakdown.RecencyBoost != 0 || breakdown.SourceBoost != 0 || breakdown.ConfidenceBoost != 0 {
			t.Fatalf("nonsemantic contributions reported: %+v", breakdown)
		}
	}
	for _, signal := range debug.RankingSignals {
		if strings.Contains(signal, "keyword") || strings.Contains(signal, "boost") || strings.Contains(signal, "confidence") {
			t.Fatalf("nonsemantic ranking signal: %s", signal)
		}
	}
}

func TestSemanticFusionHonorsFilterAndDocumentCap(t *testing.T) {
	semantic := []vectorstore.SearchResult{
		{Chunk: vectorstore.Chunk{ID: "a-0", DocPath: "docs/a.md", Content: "first"}, Score: 0.9},
		{Chunk: vectorstore.Chunk{ID: "a-1", DocPath: "docs/a.md", Content: "second"}, Score: 0.8},
		{Chunk: vectorstore.Chunk{ID: "decision", DocPath: "docs/adr/decision.md", Content: "decision"}, Score: 0.7},
		{Chunk: vectorstore.Chunk{ID: "b-0", DocPath: "docs/b.md", Content: "other"}, Score: 0.6},
	}
	rows, contents, _ := buildHybridSearchResults("query", "docs", semantic, nil, 4, 2, false, newFusionSettings("semantic", 60), 1)
	if len(rows) != 2 || rows[0].ID != "a-0" || rows[1].ID != "b-0" || contents[1] != "other" {
		t.Fatalf("filtered/capped results = %+v, %v", rows, contents)
	}
}

func TestSemanticFusionTiesDoNotDependOnDates(t *testing.T) {
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.AddDate(20, 0, 0)
	for _, reverseDates := range []bool{false, true} {
		a, z := old, newer
		if reverseDates {
			a, z = newer, old
		}
		semantic := []vectorstore.SearchResult{
			{Chunk: vectorstore.Chunk{ID: "z", DocPath: "docs/z.md", LastModified: z}, Score: 0.7},
			{Chunk: vectorstore.Chunk{ID: "a", DocPath: "docs/a.md", LastModified: a}, Score: 0.7},
		}
		rows, _, _ := buildHybridSearchResults("query", "", semantic, nil, 2, 1, false, newFusionSettings("semantic", 60), 1)
		if len(rows) != 1 || rows[0].ID != "a" {
			t.Fatalf("reverseDates=%v: tied result = %+v", reverseDates, rows)
		}
	}
}

func TestSemanticFusionSearchDoesNotNeedKeywords(t *testing.T) {
	server := newTestEmbeddingServer(t)
	t.Cleanup(server.Close)
	engine := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), false)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.vecService.config.Fusion = "semantic"
	engine.vecService.config.Strict = true
	engine.vecService.store = buildingStore{Store: engine.vecService.store}
	response := searchRunbook(t, engine)
	if len(response.Results) == 0 || response.Retrieval.Degraded || response.Retrieval.KeywordSkipped != "" {
		t.Fatalf("semantic response = %+v", response)
	}
}

func TestSemanticFusionRefusesKeywordOnlyIndex(t *testing.T) {
	engine := newKeywordOnlyEngine(t, testEmbedderAt(t, deadEmbeddingURL(t)), true)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.vecService.config.Fusion = "semantic"
	_, err := engine.vecService.search(context.Background(), searchQuery{Query: "rollback ingress", Limit: 5})
	if err == nil || !strings.Contains(err.Error(), "index_documents") {
		t.Fatalf("keyword-only index error = %v", err)
	}
}

func TestSemanticFusionDoesNotFallBackWhenEmbeddingFails(t *testing.T) {
	server := newTestEmbeddingServer(t)
	t.Cleanup(server.Close)
	engine := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), true)
	if err := engine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.vecService.config.Fusion = "semantic"
	engine.vecService.config.Embedder = testEmbedderAt(t, deadEmbeddingURL(t))
	_, err := engine.vecService.search(context.Background(), searchQuery{Query: "unseen rollback query", Limit: 5})
	if err == nil {
		t.Fatal("semantic search silently fell back to keywords")
	}
}
