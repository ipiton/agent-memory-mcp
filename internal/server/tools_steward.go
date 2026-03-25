package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/steward"
)

func (s *MCPServer) requireStewardService() *rpcError {
	if s.stewardService == nil {
		return &rpcError{Code: rpcErrServerError, Message: "steward service is not enabled; set MCP_STEWARD_ENABLED=true"}
	}
	return nil
}

// --- Phase 1 tools ---

func (s *MCPServer) callStewardRun(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	scopeRaw, _ := getString(args, "scope")
	scope, err := steward.ValidateRunScope(scopeRaw)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	dryRun := true
	if v, ok := getBool(args, "dry_run"); ok {
		dryRun = v
	}
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	format, _ := getString(args, "format")

	report, err := s.stewardService.Run(context.Background(), steward.RunParams{
		Scope:   scope,
		DryRun:  dryRun,
		Context: memContext,
		Service: service,
	})
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("steward run failed: %v", err)}
	}

	if format == "json" {
		return toolResultJSON(report), nil
	}
	return toolResultText(steward.FormatReport(report)), nil
}

func (s *MCPServer) callStewardReport(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	runID, _ := getString(args, "run_id")
	format, _ := getString(args, "format")

	report, err := s.stewardService.GetReport(runID)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("failed to load report: %v", err)}
	}
	if report == nil {
		return toolResultText("No steward reports found."), nil
	}

	if format == "json" {
		return toolResultJSON(report), nil
	}
	return toolResultText(steward.FormatReport(report)), nil
}

func (s *MCPServer) callStewardPolicy(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	action, _ := getString(args, "action")
	if action == "" {
		action = "get"
	}

	switch action {
	case "get":
		p := s.stewardService.Policy()
		return toolResultJSON(p), nil

	case "set":
		policyRaw, ok := args["policy"]
		if !ok {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: "policy parameter is required for action=set"}
		}
		data, err := json.Marshal(policyRaw)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: fmt.Sprintf("invalid policy: %v", err)}
		}
		var p steward.Policy
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: fmt.Sprintf("invalid policy structure: %v", err)}
		}
		if err := s.stewardService.SetPolicy(p); err != nil {
			return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("failed to save policy: %v", err)}
		}
		return toolResultText("Policy updated."), nil

	default:
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: fmt.Sprintf("unknown action: %s (expected get or set)", action)}
	}
}

func (s *MCPServer) callStewardStatus(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	status, err := s.stewardService.Status()
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("failed to get status: %v", err)}
	}
	if s.stewardScheduler != nil {
		status.NextRun = s.stewardScheduler.NextRun()
	}

	format, _ := getString(args, "format")
	if format == "json" {
		return toolResultJSON(status), nil
	}
	return toolResultText(formatStewardStatus(status)), nil
}

// --- Phase 2 tools ---

func (s *MCPServer) callDriftScan(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	scopeRaw, _ := getString(args, "scope")
	driftScope, err := steward.ValidateDriftScope(scopeRaw)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	format, _ := getString(args, "format")

	result, err := s.stewardService.DriftScan(context.Background(), steward.DriftScanParams{
		Scope:    driftScope,
		Context:  memContext,
		Service:  service,
		RootPath: s.config.RootPath,
	})
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("drift scan failed: %v", err)}
	}

	if format == "json" {
		return toolResultJSON(result), nil
	}
	return toolResultText(formatDriftResult(result)), nil
}

func (s *MCPServer) callVerificationCandidates(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	limit, _ := getInt(args, "limit")
	if limit <= 0 {
		limit = 20
	}
	scope, _ := getString(args, "scope")
	minAge, _ := getInt(args, "min_age_days")
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	format, _ := getString(args, "format")

	candidates, err := s.stewardService.VerificationCandidates(context.Background(), steward.VerificationCandidatesParams{
		Limit:      limit,
		Scope:      scope,
		MinAgeDays: minAge,
		Context:    memContext,
		Service:    service,
	})
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("verification candidates failed: %v", err)}
	}

	if format == "json" {
		return toolResultJSON(candidates), nil
	}
	return toolResultText(formatVerificationCandidates(candidates)), nil
}

func (s *MCPServer) callVerifyEntry(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	memoryID, ok := getString(args, "memory_id")
	if !ok || memoryID == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "memory_id is required"}
	}
	methodRaw, _ := getString(args, "method")
	method, err := steward.ValidateVerificationMethod(methodRaw)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	statusRaw, _ := getString(args, "status")
	vStatus, err := steward.ValidateVerificationStatus(statusRaw)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	note, _ := getString(args, "note")

	err = s.stewardService.VerifyEntry(context.Background(), steward.VerifyParams{
		MemoryID: memoryID,
		Method:   method,
		Status:   vStatus,
		Note:     note,
	})
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("verify entry failed: %v", err)}
	}
	return toolResultText(fmt.Sprintf("Memory %s marked as %s (method: %s)", memoryID, vStatus, method)), nil
}

