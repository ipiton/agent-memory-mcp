package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/stats"
)

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

var sessionAnalysisSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary": map[string]any{
			"type":        "string",
			"description": "Raw session summary text to analyze",
		},
		"mode": map[string]any{
			"type":        "string",
			"enum":        []string{"coding", "incident", "migration", "research", "cleanup"},
			"description": "Optional session mode that influences conservative type fallback",
			"default":     "coding",
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
		"format": map[string]any{
			"type":        "string",
			"enum":        []string{"text", "json"},
			"default":     "text",
			"description": "Return a human-readable report or structured JSON",
		},
	},
	"required": []string{"summary"},
}

func (s *MCPServer) handleToolsList(_ json.RawMessage) (any, *rpcError) {
	tools := []tool{
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
		{
			Name:        "store_memory",
			Description: "Store a memory in the agent's long-term memory",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "Memory content",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Short title for the memory",
					},
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"episodic", "semantic", "procedural", "working"},
						"description": "Memory type: episodic (events), semantic (facts), procedural (patterns), working (current context)",
						"default":     "semantic",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Tags for categorization",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Context (task slug, session, project)",
					},
					"importance": map[string]any{
						"type":        "number",
						"minimum":     0,
						"maximum":     1,
						"description": "Memory importance (0.0 - 1.0)",
						"default":     0.5,
					},
				},
				"required": []string{"content"},
			},
		},
		{
			Name:        "recall_memory",
			Description: "Recall information from memory by semantic or text query with trust-aware ranking",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Query to search in memory",
					},
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"episodic", "semantic", "procedural", "working", "all"},
						"description": "Filter by memory type",
						"default":     "all",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Filter by context",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Filter by tags",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     10,
						"minimum":     1,
						"maximum":     50,
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "update_memory",
			Description: "Update an existing memory",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Memory ID to update",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "New content",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "New title",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "New tags",
					},
					"importance": map[string]any{
						"type":        "number",
						"minimum":     0,
						"maximum":     1,
						"description": "New importance",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "delete_memory",
			Description: "Delete a memory",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Memory ID to delete",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "list_memories",
			Description: "List all memories with optional filtering",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"episodic", "semantic", "procedural", "working", "all"},
						"description": "Filter by memory type",
						"default":     "all",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Filter by context",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     20,
						"minimum":     1,
						"maximum":     100,
					},
				},
			},
		},
		{
			Name:        "memory_stats",
			Description: "Get agent memory statistics",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "merge_duplicates",
			Description: "Merge duplicate memories into a primary entry and archive the rest",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"primary_id": map[string]any{
						"type":        "string",
						"description": "Memory ID to keep as the primary consolidated entry",
					},
					"duplicate_ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Memory IDs to merge into the primary entry",
					},
				},
				"required": []string{"primary_id", "duplicate_ids"},
			},
		},
		{
			Name:        "mark_outdated",
			Description: "Mark a memory as outdated or superseded so it is downranked in future recall",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Memory ID to mark outdated",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Why this memory is outdated",
					},
					"superseded_by": map[string]any{
						"type":        "string",
						"description": "Optional newer memory ID that supersedes this one",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "promote_to_canonical",
			Description: "Promote a memory to canonical knowledge so trust-aware retrieval prefers it",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Memory ID to promote",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Owner or team responsible for this canonical entry",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "conflicts_report",
			Description: "Report duplicate candidates, conflicting statuses, and multiple canonical entries in memory",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"context": map[string]any{
						"type":        "string",
						"description": "Optional project or task context",
					},
					"service": map[string]any{
						"type":        "string",
						"description": "Optional service or component name",
					},
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"episodic", "semantic", "procedural", "working", "all"},
						"description": "Optional memory type filter",
						"default":     "all",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional tags that all reported groups must contain",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     50,
						"default":     10,
						"description": "Maximum number of conflict groups to return",
					},
				},
			},
		},
		{
			Name:        "list_canonical_knowledge",
			Description: "List canonical knowledge entries projected from confirmed memories",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"episodic", "semantic", "procedural", "working", "all"},
						"description": "Optional memory type filter",
						"default":     "all",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Optional project or task context",
					},
					"service": map[string]any{
						"type":        "string",
						"description": "Optional service or component filter",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional tags that canonical entries must contain",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     50,
						"default":     10,
						"description": "Maximum number of canonical entries to return",
					},
				},
			},
		},
		{
			Name:        "recall_canonical_knowledge",
			Description: "Recall canonical knowledge only, excluding raw memories from the result set",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "What canonical knowledge to retrieve",
					},
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"episodic", "semantic", "procedural", "working", "all"},
						"description": "Optional memory type filter",
						"default":     "all",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Optional project or task context",
					},
					"service": map[string]any{
						"type":        "string",
						"description": "Optional service or component filter",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional tags that canonical entries must contain",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     50,
						"default":     10,
						"description": "Maximum number of canonical entries to return",
					},
				},
				"required": []string{"query"},
			},
		},
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
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string", "description": "Raw session summary text to review"},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"coding", "incident", "migration", "research", "cleanup"},
						"default":     "coding",
						"description": "Optional session mode",
					},
					"context":    map[string]any{"type": "string", "description": "Optional project, task, or workflow context"},
					"service":    map[string]any{"type": "string", "description": "Optional service or component name"},
					"started_at": map[string]any{"type": "string", "description": "Optional RFC3339 session start timestamp"},
					"ended_at":   map[string]any{"type": "string", "description": "Optional RFC3339 session end timestamp"},
					"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tags"},
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
				},
				"required": []string{"summary"},
			},
		},
		{
			Name:        "accept_session_changes",
			Description: "Persist the raw session summary, auto-apply low-risk session changes, and return the remaining review backlog",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{"type": "string", "description": "Raw session summary text to apply"},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"coding", "incident", "migration", "research", "cleanup"},
						"default":     "coding",
						"description": "Optional session mode",
					},
					"context":    map[string]any{"type": "string", "description": "Optional project, task, or workflow context"},
					"service":    map[string]any{"type": "string", "description": "Optional service or component name"},
					"started_at": map[string]any{"type": "string", "description": "Optional RFC3339 session start timestamp"},
					"ended_at":   map[string]any{"type": "string", "description": "Optional RFC3339 session end timestamp"},
					"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tags"},
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
				},
				"required": []string{"summary"},
			},
		},
		{
			Name:        "resolve_review_item",
			Description: "Resolve a pending review queue item so it disappears from the active inbox while keeping an audit trail",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Review queue memory ID to resolve",
					},
					"resolution": map[string]any{
						"type":        "string",
						"enum":        []string{"resolved", "dismissed", "deferred"},
						"default":     "resolved",
						"description": "How this review item was handled",
					},
					"note": map[string]any{
						"type":        "string",
						"description": "Optional resolution note for the audit trail",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Optional owner or reviewer that handled this item",
					},
					"format": map[string]any{
						"type":        "string",
						"enum":        []string{"text", "json"},
						"default":     "text",
						"description": "Return a human-readable report or structured JSON",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "end_task",
			Description: "Consolidate working memories tied to a specific archived task slug: mark low-importance ones as outdated and queue high-importance ones for review (does NOT auto-promote)",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"context_slug": map[string]any{
						"type":        "string",
						"description": "Task slug whose working memories should be consolidated (must exist under a configured archive root)",
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "Preview actions without modifying memories",
					},
					"roots": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional override for archive roots (falls back to MCP_TASK_ARCHIVE_ROOTS)",
					},
					"promotion_threshold": map[string]any{
						"type":        "number",
						"minimum":     0,
						"maximum":     1,
						"default":     0.7,
						"description": "Importance at/above which a memory becomes a promotion candidate instead of being marked outdated",
					},
					"keep_tag": map[string]any{
						"type":        "string",
						"default":     "keep-after-archive",
						"description": "Tag that opts a memory out of the sweep",
					},
					"format": map[string]any{
						"type":        "string",
						"enum":        []string{"text", "json"},
						"default":     "text",
						"description": "Return a human-readable summary or structured JSON",
					},
				},
				"required": []string{"context_slug"},
			},
		},
		{
			Name:        "sweep_archive",
			Description: "Enumerate all archived task slugs and consolidate their working memories in one pass (pull-mode; iterates MCP_TASK_ARCHIVE_ROOTS)",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"roots": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional override for archive roots (falls back to MCP_TASK_ARCHIVE_ROOTS)",
					},
					"slug_pattern": map[string]any{
						"type":        "string",
						"description": "Optional regex that slug basenames must match (falls back to MCP_TASK_SLUG_PATTERN)",
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "Preview actions without modifying memories",
					},
					"promotion_threshold": map[string]any{
						"type":        "number",
						"minimum":     0,
						"maximum":     1,
						"default":     0.7,
						"description": "Importance at/above which a memory becomes a promotion candidate",
					},
					"keep_tag": map[string]any{
						"type":        "string",
						"default":     "keep-after-archive",
						"description": "Tag that opts a memory out of the sweep",
					},
					"format": map[string]any{
						"type":        "string",
						"enum":        []string{"text", "json"},
						"default":     "text",
						"description": "Return a human-readable summary or structured JSON",
					},
				},
			},
		},
		{
			Name:        "resolve_review_queue",
			Description: "Bulk-resolve pending review queue items by IDs or filter criteria, with optional dry-run to preview which items would be affected",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Explicit list of review queue memory IDs to resolve; if omitted, uses filter criteria instead",
					},
					"resolution": map[string]any{
						"type":        "string",
						"enum":        []string{"resolved", "dismissed", "deferred"},
						"default":     "resolved",
						"description": "How the matched review items should be handled",
					},
					"note": map[string]any{
						"type":        "string",
						"description": "Optional resolution note for the audit trail",
					},
					"owner": map[string]any{
						"type":        "string",
						"description": "Optional owner or reviewer that handled these items",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Filter by project or task context",
					},
					"service": map[string]any{
						"type":        "string",
						"description": "Filter by service name",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Filter by tags",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     100,
						"default":     20,
						"description": "Maximum number of items to resolve in one call",
					},
					"dry_run": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "Preview which items would be resolved without actually changing them",
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
		{
			Name:        "store_decision",
			Description: "Store an engineering decision with rationale and consequences",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":        map[string]any{"type": "string", "description": "Short title for the decision"},
					"decision":     map[string]any{"type": "string", "description": "What was decided"},
					"rationale":    map[string]any{"type": "string", "description": "Why the decision was made"},
					"consequences": map[string]any{"type": "string", "description": "Expected impact or tradeoffs"},
					"context":      map[string]any{"type": "string", "description": "Project, task, or service context"},
					"service":      map[string]any{"type": "string", "description": "Service or component name"},
					"owner":        map[string]any{"type": "string", "description": "Decision owner"},
					"status":       map[string]any{"type": "string", "description": "Decision status, for example proposed or accepted"},
					"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"importance":   map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": 0.85},
				},
				"required": []string{"decision"},
			},
		},
		{
			Name:        "store_incident",
			Description: "Store an incident record with impact, root cause, and resolution",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":      map[string]any{"type": "string", "description": "Short incident title"},
					"summary":    map[string]any{"type": "string", "description": "Incident summary"},
					"impact":     map[string]any{"type": "string", "description": "What was affected"},
					"root_cause": map[string]any{"type": "string", "description": "Known root cause"},
					"resolution": map[string]any{"type": "string", "description": "How it was mitigated or resolved"},
					"context":    map[string]any{"type": "string", "description": "Project, task, or service context"},
					"service":    map[string]any{"type": "string", "description": "Service or component name"},
					"severity":   map[string]any{"type": "string", "description": "Severity label such as sev1 or sev2"},
					"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"importance": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": 0.9},
				},
				"required": []string{"summary"},
			},
		},
		{
			Name:        "store_runbook",
			Description: "Store a runbook entry with procedure, rollback, and verification steps",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":        map[string]any{"type": "string", "description": "Runbook title"},
					"procedure":    map[string]any{"type": "string", "description": "Main procedure or step list"},
					"trigger":      map[string]any{"type": "string", "description": "When to use this runbook"},
					"verification": map[string]any{"type": "string", "description": "How to verify success"},
					"rollback":     map[string]any{"type": "string", "description": "How to roll back if needed"},
					"context":      map[string]any{"type": "string", "description": "Project, task, or service context"},
					"service":      map[string]any{"type": "string", "description": "Service or component name"},
					"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"importance":   map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": 0.85},
				},
				"required": []string{"procedure"},
			},
		},
		{
			Name:        "store_postmortem",
			Description: "Store a postmortem with summary, root cause, and follow-up actions",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":        map[string]any{"type": "string", "description": "Postmortem title"},
					"summary":      map[string]any{"type": "string", "description": "Postmortem summary"},
					"impact":       map[string]any{"type": "string", "description": "Operational impact"},
					"root_cause":   map[string]any{"type": "string", "description": "Root cause analysis"},
					"action_items": map[string]any{"type": "string", "description": "Follow-up action items"},
					"follow_up":    map[string]any{"type": "string", "description": "Next verification or rollout notes"},
					"context":      map[string]any{"type": "string", "description": "Project, task, or service context"},
					"service":      map[string]any{"type": "string", "description": "Service or component name"},
					"severity":     map[string]any{"type": "string", "description": "Severity label such as sev1 or sev2"},
					"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"importance":   map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": 0.85},
				},
				"required": []string{"summary"},
			},
		},
		{
			Name:        "search_runbooks",
			Description: "Search runbook memories and indexed runbook docs for operational fixes",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "What you need to fix or verify"},
					"context": map[string]any{"type": "string", "description": "Optional project or task context"},
					"service": map[string]any{"type": "string", "description": "Optional service or component name"},
					"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 5},
					"debug":   map[string]any{"type": "boolean", "default": false, "description": "Include explainable retrieval output for indexed docs"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "recall_similar_incidents",
			Description: "Recall similar incidents and postmortems from stored operational history",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "Incident symptoms, service, or failure mode"},
					"context": map[string]any{"type": "string", "description": "Optional project or task context"},
					"service": map[string]any{"type": "string", "description": "Optional service or component name"},
					"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 5},
					"debug":   map[string]any{"type": "boolean", "default": false, "description": "Include explainable retrieval output for indexed postmortem docs"},
				},
				"required": []string{"query"},
			},
		},
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
			Description: "Show a structured project bank view for canonical knowledge, decisions, runbooks, incidents, caveats, migrations, or the review queue",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"view": map[string]any{
						"type":        "string",
						"enum":        []string{"canonical_overview", "overview", "decisions", "runbooks", "incidents", "caveats", "migrations", "review_queue"},
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
		{
			Name:        "recall_as_of",
			Description: "Retrieve knowledge that was valid at a specific point in time, filtering by temporal validity (valid_from/valid_until)",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "Search query"},
					"as_of":   map[string]any{"type": "string", "description": "RFC3339 timestamp — retrieve knowledge valid at this time"},
					"context": map[string]any{"type": "string", "description": "Optional context filter"},
					"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
					"format":  map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
				},
				"required": []string{"query", "as_of"},
			},
		},
		{
			Name:        "knowledge_timeline",
			Description: "Show the chronological evolution of knowledge on a topic — how entries were created, superseded, and replaced over time",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "Topic to trace"},
					"context": map[string]any{"type": "string", "description": "Optional context filter"},
					"service": map[string]any{"type": "string", "description": "Optional service filter"},
					"format":  map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
				},
				"required": []string{"query"},
			},
		},
	}

	// Steward tools — only registered when steward service is enabled.
	if s.stewardService != nil {
		tools = append(tools,
			tool{
				Name:        "steward_run",
				Description: "Run a knowledge stewardship cycle: scan for duplicates, conflicts, stale entries, and canonical promotion candidates",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"scope": map[string]any{
							"type":        "string",
							"enum":        []string{"full", "duplicates", "conflicts", "stale", "canonical"},
							"default":     "full",
							"description": "Which scanners to run",
						},
						"dry_run": map[string]any{
							"type":        "boolean",
							"default":     true,
							"description": "If true, only report findings without applying any changes",
						},
						"context": map[string]any{
							"type":        "string",
							"description": "Optional context filter to limit the scan scope",
						},
						"service": map[string]any{
							"type":        "string",
							"description": "Optional service filter to limit the scan scope",
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
			tool{
				Name:        "steward_report",
				Description: "Retrieve the latest stewardship report or a specific one by run ID",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"run_id": map[string]any{
							"type":        "string",
							"description": "Optional run ID; defaults to the latest report",
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
			tool{
				Name:        "steward_policy",
				Description: "Get or update the stewardship policy that controls detection thresholds, auto-apply rules, and scheduling",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action": map[string]any{
							"type":        "string",
							"enum":        []string{"get", "set"},
							"default":     "get",
							"description": "Get the current policy or set a new one",
						},
						"policy": map[string]any{
							"type":        "object",
							"description": "The new policy object (required when action=set)",
						},
					},
				},
			},
			tool{
				Name:        "steward_status",
				Description: "Show current stewardship status: policy mode, last run summary, pending review count, next scheduled run",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"format": map[string]any{
							"type":        "string",
							"enum":        []string{"text", "json"},
							"default":     "text",
							"description": "Return a human-readable summary or structured JSON",
						},
					},
				},
			},
			tool{
				Name:        "drift_scan",
				Description: "Compare memory entries against live sources (repo files, docs) to detect drift, missing references, and stale unverified knowledge",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"scope":   map[string]any{"type": "string", "enum": []string{"all", "canonical", "decisions", "runbooks"}, "default": "all", "description": "Which memories to scan"},
						"context": map[string]any{"type": "string", "description": "Optional context filter"},
						"service": map[string]any{"type": "string", "description": "Optional service filter"},
						"format":  map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
					},
				},
			},
			tool{
				Name:        "verification_candidates",
				Description: "List memories that need verification, ranked by urgency — never verified, stale, low confidence, or verification failed",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20},
						"scope":        map[string]any{"type": "string", "enum": []string{"all", "canonical", "decisions", "runbooks"}, "default": "all"},
						"min_age_days": map[string]any{"type": "integer", "description": "Only entries older than N days"},
						"context":      map[string]any{"type": "string", "description": "Optional context filter"},
						"service":      map[string]any{"type": "string", "description": "Optional service filter"},
						"format":       map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
					},
				},
			},
			tool{
				Name:        "verify_entry",
				Description: "Mark a memory as verified, updating its verification metadata (timestamp, method, status)",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"memory_id": map[string]any{"type": "string", "description": "ID of the memory to verify"},
						"method":    map[string]any{"type": "string", "enum": []string{"manual", "source_check", "repo_scan", "agent_verified"}, "default": "manual"},
						"status":    map[string]any{"type": "string", "enum": []string{"verified", "verification_failed", "needs_update"}, "default": "verified"},
						"note":      map[string]any{"type": "string", "description": "Optional note about what was checked"},
					},
					"required": []string{"memory_id"},
				},
			},
			tool{
				Name:        "steward_inbox",
				Description: "List stewardship inbox items — review-required actions from maintenance runs, drift scans, and session consolidation",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"status":  map[string]any{"type": "string", "enum": []string{"pending", "resolved", "deferred", "all"}, "default": "pending"},
						"kind":    map[string]any{"type": "string", "description": "Filter by item kind (duplicate_candidate, drift_detected, etc.)"},
						"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20},
						"sort_by": map[string]any{"type": "string", "enum": []string{"urgency", "created_at", "confidence"}, "default": "urgency"},
						"format":  map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
					},
				},
			},
			tool{
				Name:        "steward_inbox_resolve",
				Description: "Resolve a steward inbox item by applying an action: merge, mark_outdated, promote, verify, suppress, or defer",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"item_id": map[string]any{"type": "string", "description": "ID of the inbox item to resolve"},
						"action":  map[string]any{"type": "string", "enum": []string{"merge", "mark_outdated", "mark_superseded", "promote", "verify", "suppress", "defer"}, "description": "Resolution action"},
						"note":    map[string]any{"type": "string", "description": "Optional note explaining the resolution"},
					},
					"required": []string{"item_id", "action"},
				},
			},
		)
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
	return map[string]any{"tools": filtered}, nil
}

