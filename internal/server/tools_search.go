package server

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
	"go.uber.org/zap"
)

func (s *MCPServer) callSemanticSearch(args map[string]any) (any, *rpcError) {
	if err := s.requireRAGEngine(); err != nil {
		if s.fileLogger != nil {
			s.fileLogger.Warn("semantic_search called but RAG engine is not available",
				zap.Bool("rag_enabled_in_config", s.config.RAGEnabled),
				zap.String("rag_index_path", s.config.RAGIndexPath),
			)
		}
		return nil, err
	}

	query, ok := getString(args, "query")
	if !ok {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "query parameter is required"}
	}
	query = strings.TrimSpace(query)
	if err := userio.ValidateQuery(query); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	limit := boundedLimit(args, 10, 50)
	sourceType, _ := getString(args, "source_type")
	debug, _ := getBool(args, "debug")

	results, err := s.ragEngine.Search(context.Background(), query, limit, sourceType, debug)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("search failed: %v", err)}
	}

	return toolResultText(s.formatSearchResults(results)), nil
}

func (s *MCPServer) callIndexDocuments(_ map[string]any) (any, *rpcError) {
	if err := s.requireRAGEngine(); err != nil {
		return nil, err
	}

	err := s.ragEngine.IndexDocuments(context.Background())
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "document indexing failed", Data: err.Error()}
	}

	return toolResultText("Documents indexed successfully."), nil
}

// Result formatting

func (s *MCPServer) formatSearchResults(results *rag.SearchResponse) string {
	if len(results.Results) == 0 {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "No results found for '%s'.", results.Query)
		if results.Debug != nil {
			buf.WriteString("\n")
			buf.WriteString(s.formatSearchDebug(results.Debug))
		}
		return buf.String()
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Found %d results for '%s':\n\n", len(results.Results), results.Query)
	if results.Debug != nil {
		buf.WriteString(s.formatSearchDebug(results.Debug))
		buf.WriteString("\n\n")
	}

	for i, result := range results.Results {
		fmt.Fprintf(&buf, "%d. **%s** (relevance: %.2f)\n", i+1, result.Title, result.Score)
		fmt.Fprintf(&buf, "   Path: %s\n", result.Path)
		if result.SourceType != "" {
			fmt.Fprintf(&buf, "   Source type: %s\n", result.SourceType)
		}
		if result.Trust != nil {
			fmt.Fprintf(&buf, "   Trust: %s\n", userio.FormatTrust(result.Trust))
		}
		if result.Debug != nil {
			fmt.Fprintf(&buf,
				"   Score breakdown: semantic=%.3f keyword_raw=%.3f keyword_norm=%.3f recency=%.3f source=%.3f confidence=%.3f final=%.3f\n",
				result.Debug.Breakdown.Semantic,
				result.Debug.Breakdown.KeywordRaw,
				result.Debug.Breakdown.KeywordNormalized,
				result.Debug.Breakdown.RecencyBoost,
				result.Debug.Breakdown.SourceBoost,
				result.Debug.Breakdown.ConfidenceBoost,
				result.Debug.Breakdown.FinalScore,
			)
			if len(result.Debug.AppliedBoosts) > 0 {
				fmt.Fprintf(&buf, "   Applied boosts: %s\n", strings.Join(result.Debug.AppliedBoosts, ", "))
			}
		}
		fmt.Fprintf(&buf, "   %s\n\n", result.Snippet)
	}

	fmt.Fprintf(&buf, "Search time: %d ms", results.SearchTime)
	return buf.String()
}

func (s *MCPServer) formatSearchDebug(debug *rag.SearchDebug) string {
	if debug == nil {
		return ""
	}

	var buf bytes.Buffer
	if len(debug.AppliedFilters) > 0 {
		fmt.Fprintf(&buf, "Applied filters: %s\n", strings.Join(debug.AppliedFilters, ", "))
	} else {
		buf.WriteString("Applied filters: none\n")
	}
	fmt.Fprintf(&buf, "Ranking signals: %s\n", strings.Join(debug.RankingSignals, ", "))
	fmt.Fprintf(&buf,
		"Indexed chunks: %d | Filtered out: %d | Discarded as noise: %d | Candidates: %d | Returned: %d",
		debug.IndexedChunks,
		debug.FilteredOut,
		debug.DiscardedAsNoise,
		debug.CandidateCount,
		debug.ReturnedCount,
	)

	return buf.String()
}