func (s *MCPServer) callStewardInbox(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	status, _ := getString(args, "status")
	if status == "" {
		status = "pending"
	}
	kind, _ := getString(args, "kind")
	limit, _ := getInt(args, "limit")
	if limit <= 0 {
		limit = 20
	}
	sortBy, _ := getString(args, "sort_by")
	format, _ := getString(args, "format")

	items, err := s.stewardService.ListInbox(steward.InboxQuery{
		Status: status,
		Kind:   kind,
		Limit:  limit,
		SortBy: sortBy,
	})
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("inbox query failed: %v", err)}
	}

	if format == "json" {
		return toolResultJSON(items), nil
	}
	return toolResultText(formatInboxItems(items)), nil
}

func (s *MCPServer) callStewardInboxResolve(args map[string]any) (any, *rpcError) {
	if err := s.requireStewardService(); err != nil {
		return nil, err
	}

	itemID, ok := getString(args, "item_id")
	if !ok || itemID == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "item_id is required"}
	}
	action, _ := getString(args, "action")
	if action == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "action is required"}
	}
	note, _ := getString(args, "note")

	err := s.stewardService.ResolveInbox(itemID, action, note, "user")
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("resolve failed: %v", err)}
	}
	return toolResultText(fmt.Sprintf("Inbox item %s resolved with action: %s", itemID, action)), nil
}

// --- Formatters ---

func formatStewardStatus(s *steward.Status) string {
	out := fmt.Sprintf("Steward Mode: %s\nPending Review: %d\n", s.PolicyMode, s.PendingReview)
	if s.LastRun != nil {
		out += fmt.Sprintf("\nLast Run: %s\n  Started: %s\n  Duration: %s\n  Scanned: %d | Applied: %d | Pending: %d\n",
			s.LastRun.RunID[:8], s.LastRun.StartedAt.Format("2006-01-02 15:04:05"),
			s.LastRun.Duration,
			s.LastRun.Stats.Scanned, s.LastRun.Stats.ActionsApplied, s.LastRun.Stats.ActionsPendingReview)
	}
	if s.NextRun != nil {
		out += fmt.Sprintf("\nNext Scheduled Run: %s\n", s.NextRun.Format("2006-01-02 15:04:05"))
	}
	return out
}

func formatDriftResult(r *steward.DriftResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Drift Scan: %d memories scanned, %d findings\n", r.Scanned, len(r.Findings))

	for i, f := range r.Findings {
		fmt.Fprintf(&sb, "\n%d. [%s] %s\n", i+1, f.DriftType, f.Title)
		fmt.Fprintf(&sb, "   Evidence: %s\n", f.Evidence)
		fmt.Fprintf(&sb, "   Suggested: %s (confidence: %.2f)\n", f.SuggestedAction, f.Confidence)
	}

	if len(r.UnreachableSources) > 0 {
		sb.WriteString("\nUnreachable sources:\n")
		for _, u := range r.UnreachableSources {
			fmt.Fprintf(&sb, "  - %s: %s (%s)\n", u.MemoryID[:8], u.SourcePath, u.Reason)
		}
	}
	return sb.String()
}

func formatVerificationCandidates(candidates []steward.VerificationCandidate) string {
	if len(candidates) == 0 {
		return "No verification candidates found."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Verification Candidates: %d\n", len(candidates))

	for i, c := range candidates {
		fmt.Fprintf(&sb, "\n%d. [%s] %s\n", i+1, c.Urgency, c.Title)
		fmt.Fprintf(&sb, "   Type: %s | Entity: %s | Age: %d days\n", c.Type, c.Entity, c.AgeDays)
		fmt.Fprintf(&sb, "   Reason: %s | Action: %s\n", c.Reason, c.SuggestedAction)
		fmt.Fprintf(&sb, "   ID: %s\n", c.MemoryID)
	}
	return sb.String()
}

// --- Phase 3 tools (temporal) ---

func (s *MCPServer) callRecallAsOf(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	query, ok := getString(args, "query")
	if !ok || query == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "query parameter is required"}
	}
	asOfStr, ok := getString(args, "as_of")
	if !ok || asOfStr == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "as_of parameter is required (RFC3339 timestamp)"}
	}
	asOf, err := time.Parse(time.RFC3339, asOfStr)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: fmt.Sprintf("invalid as_of timestamp: %v", err)}
	}

	memContext, _ := getString(args, "context")
	limit, _ := getInt(args, "limit")
	if limit <= 0 {
		limit = 10
	}
	format, _ := getString(args, "format")

	filters := memory.Filters{Context: memContext}
	results, err := s.memoryStore.RecallAsOf(context.Background(), query, asOf, filters, limit)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("recall_as_of failed: %v", err)}
	}

	if format == "json" {
		return toolResultJSON(results), nil
	}
	return toolResultText(formatRecallAsOf(results, asOf)), nil
}