var ragTools = map[string]bool{
	"semantic_search":  true,
	"index_documents":  true,
}

var memoryTools = map[string]bool{
	"store_memory":               true,
	"recall_memory":              true,
	"update_memory":              true,
	"delete_memory":              true,
	"list_memories":              true,
	"memory_stats":               true,
	"merge_duplicates":           true,
	"mark_outdated":              true,
	"promote_to_canonical":       true,
	"conflicts_report":           true,
	"list_canonical_knowledge":   true,
	"recall_canonical_knowledge": true,
	"close_session":              true,
	"analyze_session":            true,
	"review_session_changes":     true,
	"accept_session_changes":     true,
	"resolve_review_item":        true,
	"resolve_review_queue":       true,
	"end_task":                   true,
	"sweep_archive":              true,
	"store_decision":             true,
	"store_incident":             true,
	"store_runbook":              true,
	"store_postmortem":           true,
	"project_bank_view":          true,
	"recall_as_of":               true,
	"knowledge_timeline":         true,
}

// hybridTools require at least one of memoryStore or ragEngine.
var hybridTools = map[string]bool{
	"search_runbooks":           true,
	"recall_similar_incidents":  true,
	"summarize_project_context": true,
}

type toolHandler func(args map[string]any) (any, *rpcError)

