package server

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/stats"
)

var ragTools = map[string]bool{
	"semantic_search": true,
	"index_documents": true,
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
	"store_dead_end":             true,
	"project_bank_view":          true,
	"recall_as_of":               true,
	"knowledge_timeline":         true,
	"promote_sediment":           true,
	"demote_sediment":            true,
	"sediment_cycle":             true,
	"recount_references":         true,
	"recall_multihop":            true,
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
		"recall_multihop":            s.callRecallMultihop,
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
		"store_dead_end":             s.callStoreDeadEnd,
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
		"promote_sediment":           s.callPromoteSediment,
		"demote_sediment":            s.callDemoteSediment,
		"sediment_cycle":             s.callSedimentCycle,
		"recount_references":         s.callRecountReferences,
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

	// T67: a grouped meta-tool call (e.g. memory + action=store) resolves to its
	// legacy tool name; legacy names pass through unchanged. This is independent
	// of config.ToolGrouping so both call forms always work.
	name := req.Name
	if legacy, gErr, matched := resolveGroupedToolCall(req.Name, req.Arguments); matched {
		if gErr != nil {
			s.logToolEvent(req.Name, req.Arguments, start, gErr)
			return nil, gErr
		}
		name = legacy
	}

	handler, ok := s.toolHandlers[name]
	if !ok {
		rErr := &rpcError{Code: rpcErrMethodNotFound, Message: fmt.Sprintf("unknown tool: %s", name)}
		s.logToolEvent(name, req.Arguments, start, rErr)
		return nil, rErr
	}

	result, rErr := handler(req.Arguments)
	s.logToolEvent(name, req.Arguments, start, rErr)
	if s.sessionTracker != nil {
		s.sessionTracker.HandleToolCall(name, req.Arguments, rErr)
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
