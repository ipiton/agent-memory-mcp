package server

func workflowToolDefs() []tool {
	return []tool{
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
					"auto_promote": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "T62: promote candidates directly to canonical instead of creating review-queue items (autonomous mode)",
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
					"auto_promote": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "T62: promote candidates directly to canonical instead of creating review-queue items (autonomous mode)",
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
	}
}

func engineeringToolDefs() []tool {
	return []tool{
		{
			Name:        "store_decision",
			Description: "Store an engineering decision with rationale and consequences",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":               map[string]any{"type": "string", "description": "Short title for the decision"},
					"decision":            map[string]any{"type": "string", "description": "What was decided"},
					"rationale":           map[string]any{"type": "string", "description": "Why the decision was made"},
					"consequences":        map[string]any{"type": "string", "description": "Expected impact or tradeoffs"},
					"context":             map[string]any{"type": "string", "description": "Project, task, or service context"},
					"service":             map[string]any{"type": "string", "description": "Service or component name"},
					"owner":               map[string]any{"type": "string", "description": "Decision owner"},
					"status":              map[string]any{"type": "string", "description": "Decision status, for example proposed or accepted"},
					"tags":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"importance":          map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": 0.85},
					"avoided_dead_end_id": map[string]any{"type": "string", "description": "Optional ID of a previously recorded dead_end this decision avoids"},
				},
				"required": []string{"decision"},
			},
		},
		{
			Name:        "store_dead_end",
			Description: "Store an abandoned approach with its failure rationale so future agents can avoid repeating it",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":              map[string]any{"type": "string", "description": "Short title for the dead end"},
					"attempted_approach": map[string]any{"type": "string", "description": "Short description of the failed attempt"},
					"why_failed":         map[string]any{"type": "string", "description": "Root cause of the failure"},
					"alternative_used":   map[string]any{"type": "string", "description": "What was used instead"},
					"related_task_slug":  map[string]any{"type": "string", "description": "Related task slug or session ID"},
					"context":            map[string]any{"type": "string", "description": "Project, task, or service context"},
					"service":            map[string]any{"type": "string", "description": "Service or component name"},
					"tags":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"},
					"importance":         map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": 0.80},
				},
				"required": []string{"attempted_approach", "why_failed"},
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
	}
}