func (s *MCPServer) buildToolHandlers() map[string]toolHandler {
	return map[string]toolHandler{
		"repo_list":                  s.callRepoList,
		"repo_read":                  s.callRepoRead,
		"repo_search":                s.callRepoSearch,
		"semantic_search":            s.callSemanticSearch,
		"index_documents":            s.callIndexDocuments,
		"store_memory":               s.callStoreMemory,
		"recall_memory":              s.callRecallMemory,
		"update_memory":              s.callUpdateMemory,
		"delete_memory":              s.callDeleteMemory,
		"list_memories":              s.callListMemories,
		"memory_stats":               s.callMemoryStats,
		"merge_duplicates":           s.callMergeDuplicates,
		"mark_outdated":              s.callMarkOutdated,
		"promote_to_canonical":       s.callPromoteToCanonical,
		"conflicts_report":           s.callConflictsReport,
		"list_canonical_knowledge":   s.callListCanonicalKnowledge,
		"recall_canonical_knowledge": s.callRecallCanonicalKnowledge,
		"close_session":              s.callCloseSession,
		"analyze_session":            s.callCloseSession,
		"review_session_changes":     s.callReviewSessionChanges,
		"accept_session_changes":     s.callAcceptSessionChanges,
		"resolve_review_item":        s.callResolveReviewItem,
		"resolve_review_queue":       s.callResolveReviewQueue,
		"end_task":                   s.callEndTask,
		"sweep_archive":              s.callSweepArchive,
		"store_decision":             s.callStoreDecision,
		"store_incident":             s.callStoreIncident,
		"store_runbook":              s.callStoreRunbook,
		"store_postmortem":           s.callStorePostmortem,
		"search_runbooks":            s.callSearchRunbooks,
		"recall_similar_incidents":   s.callRecallSimilarIncidents,
		"summarize_project_context":  s.callSummarizeProjectContext,
		"project_bank_view":          s.callProjectBankView,
		"steward_run":                s.callStewardRun,
		"steward_report":             s.callStewardReport,
		"steward_policy":             s.callStewardPolicy,
		"steward_status":             s.callStewardStatus,
		"drift_scan":                 s.callDriftScan,
		"verification_candidates":    s.callVerificationCandidates,
		"verify_entry":               s.callVerifyEntry,
		"steward_inbox":              s.callStewardInbox,
		"steward_inbox_resolve":      s.callStewardInboxResolve,
		"recall_as_of":               s.callRecallAsOf,
		"knowledge_timeline":         s.callKnowledgeTimeline,
	}
}

