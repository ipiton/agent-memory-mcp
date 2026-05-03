package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

// callRecallMultihop is the MCP-tool entry point for T50 graph-walk recall.
// It is intentionally a thin adapter over Store.RecallMultihop: parameter
// validation + path-aware text formatting so the LLM can read what chain of
// triples earned each result. The LLM-facing schema is registered in
// tools_registry.go.
func (s *MCPServer) callRecallMultihop(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	type params struct {
		Query   string `json:"query"`
		MaxHops int    `json:"max_hops"`
		SeedK   int    `json:"seed_k"`
		Limit   int    `json:"limit"`
		Context string `json:"context"`
	}
	p, err := parseParams[params](args)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "failed to parse parameters", Data: err.Error()}
	}

	query := strings.TrimSpace(p.Query)
	if query == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "query parameter is required"}
	}
	if err := userio.ValidateQuery(query); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	// Honour the documented schema bounds: 50 is the upper limit for any
	// single tool call (matches recall_memory). The deeper Store.* call
	// will additionally clamp to its own ceilings.
	if p.Limit > 50 {
		p.Limit = 50
	}

	results, err := s.memoryStore.RecallMultihop(context.Background(), memory.MultiHopRequest{
		Query:   query,
		MaxHops: p.MaxHops,
		SeedK:   p.SeedK,
		Limit:   p.Limit,
		Filters: memory.Filters{Context: strings.TrimSpace(p.Context)},
	})
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "multihop recall failed", Data: err.Error()}
	}
	return toolResultText(formatMultihopResults(query, results)), nil
}

func formatMultihopResults(query string, results []*memory.MultiHopResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("Multihop recall for %q: no results.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Multihop recall for %q (%d results):\n", query, len(results))
	for i, r := range results {
		if r == nil || r.Memory == nil {
			continue
		}
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, formatMultihopMemoryHeader(r))
		if title := strings.TrimSpace(r.Memory.Title); title != "" {
			fmt.Fprintf(&b, "   title: %s\n", title)
		}
		if ctx := strings.TrimSpace(r.Memory.Context); ctx != "" {
			fmt.Fprintf(&b, "   context: %s\n", ctx)
		}
		if len(r.Path) > 0 {
			fmt.Fprintf(&b, "   path:\n")
			for _, t := range r.Path {
				fmt.Fprintf(&b, "     - (%s)─[%s]→(%s)\n", t.Subject, t.Relation, t.Object)
			}
		}
		if snippet := truncateSnippet(r.Memory.Content, 220); snippet != "" {
			fmt.Fprintf(&b, "   snippet: %s\n", snippet)
		}
	}
	return b.String()
}

func formatMultihopMemoryHeader(r *memory.MultiHopResult) string {
	return fmt.Sprintf("[%s] hops=%d score=%.3f", r.Memory.ID, r.Hops, r.Score)
}

// truncateSnippet keeps result text printable without flooding the LLM
// context. Single-line view, trims internal whitespace runs.
func truncateSnippet(content string, maxLen int) string {
	cleaned := strings.Join(strings.Fields(content), " ")
	if len(cleaned) <= maxLen {
		return cleaned
	}
	return cleaned[:maxLen] + "…"
}
