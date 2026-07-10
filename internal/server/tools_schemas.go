package server

import "encoding/json"

// JSON-RPC error codes.
const (
	rpcErrInvalidParams  = -32602
	rpcErrMethodNotFound = -32601
	rpcErrInternalError  = -32603
	rpcErrServerError    = -32000
)

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// buildSessionSchema returns a JSON-Schema object literal that describes the
// shared input shape of close_session, review_session_changes, and
// accept_session_changes. Pass `extras` to add tool-specific properties
// (e.g. dry_run, save_raw) or override the default summary description.
// `extras` keys are merged into properties; nil is fine.
func buildSessionSchema(summaryDesc string, extras map[string]any) map[string]any {
	if summaryDesc == "" {
		summaryDesc = "Raw session summary text"
	}
	properties := map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"description": summaryDesc,
		},
		"mode": map[string]any{
			"type":        "string",
			"enum":        []string{"coding", "incident", "migration", "research", "cleanup"},
			"default":     "coding",
			"description": "Optional session mode that influences conservative type fallback",
		},
		"context": map[string]any{
			"type":        "string",
			"description": "Optional project, task, or workflow context",
		},
		"service": map[string]any{
			"type":        "string",
			"description": "Optional service or component name",
		},
		"started_at": map[string]any{
			"type":        "string",
			"description": "Optional RFC3339 session start timestamp",
		},
		"ended_at": map[string]any{
			"type":        "string",
			"description": "Optional RFC3339 session end timestamp",
		},
		"tags": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Optional tags copied into extracted candidates and raw session memory",
		},
		"metadata": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "Optional string metadata for the raw session summary",
		},
		"format": map[string]any{
			"type":        "string",
			"enum":        []string{"text", "json"},
			"default":     "text",
			"description": "Return a human-readable report or structured JSON",
		},
	}
	for k, v := range extras {
		properties[k] = v
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []string{"summary"},
	}
}

var sessionAnalysisSchema = buildSessionSchema("Raw session summary text to analyze", map[string]any{
	"dry_run": map[string]any{
		"type":        "boolean",
		"default":     true,
		"description": "If true, only plan actions without saving the raw summary",
	},
	"save_raw": map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Persist the raw session summary when dry_run is false",
	},
	"auto_apply_low_risk": map[string]any{
		"type":        "boolean",
		"default":     false,
		"description": "Auto-apply only low-risk actions such as near-exact updates; risky changes stay in review_required",
	},
})

func mainToolDefs() []tool {
	var tools []tool
	tools = append(tools, repoToolDefs()...)
	tools = append(tools, ragToolDefs()...)
	tools = append(tools, memoryToolDefs()...)
	tools = append(tools, sessionToolDefs()...)
	tools = append(tools, workflowToolDefs()...)
	tools = append(tools, engineeringToolDefs()...)
	tools = append(tools, projectBankToolDefs()...)
	tools = append(tools, temporalToolDefs()...)
	return tools
}

// handleToolsList returns the MCP tool list, filtered to the subsystems that
// are actually available (RAG engine / memory store / steward service). Tool
// definitions live in per-category *ToolDefs builders (Round 3 H16). When
// config.ToolGrouping is set, the core toolset is collapsed into grouped
// meta-tools (T67) after availability filtering.
func (s *MCPServer) handleToolsList(_ json.RawMessage) (any, *rpcError) {
	tools := s.availableToolDefs()
	if s.config.ToolGrouping {
		tools = buildGroupedList(tools)
	}
	return map[string]any{"tools": tools}, nil
}

