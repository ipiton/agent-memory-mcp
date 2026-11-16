package server

// T183: MCP tool annotations. Without them a client (Claude Code, Codex) sees
// delete_memory and recall_memory as the same kind of tool, so the only way to
// ask for confirmation on the dangerous ones is to list their names by hand.
// The MCP spec defaults an unannotated tool to readOnlyHint=false and
// destructiveHint=true, which is wrong both ways: every recall would look
// destructive, and a client that trusts the defaults learns nothing. Every tool
// therefore carries all four hints explicitly.

// toolAnnotations is the MCP `annotations` object of a tool definition. The
// hints are advisory: they let a client gate the tool, the server does not
// enforce anything from them.
type toolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

// Every tool works on the local memory bank, the local RAG index or allowlisted
// local files, so none is open-world.
var (
	// readOnlyTool changes nothing.
	readOnlyTool = toolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	// additiveTool adds entries or adjusts metadata that another tool can set
	// back (status, layer, verification stamp, review resolution).
	additiveTool = toolAnnotations{}
	// idempotentTool is additiveTool that converges: a repeat call with the
	// same arguments changes nothing further.
	idempotentTool = toolAnnotations{IdempotentHint: true}
	// destructiveTool deletes, merges or archives entries, or overwrites their
	// content, so what was there cannot be had back through the tools.
	destructiveTool = toolAnnotations{DestructiveHint: true}
	// idempotentDestructiveTool is destructiveTool whose repeat is a no-op.
	idempotentDestructiveTool = toolAnnotations{DestructiveHint: true, IdempotentHint: true}
)

// toolSafety classifies every flat tool. TestEveryToolIsAnnotated keeps it in
// step with the tool registry in both directions, so a new tool cannot ship
// without a decision here.
var toolSafety = map[string]toolAnnotations{
	// Repository and RAG.
	"repo_list":        readOnlyTool,
	"repo_read":        readOnlyTool,
	"repo_search":      readOnlyTool,
	"semantic_search":  readOnlyTool,
	"document_context": readOnlyTool,
	"index_documents":  idempotentTool, // rebuilds a derived index

	// Memory.
	"store_memory":               additiveTool,
	"recall_memory":              readOnlyTool,
	"update_memory":              idempotentDestructiveTool, // overwrites content, no history
	"delete_memory":              idempotentDestructiveTool, // hard DELETE
	"list_memories":              readOnlyTool,
	"memory_stats":               readOnlyTool,
	"merge_duplicates":           destructiveTool, // archives the duplicates
	"mark_outdated":              idempotentTool,
	"promote_to_canonical":       idempotentTool,
	"conflicts_report":           readOnlyTool,
	"list_canonical_knowledge":   readOnlyTool,
	"recall_canonical_knowledge": readOnlyTool,
	"recall_multihop":            readOnlyTool,

	// Session close and the review queue. close_session and analyze_session
	// default to dry_run, but auto_apply_low_risk updates and merges existing
	// entries; accept_session_changes always does.
	"close_session":          destructiveTool,
	"analyze_session":        destructiveTool,
	"review_session_changes": readOnlyTool, // forced dry_run
	"accept_session_changes": destructiveTool,
	"resolve_review_item":    idempotentTool, // keeps an audit trail
	"resolve_review_queue":   idempotentTool,

	// Task lifecycle: outdates working memories in bulk.
	"end_task":      destructiveTool,
	"sweep_archive": destructiveTool,

	// Engineering records.
	"store_decision":            additiveTool,
	"store_incident":            additiveTool,
	"store_runbook":             additiveTool,
	"store_postmortem":          additiveTool,
	"store_dead_end":            additiveTool,
	"search_runbooks":           readOnlyTool,
	"recall_similar_incidents":  readOnlyTool,
	"summarize_project_context": readOnlyTool,
	"project_bank_view":         readOnlyTool,

	// Temporal knowledge and sediment layers.
	"recall_as_of":       readOnlyTool,
	"knowledge_timeline": readOnlyTool,
	"promote_sediment":   idempotentTool,
	"demote_sediment":    additiveTool, // one layer down per call
	"sediment_cycle":     additiveTool, // layer moves only
	"recount_references": idempotentTool,

	// Steward. steward_run defaults to dry_run but, applied, deletes expired
	// working memories and merges duplicates; steward_inbox_resolve carries out
	// merge and delete verbs.
	"steward_run":             destructiveTool,
	"steward_report":          readOnlyTool,
	"steward_policy":          idempotentTool, // action=set patches the policy
	"steward_status":          readOnlyTool,
	"drift_scan":              readOnlyTool,
	"verification_candidates": readOnlyTool,
	"verify_entry":            idempotentTool,
	"steward_inbox":           readOnlyTool,
	"steward_inbox_resolve":   destructiveTool,
}

// annotationsFor returns the hints of a flat tool, nil for a name toolSafety
// does not know (only possible if the registry test is skipped).
func annotationsFor(name string) *toolAnnotations {
	a, ok := toolSafety[name]
	if !ok {
		return nil
	}
	return &a
}

// combineAnnotations folds the hints of a grouped meta-tool's members: it is
// read-only or idempotent only if every action is, and destructive if any
// action is, because a client gates the meta-tool as a whole.
func combineAnnotations(members []tool) *toolAnnotations {
	out := toolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	for _, m := range members {
		if m.Annotations == nil {
			return nil
		}
		out.ReadOnlyHint = out.ReadOnlyHint && m.Annotations.ReadOnlyHint
		out.IdempotentHint = out.IdempotentHint && m.Annotations.IdempotentHint
		out.DestructiveHint = out.DestructiveHint || m.Annotations.DestructiveHint
		out.OpenWorldHint = out.OpenWorldHint || m.Annotations.OpenWorldHint
	}
	return &out
}