func (s *MCPServer) handleToolsCall(params json.RawMessage) (any, *rpcError) {
	start := time.Now()
	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		rErr := &rpcError{Code: rpcErrInvalidParams, Message: "invalid params", Data: err.Error()}
		s.logToolEvent("", nil, start, rErr)
		return nil, rErr
	}
	if req.Name == "" {
		rErr := &rpcError{Code: rpcErrInvalidParams, Message: "tool name is required"}
		s.logToolEvent("", req.Arguments, start, rErr)
		return nil, rErr
	}

	handler, ok := s.toolHandlers[req.Name]
	if !ok {
		rErr := &rpcError{Code: rpcErrMethodNotFound, Message: fmt.Sprintf("unknown tool: %s", req.Name)}
		s.logToolEvent(req.Name, req.Arguments, start, rErr)
		return nil, rErr
	}

	result, rErr := handler(req.Arguments)
	s.logToolEvent(req.Name, req.Arguments, start, rErr)
	if s.sessionTracker != nil {
		s.sessionTracker.HandleToolCall(req.Name, req.Arguments, rErr)
	}
	return result, rErr
}

func (s *MCPServer) logToolEvent(name string, args map[string]any, start time.Time, rErr *rpcError) {
	if s.stats == nil {
		return
	}
	event := stats.Event{
		EventName:  "tool_call",
		Method:     "tools/call",
		Tool:       name,
		DurationMs: time.Since(start).Milliseconds(),
		Success:    rErr == nil,
	}
	if rErr != nil {
		event.Error = rErr.Message
	}
	if path, ok := getString(args, "path"); ok {
		event.Path = path
	}
	if query, ok := getString(args, "query"); ok {
		event.QueryLength = len(query)
	}
	if maxResults, ok := getInt(args, "max_results"); ok {
		event.MaxResults = maxResults
	}
	if maxBytes, ok := getInt64(args, "max_bytes"); ok {
		event.MaxBytes = maxBytes
	}
	if maxDepth, ok := getInt(args, "max_depth"); ok {
		event.MaxDepth = maxDepth
	}
	s.stats.Log(event)
}

