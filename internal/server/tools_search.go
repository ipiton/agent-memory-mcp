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
				zap.Bool("rag_enabled_in_config", s.config.RAG.Enabled),
				zap.String("rag_index_path", s.config.RAG.IndexPath),
			)
		}
		return nil, err
	}

	query, rsErr := requiredString(args, "query")
	if rsErr != nil {
		return nil, rsErr
	}
	if err := userio.ValidateQuery(query); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	limit := boundedLimit(args, 10, 50)
	sourceType, _ := getString(args, "source_type")
	debug, _ := getBool(args, "debug")

	results, err := s.getRagEngine().Search(context.Background(), query, limit, sourceType, debug)
	if err != nil {
		if s.fileLogger != nil {
			s.fileLogger.Error("search failed", zap.Error(err), zap.String("query", query))
		}
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("search failed: %v", err)}
	}

	return toolResultText(s.formatSearchResults(results)), nil
}

func (s *MCPServer) callIndexDocuments(_ map[string]any) (any, *rpcError) {
	if err := s.requireRAGEngine(); err != nil {
		return nil, err
	}

	engine := s.getRagEngine()
	err := engine.IndexDocuments(context.Background())
	if err != nil {
		if s.fileLogger != nil {
			s.fileLogger.Error("document indexing failed", zap.Error(err))
		}
		return nil, &rpcError{Code: rpcErrServerError, Message: "document indexing failed", Data: err.Error()}
	}

	// T97: "Documents indexed successfully." was the whole answer, so the one
	// question worth asking after indexing — what is actually in the corpus —
	// had no answer short of reading a background process's log. The breakdown
	// below is that answer, and a configured root that contributed nothing is
	// called out by name.
	report, covErr := engine.Coverage()
	if covErr != nil {
		if s.fileLogger != nil {
			s.fileLogger.Warn("coverage report unavailable after indexing", zap.Error(covErr))
		}
		return toolResultText("Documents indexed successfully. (Coverage report unavailable: " + covErr.Error() + ")"), nil
	}
	return toolResultText(rag.FormatCoverage(report)), nil
}

// Result formatting

func (s *MCPServer) formatSearchResults(results *rag.SearchResponse) string {
	if len(results.Results) == 0 {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "No results found for '%s'.", results.Query)
		// An empty result set is exactly where a degraded path is most likely
		// to be misread as "there is nothing to find" (the class T97 named).
		if note := formatDegradedNote(results.Retrieval); note != "" {
			buf.WriteString("\n")
			buf.WriteString(note)
		}
		if results.Debug != nil {
			buf.WriteString("\n")
			buf.WriteString(s.formatSearchDebug(results.Debug))
		}
		return buf.String()
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Found %d results for '%s':\n\n", len(results.Results), results.Query)
	// Printed only when something fell back: a line on every healthy search
	// would be noise, and noise is how a real warning stops being read.
	if note := formatDegradedNote(results.Retrieval); note != "" {
		buf.WriteString(note)
		buf.WriteString("\n\n")
	}
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

// formatDegradedNote renders the one-line warning for a retrieval path that
// fell back, and the empty string for one that did not. T99: the JSON response
// always carries the full RetrievalPath; the text surface stays silent unless
// there is something to say.
func formatDegradedNote(path *rag.RetrievalPath) string {
	if path == nil || !path.Degraded {
		return ""
	}
	var parts []string
	if len(path.EmbeddingFallbacks) > 0 {
		parts = append(parts, fmt.Sprintf("embedding provider(s) %s failed, served by %s",
			strings.Join(path.EmbeddingFallbacks, ", "), path.EmbeddingProvider))
	}
	if path.RerankSkipped != "" {
		parts = append(parts, "reranking skipped ("+path.RerankSkipped+") — results are in hybrid order")
	}
	if len(parts) == 0 {
		return "⚠️  Degraded retrieval path."
	}
	return "⚠️  Degraded retrieval: " + strings.Join(parts, "; ") + "."
}
