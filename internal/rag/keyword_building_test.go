package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

// buildingStore is a store whose first-time keyword build has not finished.
type buildingStore struct {
	vectorstore.Store
}

func (buildingStore) KeywordSearch(string, int) ([]vectorstore.SearchResult, error) {
	return nil, vectorstore.ErrKeywordIndexBuilding
}

// T174: a build can take minutes on a loaded machine, and search waited for it
// until the client timed out, though vectors could answer all along.
func TestSearchDuringKeywordBuildAnswersFromVectors(t *testing.T) {
	server := newTestEmbeddingServer(t)
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), false)
	if err := e.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}
	e.vecService.store = buildingStore{Store: e.vecService.store}

	response := searchRunbook(t, e)
	if len(response.Results) == 0 {
		t.Fatal("no results from vectors")
	}
	if response.Retrieval.KeywordSkipped == "" || !response.Retrieval.Degraded {
		t.Fatalf("retrieval = %+v, want keyword_skipped and degraded", response.Retrieval)
	}

	e.vecService.config.Strict = true
	_, err := e.vecService.search(context.Background(), searchQuery{Query: "rollback ingress", Limit: 5})
	if !errors.Is(err, vectorstore.ErrKeywordIndexBuilding) {
		t.Fatalf("strict search: err = %v, want ErrKeywordIndexBuilding", err)
	}
}

// With no vectors to fall back on, an empty answer would read as "nothing found".
func TestSearchDuringKeywordBuildWithoutVectorsReportsIt(t *testing.T) {
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, deadEmbeddingURL(t)), true)
	if err := e.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}
	e.vecService.store = buildingStore{Store: e.vecService.store}

	_, err := e.vecService.search(context.Background(), searchQuery{Query: "rollback ingress", Limit: 5})
	if !errors.Is(err, vectorstore.ErrKeywordIndexBuilding) {
		t.Fatalf("search: err = %v, want ErrKeywordIndexBuilding", err)
	}
}