func (s *MCPServer) callKnowledgeTimeline(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	query, ok := getString(args, "query")
	if !ok || query == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "query parameter is required"}
	}
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	format, _ := getString(args, "format")

	entries, err := s.memoryStore.KnowledgeTimeline(context.Background(), query, memContext, service)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: fmt.Sprintf("knowledge_timeline failed: %v", err)}
	}

	if format == "json" {
		return toolResultJSON(entries), nil
	}
	return toolResultText(formatKnowledgeTimeline(entries, query)), nil
}

func formatRecallAsOf(results []*memory.SearchResult, asOf time.Time) string {
	if len(results) == 0 {
		return fmt.Sprintf("No knowledge found valid at %s.", asOf.Format("2006-01-02 15:04"))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Knowledge valid at %s: %d results\n", asOf.Format("2006-01-02 15:04"), len(results))
	for i, r := range results {
		title := r.Memory.Title
		if title == "" {
			runes := []rune(r.Memory.Content)
			if len(runes) > 60 {
				title = string(runes[:60]) + "..."
			} else {
				title = r.Memory.Content
			}
		}
		fmt.Fprintf(&sb, "\n%d. %s (score: %.2f)\n", i+1, title, r.Score)
		fmt.Fprintf(&sb, "   ID: %s | Type: %s\n", r.Memory.ID, r.Memory.Type)
		if r.Memory.ValidFrom != nil {
			fmt.Fprintf(&sb, "   Valid from: %s", r.Memory.ValidFrom.Format("2006-01-02"))
		}
		if r.Memory.ValidUntil != nil {
			fmt.Fprintf(&sb, " until: %s", r.Memory.ValidUntil.Format("2006-01-02"))
		}
		if r.Memory.ValidFrom != nil || r.Memory.ValidUntil != nil {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func formatKnowledgeTimeline(entries []memory.TimelineEntry, query string) string {
	if len(entries) == 0 {
		return fmt.Sprintf("No timeline entries found for %q.", query)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Knowledge Timeline for %q: %d entries\n", query, len(entries))
	for i, e := range entries {
		status := e.Status
		if e.SupersededBy != "" {
			status += " -> " + e.SupersededBy[:8]
		}
		fromStr := e.CreatedAt.Format("2006-01-02")
		if e.ValidFrom != nil {
			fromStr = e.ValidFrom.Format("2006-01-02")
		}
		untilStr := "now"
		if e.ValidUntil != nil {
			untilStr = e.ValidUntil.Format("2006-01-02")
		}
		fmt.Fprintf(&sb, "\n%d. [%s..%s] %s — %s\n", i+1, fromStr, untilStr, e.Title, status)
		if e.Replaces != "" {
			fmt.Fprintf(&sb, "   Replaces: %s\n", e.Replaces[:min(8, len(e.Replaces))])
		}
		fmt.Fprintf(&sb, "   ID: %s\n", e.MemoryID)
	}
	return sb.String()
}

func formatInboxItems(items []steward.InboxItem) string {
	if len(items) == 0 {
		return "Steward inbox is empty."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Steward Inbox: %d items\n", len(items))

	for i, item := range items {
		stateIcon := "[ ]"
		switch item.State {
		case steward.InboxResolved:
			stateIcon = "[x]"
		case steward.InboxDeferred:
			stateIcon = "[~]"
		}
		fmt.Fprintf(&sb, "\n%d. %s [%s] %s — %s\n", i+1, stateIcon, item.Urgency, item.Kind, item.Title)
		fmt.Fprintf(&sb, "   Confidence: %.2f | Recommended: %s\n", item.Confidence, item.RecommendedAction)
		if len(item.TargetIDs) > 0 {
			fmt.Fprintf(&sb, "   Targets: %s\n", strings.Join(item.TargetIDs, ", "))
		}
		fmt.Fprintf(&sb, "   ID: %s\n", item.ID)
	}
	return sb.String()
}
