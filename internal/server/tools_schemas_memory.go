package server

func memoryToolDefs() []tool {
	return []tool{
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
			Name:        "recall_multihop",
			Description: "Recall memories via multi-hop knowledge-graph walk (T50). Seeds via semantic recall, expands through (subj, rel, obj) triples up to MaxHops with damping, returns memories ranked by aggregated path score plus the chain of triples that earned each result. Use for cross-memory reasoning queries (\"why did we choose X — what feedback led to it, what incidents confirmed?\") that single-hop search cannot trace.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Free-text query that anchors the graph walk via semantic seed recall.",
					},
					"max_hops": map[string]any{
						"type":        "integer",
						"description": "BFS depth bound (1-4, default 2). Higher hops capture more distant evidence at the cost of precision.",
						"minimum":     1,
						"maximum":     4,
						"default":     2,
					},
					"seed_k": map[string]any{
						"type":        "integer",
						"description": "How many memories drive the seed-entity selection (1-20, default 5).",
						"minimum":     1,
						"maximum":     20,
						"default":     5,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of memories to return (default 10, capped at 50).",
						"minimum":     1,
						"maximum":     50,
						"default":     10,
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Optional context filter applied to the seed recall step.",
					},
				},
				"required": []string{"query"},
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
	}
}