func parseParams[T any](args map[string]any) (T, error) {
	var result T
	data, err := json.Marshal(args)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	return result, nil
}

func getString(args map[string]any, key string) (string, bool) {
	val, ok := args[key]
	if !ok {
		return "", false
	}
	switch typed := val.(type) {
	case string:
		return typed, true
	default:
		return fmt.Sprintf("%v", typed), true
	}
}

func getInt(args map[string]any, key string) (int, bool) {
	val, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := val.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func getInt64(args map[string]any, key string) (int64, bool) {
	val, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := val.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func getBool(args map[string]any, key string) (bool, bool) {
	val, ok := args[key]
	if !ok {
		return false, false
	}
	switch typed := val.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y":
			return true, true
		case "false", "0", "no", "n":
			return false, true
		}
	}
	return false, false
}

func getStringSlice(args map[string]any, key string) []string {
	val, ok := args[key]
	if !ok {
		return nil
	}

	switch typed := val.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
				result = append(result, strings.TrimSpace(str))
			}
		}
		return result
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		parts := strings.Split(typed, ",")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, part)
			}
		}
		return result
	default:
		return nil
	}
}

func (s *MCPServer) requireMemoryStore() *rpcError {
	if s.memoryStore == nil {
		return &rpcError{Code: rpcErrServerError, Message: "Memory store not available"}
	}
	return nil
}

func (s *MCPServer) requireRAGEngine() *rpcError {
	if s.getRagEngine() == nil {
		return &rpcError{Code: rpcErrServerError, Message: "RAG engine not available"}
	}
	return nil
}

// RAG tool implementations
