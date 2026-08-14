package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
)

// T99. Recall drops the whole semantic leg when the embedder fails and still
// returns a scored list — so a broken embedding setup and a working one are
// indistinguishable from the caller's side. Strict mode is how a measurement
// asks not to be handed the substitute.
func TestStrictRecallRefusesTextOnlyFallback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	m := &Memory{Title: "cache ttl", Content: "the cache expires after 60 seconds", Type: TypeSemantic}
	if err := store.Store(ctx, m); err != nil {
		t.Fatalf("Store: %v", err)
	}

	store.embedder = failingEmbedder{}

	// Default: a lesser answer, no error. This is the production contract and
	// it is deliberately unchanged.
	if _, err := store.Recall(ctx, "cache", Filters{}, 5); err != nil {
		t.Fatalf("non-strict recall returned an error: %v", err)
	}

	store.SetRetrievalStrict(true)
	_, err := store.Recall(ctx, "cache", Filters{}, 5)
	if err == nil {
		t.Fatal("strict recall returned results with the embedder down")
	}
	if !errors.Is(err, ErrRetrievalDegraded) {
		t.Fatalf("err = %v, want ErrRetrievalDegraded", err)
	}
	if !strings.Contains(err.Error(), "MCP_RETRIEVAL_STRICT") {
		t.Errorf("error does not say how to turn the mode off:\n%v", err)
	}
}

// A memory with no triples yields seed results at hops=0 — identical in shape
// to a graph walk that found direct hits. The flag is what separates them.
//
// Reaching that branch takes care: RecallMultihop returns early when the corpus
// holds no triples at all, so the seeded graph has to exist and the query has
// to land on a memory outside it. The pre-existing
// TestRecallMultihop_GracefulFallbackWhenSeedHasNoTriples uses an empty corpus
// and therefore exits through the earlier return without ever reaching the
// branch it is named for.
func TestMultihopMarksSeedOnlyResultsDegraded(t *testing.T) {
	store, _ := seedTriplesGraph(t)
	ctx := context.Background()

	isolated := &Memory{
		Title:      "Standalone runbook",
		Content:    "quokka telemetry dashboard rotation quokka quokka",
		Type:       TypeSemantic,
		Importance: 0.6,
	}
	if err := store.Store(ctx, isolated); err != nil {
		t.Fatalf("Store: %v", err)
	}

	const query = "quokka telemetry dashboard rotation"
	got, err := store.RecallMultihop(ctx, MultiHopRequest{Query: query, SeedK: 1})
	if err != nil {
		t.Fatalf("RecallMultihop: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no results: the isolated memory did not seed, so the degraded branch was not exercised")
	}
	for _, r := range got {
		if !r.Degraded {
			t.Errorf("result %s not marked degraded, though no graph walk ran", r.Memory.ID)
		}
		if r.Hops != 0 {
			t.Errorf("result %s has hops=%d, want 0", r.Memory.ID, r.Hops)
		}
	}

	store.SetRetrievalStrict(true)
	if _, err := store.RecallMultihop(ctx, MultiHopRequest{Query: query, SeedK: 1}); !errors.Is(err, ErrRetrievalDegraded) {
		t.Fatalf("strict multihop err = %v, want ErrRetrievalDegraded", err)
	}
}

// failingEmbedder stands in for every provider being down at once — the state
// in which Recall quietly becomes a text search.
type failingEmbedder struct{}

var errProviderDown = errors.New("provider unavailable")

func (failingEmbedder) EmbedDetailed(context.Context, string) (*embedder.EmbeddingResult, error) {
	return nil, errProviderDown
}
func (failingEmbedder) EmbedQueryDetailed(context.Context, string) (*embedder.EmbeddingResult, error) {
	return nil, errProviderDown
}
func (failingEmbedder) BatchEmbedDetailed(context.Context, []string) (*embedder.BatchEmbeddingResult, error) {
	return nil, errProviderDown
}
func (failingEmbedder) Dimensions() int { return 4 }
func (failingEmbedder) Close()          {}
