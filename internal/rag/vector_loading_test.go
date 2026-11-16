package rag

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

// loadingStore is a store whose vectors are still on their way into memory.
type loadingStore struct {
	vectorstore.Store
}

func (loadingStore) StartVectorLoad() bool { return false }

// T177: under load the first vector search waited minutes for the load and the
// client gave up. It now answers from keywords at once, says so, and the strict
// mode refuses with a reason instead of a silent keyword answer.
func TestSearchWhileVectorsLoadAnswersFromKeywords(t *testing.T) {
	server := newTestEmbeddingServer(t)
	e := newKeywordOnlyEngine(t, testEmbedderAt(t, server.URL), false)
	if err := e.IndexDocuments(context.Background()); err != nil {
		t.Fatalf("IndexDocuments: %v", err)
	}
	e.vecService.config.BackgroundVectorLoad = true
	real := e.vecService.store
	e.vecService.store = loadingStore{Store: real}

	response := searchRunbook(t, e)
	if len(response.Results) == 0 {
		t.Fatal("no keyword results while vectors load")
	}
	path := response.Retrieval
	if path.VectorsSkipped == "" || !path.KeywordOnly || !path.Degraded || path.EmbeddingProvider != "" {
		t.Fatalf("retrieval = %+v, want vectors_skipped, keyword_only, degraded and no embedding", path)
	}

	e.vecService.config.Strict = true
	_, err := e.vecService.search(context.Background(), searchQuery{Query: "rollback ingress", Limit: 5})
	if !errors.Is(err, ErrRetrievalDegraded) {
		t.Fatalf("strict search: err = %v, want ErrRetrievalDegraded", err)
	}
	e.vecService.config.Strict = false

	// The real store: the first search starts the load and does not wait for
	// it; once it lands, searches use the vectors again.
	e.vecService.store = real
	deadline := time.Now().Add(10 * time.Second)
	for {
		response = searchRunbook(t, e)
		if response.Retrieval.VectorsSkipped == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("vectors never finished loading in the background")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if response.Retrieval.KeywordOnly || response.Retrieval.EmbeddingProvider == "" {
		t.Fatalf("retrieval after load = %+v, want the semantic arm back", response.Retrieval)
	}
}
