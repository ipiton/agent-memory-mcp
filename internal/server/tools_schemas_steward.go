package server

func temporalToolDefs() []tool {
	return []tool{
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
		// Sedimentation tools (T48).
		{
			Name:        "promote_sediment",
			Description: "Move a memory to the specified sediment layer (surface, episodic, semantic, or character). Used by reviewers to accept sediment-cycle proposals.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "Memory ID to promote"},
					"target_layer": map[string]any{
						"type":        "string",
						"enum":        []string{"surface", "episodic", "semantic", "character"},
						"description": "Target sediment layer",
					},
					"format": map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
				},
				"required": []string{"id", "target_layer"},
			},
		},
		{
			Name:        "demote_sediment",
			Description: "Demote a memory one sediment layer toward surface. No-op if already at surface.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "string", "description": "Memory ID to demote"},
					"format": map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
				},
				"required": []string{"id"},
			},
		},
		{
			Name:        "sediment_cycle",
			Description: "Run the sediment-cycle job: auto-apply trivial transitions (surface→episodic by age) and queue non-trivial ones for review",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dry_run":    map[string]any{"type": "boolean", "default": false, "description": "Preview only; AutoApplied and ReviewQueued in result count proposed transitions, not mutations"},
					"since_days": map[string]any{"type": "integer", "minimum": 0, "description": "Only consider memories OLDER than N days (0 = all). Useful for limiting cycle scope to stable memories."},
					"limit":      map[string]any{"type": "integer", "minimum": 0, "description": "Cap on transitions per run (0 = no limit)"},
					"format":     map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
				},
			},
		},
		{
			Name:        "recount_references",
			Description: "Backfill referenced_by_count metadata from existing data (avoided_dead_end_id and superseded_by edges). Idempotent; feeds the T48 semantic→character 'by refs' promotion rule.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dry_run": map[string]any{"type": "boolean", "default": false, "description": "Preview changes without writing. Updated counts rows that would change."},
					"format":  map[string]any{"type": "string", "enum": []string{"text", "json"}, "default": "text"},
				},
			},
		},
	}
}

func stewardToolDefs() []tool {
	return []tool{
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
			Description: "Resolve a steward inbox item, executing the action against its target memories before marking it resolved. Actions: merge (merge duplicate target_ids into the first), mark_outdated / mark_superseded (mark first target outdated; superseded links to the second target), promote (promote first target to canonical), verify (stamp last_verified_at on first target), suppress (dismiss a false positive, no target change), defer (postpone, no target change). A failed action leaves the item pending.",
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
	}
}