// availableToolDefs returns the flat tool list filtered to the subsystems that
// are actually available. Shared by the flat and grouped tools/list paths so
// grouping never advertises an action whose backing tool is absent.
func (s *MCPServer) availableToolDefs() []tool {
	tools := mainToolDefs()
	if s.stewardService != nil {
		tools = append(tools, stewardToolDefs()...)
	}

	re := s.getRagEngine()
	filtered := make([]tool, 0, len(tools))
	for _, t := range tools {
		if re == nil && ragTools[t.Name] {
			continue
		}
		if s.memoryStore == nil && memoryTools[t.Name] {
			continue
		}
		if s.memoryStore == nil && re == nil && hybridTools[t.Name] {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

func repoToolDefs() []tool {
	return []tool{
		{
			Name:        "repo_list",
			Description: "List files and folders under an allowlisted path",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to list (empty for root)",
					},
					"max_depth": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"description": "Maximum directory depth to traverse (0 = unlimited)",
					},
				},
			},
		},
		{
			Name:        "repo_read",
			Description: "Read a file from the allowlisted paths",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to the file to read",
					},
					"offset": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"description": "Byte offset to start reading from (default: 0)",
					},
					"max_bytes": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"description": "Maximum number of bytes to read (default: 2MB)",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "repo_search",
			Description: "Search for a query string in allowlisted paths",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query string",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to search within (empty for all allowed paths)",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"description": "Maximum number of search results to return (default: 200)",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

func ragToolDefs() []tool {
	return []tool{
		{
			Name:        "semantic_search",
			Description: "Hybrid search across indexed documents using semantic similarity, keyword matching, source-aware ranking, and trust metadata",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language search query",
					},
					"source_type": map[string]any{
						"type":        "string",
						"description": "Optional source type filter: docs, adr, rfc, changelog, runbook, postmortem, ci_config, helm, terraform, k8s",
					},
					"debug": map[string]any{
						"type":        "boolean",
						"description": "Include score breakdown, applied filters, and ranking boosts in the response",
						"default":     false,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results (default: 10)",
						"default":     10,
						"minimum":     1,
						"maximum":     50,
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "index_documents",
			Description: "Re-index documents for RAG search",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		// Memory tools
	}
}

func sessionToolDefs() []tool {
	return []tool{
		{
			Name:        "close_session",
			Description: "Analyze a finished session into raw summary metadata, candidate knowledge items, and review-safe consolidation actions",
			InputSchema: sessionAnalysisSchema,
		},
		{
			Name:        "analyze_session",
			Description: "Compatibility alias for close_session with the same planning and reporting behavior",
			InputSchema: sessionAnalysisSchema,
		},
		{
			Name:        "review_session_changes",
			Description: "Re-run session analysis in explicit review mode and return only the review-oriented consolidation report",
			InputSchema: buildSessionSchema("Raw session summary text to review", nil),
		},
		{
			Name:        "accept_session_changes",
			Description: "Persist the raw session summary, auto-apply low-risk session changes, and return the remaining review backlog",
			InputSchema: buildSessionSchema("Raw session summary text to apply", nil),
		},
	}
}

func projectBankToolDefs() []tool {
	return []tool{
		{
			Name:        "summarize_project_context",
			Description: "Summarize recent decisions, runbooks, incidents, and related docs for a project context",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"context": map[string]any{"type": "string", "description": "Optional project or task context"},
					"focus":   map[string]any{"type": "string", "description": "Optional focus query for narrowing the summary"},
					"service": map[string]any{"type": "string", "description": "Optional service or component name"},
					"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 5},
				},
			},
		},
		{
			Name:        "project_bank_view",
			Description: "Show a structured project bank view for canonical knowledge, decisions, runbooks, incidents, caveats, migrations, the review queue, or sediment candidates",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"view": map[string]any{
						"type":        "string",
						"enum":        []string{"canonical_overview", "overview", "decisions", "runbooks", "incidents", "caveats", "migrations", "review_queue", "sediment_candidates"},
						"default":     "canonical_overview",
						"description": "Which project bank view to render",
					},
					"context": map[string]any{"type": "string", "description": "Optional project or task context"},
					"service": map[string]any{"type": "string", "description": "Optional service or component filter"},
					"status": map[string]any{
						"type":        "string",
						"description": "Optional lifecycle or status filter such as canonical, active, draft, outdated, superseded, or review_required",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Optional owner filter for canonical or maintained knowledge",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional tags that all returned items must contain",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     50,
						"default":     10,
						"description": "Maximum number of items per section",
					},
					"format": map[string]any{
						"type":        "string",
						"enum":        []string{"text", "json"},
						"default":     "text",
						"description": "Return a human-readable report or structured JSON",
					},
				},
			},
		},
		// Temporal knowledge tools.
	}
}
