package server

import (
	"fmt"
	"strings"
)

// T67: opt-in MCP tool grouping.
//
// High-tool-count MCP servers pay a large token cost at initialize time: every
// tool's full JSON schema is loaded into the client's context before the first
// user message. Grouping collapses the core, high-traffic agent tools into a
// handful of "meta-tools", each dispatching by a required `action` discriminator,
// so the discovery payload shrinks dramatically.
//
// Grouping is purely a discovery-surface transform:
//   - tools/list output depends on config.ToolGrouping.
//   - tools/call ALWAYS accepts BOTH the legacy tool name (e.g. store_memory) and
//     the grouped form (e.g. memory + action=store), regardless of the flag — so
//     existing clients and user scripts on legacy names never break.
//
// Only the core toolset is grouped (groupedSpecs below). Administrative, steward,
// and sediment tools that a high-volume agent rarely lists stay individual and
// pass through unchanged. Meta-tool names are chosen to never collide with any
// legacy tool name.

// groupAction maps a discriminator value to the legacy tool it dispatches to.
type groupAction struct {
	Action string
	Legacy string
}

// toolGroupSpec declares one meta-tool: its name, description, and the ordered
// set of actions it exposes.
type toolGroupSpec struct {
	Name        string
	Description string
	Actions     []groupAction
}

// groupedSpecs is the static grouping definition (T67 design). Each Legacy name
// must exist in the flat tool registry; TestGroupedSchemaIntegrity enforces it.
var groupedSpecs = []toolGroupSpec{
	{
		Name:        "repo",
		Description: "Repository file access. Set action to list, read, or search.",
		Actions: []groupAction{
			{"list", "repo_list"},
			{"read", "repo_read"},
			{"search", "repo_search"},
		},
	},
	{
		Name:        "memory",
		Description: "Core memory operations. Set action to select the operation (see the action enum).",
		Actions: []groupAction{
			{"store", "store_memory"},
			{"recall", "recall_memory"},
			{"update", "update_memory"},
			{"delete", "delete_memory"},
			{"list", "list_memories"},
			{"stats", "memory_stats"},
		},
	},
	{
		Name:        "memory_admin",
		Description: "Memory maintenance operations. Set action to select the maintenance verb (see the action enum).",
		Actions: []groupAction{
			{"merge_duplicates", "merge_duplicates"},
			{"mark_outdated", "mark_outdated"},
			{"promote_to_canonical", "promote_to_canonical"},
			{"demote_sediment", "demote_sediment"},
			{"verify_entry", "verify_entry"},
			{"recount_references", "recount_references"},
			{"conflicts_report", "conflicts_report"},
			{"promote_sediment", "promote_sediment"},
			{"sediment_cycle", "sediment_cycle"},
		},
	},
	{
		Name:        "engineering",
		Description: "Store an engineering artifact. Set action to decision, runbook, incident, postmortem, or dead_end.",
		Actions: []groupAction{
			{"decision", "store_decision"},
			{"runbook", "store_runbook"},
			{"incident", "store_incident"},
			{"postmortem", "store_postmortem"},
			{"dead_end", "store_dead_end"},
		},
	},
	{
		Name:        "search",
		Description: "Retrieval operations. Set action to select the retrieval mode (see the action enum).",
		Actions: []groupAction{
			{"semantic", "semantic_search"},
			{"canonical", "recall_canonical_knowledge"},
			{"list_canonical", "list_canonical_knowledge"},
			{"multihop", "recall_multihop"},
			{"as_of", "recall_as_of"},
			{"similar_incidents", "recall_similar_incidents"},
			{"runbooks", "search_runbooks"},
			{"summarize_project", "summarize_project_context"},
		},
	},
	{
		Name:        "session",
		Description: "Session and task-lifecycle operations. Set action to select the lifecycle step (see the action enum).",
		Actions: []groupAction{
			{"end_task", "end_task"},
			{"sweep_archive", "sweep_archive"},
			{"analyze", "analyze_session"},
			{"accept", "accept_session_changes"},
			{"review", "review_session_changes"},
			{"close", "close_session"},
			{"timeline", "knowledge_timeline"},
			{"resolve_review_item", "resolve_review_item"},
			{"resolve_review_queue", "resolve_review_queue"},
		},
	},
}

// groupByName indexes groupedSpecs by meta-tool name for O(1) dispatch lookup.
var groupByName = func() map[string]toolGroupSpec {
	m := make(map[string]toolGroupSpec, len(groupedSpecs))
	for _, g := range groupedSpecs {
		m[g.Name] = g
	}
	return m
}()

