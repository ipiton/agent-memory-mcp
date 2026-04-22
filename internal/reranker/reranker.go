// Package reranker provides a neural cross-encoder re-ranking step over
// candidate documents produced by the RAG hybrid search. It is designed to be
// opt-in behind a feature flag, with strict timeout/fallback semantics so the
// retrieval pipeline always degrades gracefully to the un-reranked hybrid
// result when a provider fails or times out.
//
// Production provider is Jina's `/v1/rerank` HTTP endpoint
// (jina-reranker-v2-base-multilingual). Tests inject deterministic in-process
// rerankers via the Reranker interface.
package reranker

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ErrDisabled is returned by New when the provider is empty or "disabled".
// Callers should treat this as a non-fatal signal that reranking is opt-in
// and not enabled for this deployment.
var ErrDisabled = errors.New("reranker: disabled")

// ErrProviderUnknown is returned by New when Config.Provider doesn't match any
// supported provider identifier.
var ErrProviderUnknown = errors.New("reranker: unknown provider")

// Candidate is one document considered for re-ranking.
type Candidate struct {
	ID      string
	Title   string
	Content string
}

// Scored pairs a candidate ID with its relevance score as assigned by the
// reranker. Higher is more relevant.
type Scored struct {
	ID    string
	Score float64
}

// Reranker re-orders a slice of candidates by relevance to the query.
//
// Implementations MUST respect ctx: timeout wrapping happens at the caller so
// a well-behaved implementation will return a context error (context.Canceled
// or context.DeadlineExceeded) promptly when ctx is done.
type Reranker interface {
	Rerank(ctx context.Context, query string, candidates []Candidate) ([]Scored, error)
}

// Config controls how New builds a Reranker.
type Config struct {
	// Provider selects the adapter. Supported values: "jina", "disabled", "".
	Provider string
	// Model is the provider-specific model identifier
	// (e.g. "jina-reranker-v2-base-multilingual").
	Model string
	// APIKey is the Bearer token for the provider.
	APIKey string
	// Endpoint overrides the default provider endpoint. Used mainly by tests
	// to point the Jina adapter at an httptest.Server URL.
	Endpoint string
	// Timeout is recorded for introspection; actual timeout enforcement
	// happens via context.WithTimeout at the call site.
	Timeout time.Duration
	// TopN is the maximum number of candidates the caller plans to send to
	// the reranker. The adapter also echoes this field in the HTTP payload.
	TopN int
}

// New returns a Reranker based on cfg.Provider.
//
//   - "", "disabled": returns (nil, ErrDisabled). Callers must check both the
//     returned value and ErrDisabled explicitly — a nil Reranker is the
//     expected "feature off" state.
//   - "jina": returns a HTTP adapter against the Jina /v1/rerank endpoint.
//   - anything else: returns ErrProviderUnknown.
func New(cfg Config, logger *zap.Logger) (Reranker, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "", "disabled":
		return nil, ErrDisabled
	case "jina":
		return newJinaReranker(cfg, logger), nil
	default:
		return nil, ErrProviderUnknown
	}
}
