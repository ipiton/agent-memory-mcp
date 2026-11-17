//go:build eval

package rag

import "context"

// SearchSemanticForEval compares vector-only retrieval through the shared search
// pipeline. It is unavailable in release builds and refuses degraded retrieval.
func (re *Engine) SearchSemanticForEval(ctx context.Context, query string, limit int, sourceType string, debug bool) (*SearchResponse, error) {
	return re.search(ctx, searchQuery{Query: query, Limit: limit, SourceType: sourceType, Debug: debug, semanticOnly: true})
}