// legacyFor returns the legacy tool name for an action value.
func (g toolGroupSpec) legacyFor(action string) (string, bool) {
	for _, a := range g.Actions {
		if a.Action == action {
			return a.Legacy, true
		}
	}
	return "", false
}

// actionNames returns the ordered action discriminator values.
func (g toolGroupSpec) actionNames() []string {
	names := make([]string, len(g.Actions))
	for i, a := range g.Actions {
		names[i] = a.Action
	}
	return names
}

// buildGroupedList transforms an availability-filtered flat tool list into the
// grouped surface: each spec becomes one meta-tool built from its available
// members, and every tool not claimed by a group passes through unchanged
// (singletons like index_documents/project_bank_view, admin/steward tools).
func buildGroupedList(available []tool) []tool {
	byName := make(map[string]tool, len(available))
	for _, t := range available {
		byName[t.Name] = t
	}

	claimed := make(map[string]bool)
	out := make([]tool, 0, len(available))

	for _, g := range groupedSpecs {
		var members []tool
		var actions []groupAction
		for _, a := range g.Actions {
			mt, ok := byName[a.Legacy]
			if !ok {
				continue // member unavailable in this config
			}
			claimed[a.Legacy] = true
			members = append(members, mt)
			actions = append(actions, a)
		}
		if len(members) == 0 {
			continue // whole group unavailable — emit nothing
		}
		out = append(out, buildGroupedTool(g, members, actions))
	}

	// Pass through anything not folded into a group, preserving original order.
	for _, t := range available {
		if !claimed[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// buildGroupedTool synthesizes a single meta-tool schema from its member tools.
// It is DRY by construction: the `action` enum and the merged property set are
// derived from the members' own canonical schemas — never duplicated.
func buildGroupedTool(g toolGroupSpec, members []tool, actions []groupAction) tool {
	props := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        actionEnum(actions),
			"description": actionDescription(actions, members),
		},
	}
	// Merge each member's properties. First writer wins on name collisions; no
	// grouped member uses `action` (guarded by TestGroupedSchemaIntegrity), so
	// the discriminator above is never shadowed.
	for _, m := range members {
		mp, _ := m.InputSchema["properties"].(map[string]any)
		for k, v := range mp {
			if k == "action" {
				continue
			}
			if _, exists := props[k]; !exists {
				props[k] = v
			}
		}
	}
	return tool{
		Name:        g.Name,
		Description: g.Description,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": props,
			"required":   []string{"action"},
		},
	}
}

func actionEnum(actions []groupAction) []string {
	enum := make([]string, len(actions))
	for i, a := range actions {
		enum[i] = a.Action
	}
	return enum
}

// actionDescription folds each member tool's own description into the enum help
// so the model retains per-action guidance despite the collapsed surface.
func actionDescription(actions []groupAction, members []tool) string {
	var b strings.Builder
	b.WriteString("Required. Operation to perform:")
	for i, a := range actions {
		desc := strings.TrimSpace(members[i].Description)
		fmt.Fprintf(&b, "\n- %s: %s", a.Action, desc)
	}
	return b.String()
}

// resolveGroupedToolCall translates a grouped meta-tool call into its legacy
// tool name. Returns matched=false when name is not a meta-tool (the caller then
// dispatches by name unchanged). When matched, it either returns the resolved
// legacy name or a client-facing error (missing/unknown action). On success the
// `action` key is stripped from args so the legacy handler sees its native shape.
func resolveGroupedToolCall(name string, args map[string]any) (legacy string, rErr *rpcError, matched bool) {
	g, ok := groupByName[name]
	if !ok {
		return "", nil, false
	}
	action, ok := getString(args, "action")
	if !ok || strings.TrimSpace(action) == "" {
		return "", &rpcError{
			Code:    rpcErrInvalidParams,
			Message: fmt.Sprintf("tool %q requires an \"action\" argument; one of: %s", name, strings.Join(g.actionNames(), ", ")),
		}, true
	}
	legacy, ok = g.legacyFor(action)
	if !ok {
		return "", &rpcError{
			Code:    rpcErrInvalidParams,
			Message: fmt.Sprintf("unknown action %q for tool %q; valid actions: %s", action, name, strings.Join(g.actionNames(), ", ")),
		}, true
	}
	delete(args, "action")
	return legacy, nil, true
}
