package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/sessionclose"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

type sessionAnalysisOptions struct {
	forceDryRun           *bool
	forceSaveRaw          *bool
	forceAutoApplyLowRisk *bool
}

func (s *MCPServer) callCloseSession(args map[string]any) (any, *rpcError) {
	return s.runSessionAnalysis(args, sessionAnalysisOptions{})
}

func (s *MCPServer) callAnalyzeSession(args map[string]any) (any, *rpcError) {
	return s.runSessionAnalysis(args, sessionAnalysisOptions{})
}

func (s *MCPServer) callReviewSessionChanges(args map[string]any) (any, *rpcError) {
	dryRun := true
	saveRaw := false
	autoApplyLowRisk := false
	return s.runSessionAnalysis(args, sessionAnalysisOptions{
		forceDryRun:           &dryRun,
		forceSaveRaw:          &saveRaw,
		forceAutoApplyLowRisk: &autoApplyLowRisk,
	})
}

func (s *MCPServer) callAcceptSessionChanges(args map[string]any) (any, *rpcError) {
	dryRun := false
	saveRaw := true
	autoApplyLowRisk := true
	return s.runSessionAnalysis(args, sessionAnalysisOptions{
		forceDryRun:           &dryRun,
		forceSaveRaw:          &saveRaw,
		forceAutoApplyLowRisk: &autoApplyLowRisk,
	})
}

func (s *MCPServer) callResolveReviewItem(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	id, ok := getString(args, "id")
	if !ok || strings.TrimSpace(id) == "" {
		return nil, &rpcError{Code: -32602, Message: "id parameter is required"}
	}

	resolution, err := normalizeReviewResolution(mustString(args, "resolution"))
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	note := strings.TrimSpace(mustString(args, "note"))
	owner := strings.TrimSpace(mustString(args, "owner"))

	resolved, err := resolveReviewItemInStore(s.memoryStore, strings.TrimSpace(id), resolution, note, owner, time.Now().UTC())
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to resolve review item", Data: err.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	switch format {
	case "text":
		text := fmt.Sprintf("Review item resolved:\n- ID: %s\n- Resolution: %s", resolved["id"], resolved["resolution"])
		if owner != "" {
			text += fmt.Sprintf("\n- Owner: %s", owner)
		}
		if note != "" {
			text += fmt.Sprintf("\n- Note: %s", note)
		}
		return toolResultText(text), nil
	default:
		return toolResultJSON(resolved), nil
	}
}

func (s *MCPServer) callResolveReviewQueue(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	resolution, err := normalizeReviewResolution(mustString(args, "resolution"))
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	note := strings.TrimSpace(mustString(args, "note"))
	owner := strings.TrimSpace(mustString(args, "owner"))
	dryRun, _ := getBool(args, "dry_run")
	limit := boundedLimit(args, 20, 100)

	ids, err := resolveReviewQueueTargetIDs(s.memoryStore, getStringSlice(args, "ids"), memory.ProjectBankOptions{
		Filters: memory.Filters{
			Context: strings.TrimSpace(mustString(args, "context")),
		},
		Service: strings.TrimSpace(mustString(args, "service")),
		Tags:    userio.NormalizeTags(getStringSlice(args, "tags")),
		Limit:   limit,
	})
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to select review queue items", Data: err.Error()}
	}

	result := map[string]any{
		"resolution": resolution,
		"count":      len(ids),
		"ids":        ids,
		"dry_run":    dryRun,
	}
	if note != "" {
		result["note"] = note
	}
	if owner != "" {
		result["owner"] = owner
	}

	if !dryRun {
		resolvedItems := make([]map[string]any, 0, len(ids))
		now := time.Now().UTC()
		for _, id := range ids {
			resolved, err := resolveReviewItemInStore(s.memoryStore, id, resolution, note, owner, now)
			if err != nil {
				return nil, &rpcError{Code: -32000, Message: "failed to resolve review queue", Data: err.Error()}
			}
			resolvedItems = append(resolvedItems, resolved)
		}
		result["resolved_items"] = resolvedItems
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	switch format {
	case "text":
		if len(ids) == 0 {
			return toolResultText("Review queue resolution matched no pending items."), nil
		}
		if dryRun {
			return toolResultText(fmt.Sprintf("Review queue dry-run:\n- Count: %d\n- Resolution: %s\n- IDs: %s", len(ids), resolution, strings.Join(ids, ", "))), nil
		}
		return toolResultText(fmt.Sprintf("Review queue resolved:\n- Count: %d\n- Resolution: %s\n- IDs: %s", len(ids), resolution, strings.Join(ids, ", "))), nil
	default:
		return toolResultJSON(result), nil
	}
}

func (s *MCPServer) runSessionAnalysis(args map[string]any, options sessionAnalysisOptions) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	summaryText, ok := getString(args, "summary")
	if !ok || strings.TrimSpace(summaryText) == "" {
		return nil, &rpcError{Code: -32602, Message: "summary parameter is required"}
	}

	modeValue := mustString(args, "mode")
	mode := memory.SessionMode("")
	if strings.TrimSpace(modeValue) != "" {
		validatedMode, err := memory.ValidateSessionMode(modeValue, "")
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		mode = validatedMode
	}

	startedAt, err := parseOptionalRFC3339(args, "started_at")
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	endedAt, err := parseOptionalRFC3339(args, "ended_at")
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}

	saveRaw, saveRawProvided := getBool(args, "save_raw")
	autoApplyLowRisk, autoApplyProvided := getBool(args, "auto_apply_low_risk")
	dryRun := true
	if requestedDryRun, ok := getBool(args, "dry_run"); ok {
		dryRun = requestedDryRun
	} else if (saveRawProvided && saveRaw) || (autoApplyProvided && autoApplyLowRisk) {
		dryRun = false
	}
	if dryRun && saveRaw {
		return nil, &rpcError{Code: -32602, Message: "save_raw requires dry_run=false"}
	}
	if dryRun && autoApplyLowRisk {
		return nil, &rpcError{Code: -32602, Message: "auto_apply_low_risk requires dry_run=false"}
	}
	if options.forceDryRun != nil {
		dryRun = *options.forceDryRun
	}
	if options.forceSaveRaw != nil {
		saveRaw = *options.forceSaveRaw
	}
	if options.forceAutoApplyLowRisk != nil {
		autoApplyLowRisk = *options.forceAutoApplyLowRisk
	}

	request := sessionclose.AnalyzeRequest{
		Summary: memory.SessionSummary{
			Mode:      mode,
			Context:   mustString(args, "context"),
			Service:   mustString(args, "service"),
			Summary:   summaryText,
			StartedAt: startedAt,
			EndedAt:   endedAt,
			Tags:      userio.NormalizeTags(getStringSlice(args, "tags")),
			Metadata:  getStringMap(args, "metadata"),
		},
		DryRun:           dryRun,
		SaveRaw:          saveRaw,
		AutoApplyLowRisk: autoApplyLowRisk,
	}

	result, analyzeErr := sessionclose.New(s.memoryStore).Analyze(context.Background(), request)
	if analyzeErr != nil {
		return nil, &rpcError{Code: -32000, Message: "session analysis failed", Data: analyzeErr.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	switch format {
	case "text":
		return toolResultText(sessionclose.FormatAnalysis(result)), nil
	default:
		return toolResultJSON(result), nil
	}
}

func (s *MCPServer) callStoreDecision(args map[string]any) (any, *rpcError) {
	decision, ok := getString(args, "decision")
	if !ok || strings.TrimSpace(decision) == "" {
		return nil, &rpcError{Code: -32602, Message: "decision parameter is required"}
	}

	title, _ := getString(args, "title")
	rationale, _ := getString(args, "rationale")
	consequences, _ := getString(args, "consequences")
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	owner, _ := getString(args, "owner")
	status, _ := getString(args, "status")

	mem := &memory.Memory{
		Title:      defaultTitle(title, decision),
		Content:    joinContentLines(prefixedLine("Decision", decision), prefixedLine("Rationale", rationale), prefixedLine("Consequences", consequences), prefixedLine("Service", service), prefixedLine("Owner", owner), prefixedLine("Status", status)),
		Type:       memory.TypeSemantic,
		Context:    context,
		Importance: getImportance(args, 0.85),
		Tags:       buildEntityTags(memory.EngineeringTypeDecision, service, "", status, false, getStringSlice(args, "tags")),
		Metadata:   buildEntityMetadata(memory.EngineeringTypeDecision, service, "", status, false, map[string]string{"owner": owner}),
	}

	return s.storeWorkflowMemory("Decision", mem)
}

func (s *MCPServer) callStoreIncident(args map[string]any) (any, *rpcError) {
	summary, ok := getString(args, "summary")
	if !ok || strings.TrimSpace(summary) == "" {
		return nil, &rpcError{Code: -32602, Message: "summary parameter is required"}
	}

	title, _ := getString(args, "title")
	impact, _ := getString(args, "impact")
	rootCause, _ := getString(args, "root_cause")
	resolution, _ := getString(args, "resolution")
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	severity, _ := getString(args, "severity")

	mem := &memory.Memory{
		Title:      defaultTitle(title, summary),
		Content:    joinContentLines(prefixedLine("Incident", summary), prefixedLine("Impact", impact), prefixedLine("Root cause", rootCause), prefixedLine("Resolution", resolution), prefixedLine("Service", service), prefixedLine("Severity", severity)),
		Type:       memory.TypeEpisodic,
		Context:    context,
		Importance: getImportance(args, 0.90),
		Tags:       buildEntityTags(memory.EngineeringTypeIncident, service, severity, "", false, getStringSlice(args, "tags")),
		Metadata:   buildEntityMetadata(memory.EngineeringTypeIncident, service, severity, "", false, nil),
	}

	return s.storeWorkflowMemory("Incident", mem)
}

func (s *MCPServer) callStoreRunbook(args map[string]any) (any, *rpcError) {
	procedure, ok := getString(args, "procedure")
	if !ok || strings.TrimSpace(procedure) == "" {
		return nil, &rpcError{Code: -32602, Message: "procedure parameter is required"}
	}

	title, _ := getString(args, "title")
	trigger, _ := getString(args, "trigger")
	verification, _ := getString(args, "verification")
	rollback, _ := getString(args, "rollback")
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")

	mem := &memory.Memory{
		Title:      defaultTitle(title, procedure),
		Content:    joinContentLines(prefixedLine("Procedure", procedure), prefixedLine("Trigger", trigger), prefixedLine("Verification", verification), prefixedLine("Rollback", rollback), prefixedLine("Service", service)),
		Type:       memory.TypeProcedural,
		Context:    context,
		Importance: getImportance(args, 0.85),
		Tags:       buildEntityTags(memory.EngineeringTypeRunbook, service, "", "", false, getStringSlice(args, "tags")),
		Metadata:   buildEntityMetadata(memory.EngineeringTypeRunbook, service, "", "", false, nil),
	}

	return s.storeWorkflowMemory("Runbook", mem)
}

func (s *MCPServer) callStorePostmortem(args map[string]any) (any, *rpcError) {
	summary, ok := getString(args, "summary")
	if !ok || strings.TrimSpace(summary) == "" {
		return nil, &rpcError{Code: -32602, Message: "summary parameter is required"}
	}

	title, _ := getString(args, "title")
	impact, _ := getString(args, "impact")
	rootCause, _ := getString(args, "root_cause")
	actionItems, _ := getString(args, "action_items")
	followUp, _ := getString(args, "follow_up")
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	severity, _ := getString(args, "severity")

	mem := &memory.Memory{
		Title:      defaultTitle(title, summary),
		Content:    joinContentLines(prefixedLine("Postmortem", summary), prefixedLine("Impact", impact), prefixedLine("Root cause", rootCause), prefixedLine("Action items", actionItems), prefixedLine("Follow-up", followUp), prefixedLine("Service", service), prefixedLine("Severity", severity)),
		Type:       memory.TypeEpisodic,
		Context:    context,
		Importance: getImportance(args, 0.85),
		Tags:       buildEntityTags(memory.EngineeringTypePostmortem, service, severity, "", false, append([]string{"incident"}, getStringSlice(args, "tags")...)),
		Metadata:   buildEntityMetadata(memory.EngineeringTypePostmortem, service, severity, "", false, nil),
	}

	return s.storeWorkflowMemory("Postmortem", mem)
}

func (s *MCPServer) callSearchRunbooks(args map[string]any) (any, *rpcError) {
	query, ok := getString(args, "query")
	if !ok || strings.TrimSpace(query) == "" {
		return nil, &rpcError{Code: -32602, Message: "query parameter is required"}
	}

	ctx := context.Background()
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")
	limit := boundedLimit(args, 5, 20)
	debug, _ := getBool(args, "debug")
	var memoryResults []*memory.SearchResult
	if s.memoryStore != nil {
		results, err := s.memoryStore.Recall(ctx, query, memory.Filters{
			Type:    memory.TypeProcedural,
			Context: context,
			Tags:    []string{"runbook"},
		}, min(limit*3, 50))
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "runbook recall failed", Data: err.Error()}
		}
		memoryResults = filterMemorySearchResults(results, service, requiredTags, limit)
	}

	var docResults *rag.SearchResponse
	if s.ragEngine != nil {
		searchQuery := mergeQueryWithService(query, service)
		results, err := s.ragEngine.Search(ctx, searchQuery, limit, "runbook", debug)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "runbook document search failed", Data: err.Error()}
		}
		docResults = results
	}

	return toolResultText(s.formatWorkflowSearch("Runbook search", query, context, service, memoryResults, docResults, "Memory runbooks", "Indexed runbook docs")), nil
}

func (s *MCPServer) callRecallSimilarIncidents(args map[string]any) (any, *rpcError) {
	query, ok := getString(args, "query")
	if !ok || strings.TrimSpace(query) == "" {
		return nil, &rpcError{Code: -32602, Message: "query parameter is required"}
	}

	ctx := context.Background()
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")
	limit := boundedLimit(args, 5, 20)
	debug, _ := getBool(args, "debug")

	var memoryResults []*memory.SearchResult
	if s.memoryStore != nil {
		results, err := s.memoryStore.Recall(ctx, query, memory.Filters{
			Type:    memory.TypeEpisodic,
			Context: context,
			Tags:    []string{"incident", "postmortem"},
		}, min(limit*3, 50))
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "incident recall failed", Data: err.Error()}
		}
		memoryResults = filterMemorySearchResults(results, service, requiredTags, limit)
	}

	var docResults *rag.SearchResponse
	if s.ragEngine != nil {
		searchQuery := mergeQueryWithService(query, service)
		results, err := s.ragEngine.Search(ctx, searchQuery, limit, "postmortem", debug)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "postmortem document search failed", Data: err.Error()}
		}
		docResults = results
	}

	return toolResultText(s.formatWorkflowSearch("Similar incidents", query, context, service, memoryResults, docResults, "Incident memories", "Indexed postmortems")), nil
}

func (s *MCPServer) callSummarizeProjectContext(args map[string]any) (any, *rpcError) {
	if s.memoryStore == nil && s.ragEngine == nil {
		return nil, &rpcError{Code: -32000, Message: "no memory or RAG backend available"}
	}

	ctx := context.Background()
	context, _ := getString(args, "context")
	focus, _ := getString(args, "focus")
	service, _ := getString(args, "service")
	limit := boundedLimit(args, 5, 20)

	var canonicalEntries []*memory.CanonicalKnowledge
	var decisions []*memory.Memory
	var runbooks []*memory.Memory
	var incidents []*memory.Memory

	if s.memoryStore != nil {
		fetchLimit := min(limit*5, 100)
		filters := memory.Filters{Context: context}

		var allMemories []*memory.Memory
		if strings.TrimSpace(focus) != "" {
			allMemories = toMemories(s.recallMemories(ctx, focus, filters, fetchLimit))
		} else {
			allMemories = s.listMemories(ctx, filters, fetchLimit)
		}

		serviceTag := ""
		if service = strings.TrimSpace(service); service != "" {
			serviceTag = "service:" + service
		}

		for _, m := range allMemories {
			if serviceTag != "" && !hasAllTags(m.Tags, []string{serviceTag}) {
				continue
			}
			if memory.IsCanonicalMemory(m) && len(canonicalEntries) < limit {
				canonicalEntries = append(canonicalEntries, memory.ToCanonicalKnowledge(m, nil))
			}
			switch memory.EngineeringTypeOf(m) {
			case memory.EngineeringTypeDecision:
				if len(decisions) < limit {
					decisions = append(decisions, m)
				}
			case memory.EngineeringTypeRunbook:
				if len(runbooks) < limit {
					runbooks = append(runbooks, m)
				}
			case memory.EngineeringTypeIncident, memory.EngineeringTypePostmortem:
				if len(incidents) < limit {
					incidents = append(incidents, m)
				}
			}
		}
	}

	var relatedDocs *rag.SearchResponse
	if focus != "" && s.ragEngine != nil {
		searchQuery := mergeQueryWithService(focus, service)
		results, err := s.ragEngine.Search(ctx, searchQuery, limit, "", false)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "project context search failed", Data: err.Error()}
		}
		relatedDocs = results
	}

	return toolResultText(s.formatProjectContextSummary(context, focus, service, canonicalEntries, decisions, runbooks, incidents, relatedDocs)), nil
}

func (s *MCPServer) storeWorkflowMemory(entityLabel string, mem *memory.Memory) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}
	mem.Content = strings.TrimSpace(mem.Content)
	mem.Title = strings.TrimSpace(mem.Title)
	mem.Context = strings.TrimSpace(mem.Context)
	mem.Tags = userio.NormalizeTags(mem.Tags)
	if err := userio.ValidateMemoryContent(mem.Content); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	if err := s.memoryStore.Store(context.Background(), mem); err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to store memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("%s stored:\n- ID: %s\n- Type: %s\n- Title: %s\n- Tags: %v",
		entityLabel, mem.ID, formatMemoryType(mem.Type), mem.Title, mem.Tags)), nil
}

func parseOptionalRFC3339(args map[string]any, key string) (time.Time, error) {
	value := strings.TrimSpace(mustString(args, key))
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", key, err)
	}
	return parsed, nil
}

func getStringMap(args map[string]any, key string) map[string]string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case map[string]string:
		if len(typed) == 0 {
			return nil
		}
		result := make(map[string]string, len(typed))
		for k, v := range typed {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" || v == "" {
				continue
			}
			result[k] = v
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case map[string]any:
		if len(typed) == 0 {
			return nil
		}
		result := make(map[string]string, len(typed))
		for k, v := range typed {
			k = strings.TrimSpace(k)
			value := strings.TrimSpace(fmt.Sprintf("%v", v))
			if k == "" || value == "" {
				continue
			}
			result[k] = value
		}
		if len(result) == 0 {
			return nil
		}
		return result
	default:
		return nil
	}
}

func normalizeReviewResolution(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "resolved", nil
	}
	switch value {
	case "resolved", "dismissed", "deferred":
		return value, nil
	default:
		return "", fmt.Errorf("resolution must be resolved, dismissed, or deferred")
	}
}

func resolveReviewQueueTargetIDs(store *memory.Store, ids []string, options memory.ProjectBankOptions) ([]string, error) {
	normalizedIDs := normalizeIDs(ids)
	if len(normalizedIDs) > 0 {
		return normalizedIDs, nil
	}

	view, err := store.ProjectBankView(context.Background(), memory.ProjectBankViewReviewQueue, options)
	if err != nil {
		return nil, err
	}

	targets := make([]string, 0)
	for _, section := range view.Sections {
		for _, item := range section.Items {
			if item == nil || strings.TrimSpace(item.ID) == "" {
				continue
			}
			targets = append(targets, item.ID)
		}
	}
	return normalizeIDs(targets), nil
}

func normalizeIDs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func resolvedReviewTags(tags []string, resolution string) []string {
	filtered := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		switch {
		case tag == "":
			continue
		case tag == "review:required":
			continue
		case strings.HasPrefix(tag, "review:"):
			continue
		case tag == "status:review_required":
			continue
		case tag == "status:resolved" || tag == "status:dismissed" || tag == "status:deferred":
			continue
		default:
			filtered = append(filtered, tag)
		}
	}
	filtered = append(filtered, "review:"+resolution, "status:"+resolution)
	return memory.NormalizeTags(filtered)
}

func resolveReviewItemInStore(store *memory.Store, id string, resolution string, note string, owner string, resolvedAt time.Time) (map[string]any, error) {
	item, err := store.Get(strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if !memory.IsReviewQueueMemory(item) {
		return nil, fmt.Errorf("memory is not a review queue item")
	}

	metadata := map[string]string{
		memory.MetadataReviewRequired: "false",
		memory.MetadataStatus:         resolution,
		"review_resolved_at":          resolvedAt.UTC().Format(time.RFC3339),
	}
	if note != "" {
		metadata["review_resolution_note"] = note
	}
	if owner != "" {
		metadata["review_resolved_by"] = owner
	}

	if err := store.Update(context.Background(), item.ID, memory.Update{
		Tags:     resolvedReviewTags(item.Tags, resolution),
		Metadata: metadata,
	}); err != nil {
		return nil, err
	}

	result := map[string]any{
		"id":         item.ID,
		"resolution": resolution,
		"resolved":   true,
	}
	if note != "" {
		result["note"] = note
	}
	if owner != "" {
		result["owner"] = owner
	}
	return result, nil
}

func mustString(args map[string]any, key string) string {
	value, _ := getString(args, key)
	return strings.TrimSpace(value)
}

func parseFormat(args map[string]any) (string, *rpcError) {
	f := strings.ToLower(strings.TrimSpace(mustString(args, "format")))
	if f == "" {
		return "text", nil
	}
	if f != "text" && f != "json" {
		return "", &rpcError{Code: -32602, Message: "format must be text or json"}
	}
	return f, nil
}

func buildEntityTags(entity memory.EngineeringType, service string, severity string, status string, reviewRequired bool, extra []string) []string {
	return memory.BuildEngineeringTags(entity, service, severity, status, reviewRequired, extra)
}

func buildEntityMetadata(entity memory.EngineeringType, service string, severity string, status string, reviewRequired bool, extra map[string]string) map[string]string {
	return memory.BuildEngineeringMetadata(entity, service, severity, status, reviewRequired, extra)
}

func defaultTitle(title string, fallback string) string {
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}
	fallback = strings.TrimSpace(fallback)
	if len(fallback) > 80 {
		return fallback[:80] + "..."
	}
	return fallback
}

func prefixedLine(label string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return fmt.Sprintf("%s: %s", label, value)
}

func joinContentLines(lines ...string) string {
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}

func getImportance(args map[string]any, defaultValue float64) float64 {
	if importance, ok := args["importance"].(float64); ok && importance >= 0 && importance <= 1 {
		return importance
	}
	return defaultValue
}

func boundedLimit(args map[string]any, defaultValue int, maxValue int) int {
	limit := defaultValue
	if l, ok := getInt(args, "limit"); ok && l > 0 {
		limit = l
	}
	if limit > maxValue {
		limit = maxValue
	}
	return limit
}

func mergeQueryWithService(query string, service string) string {
	query = strings.TrimSpace(query)
	service = strings.TrimSpace(service)
	if service == "" {
		return query
	}
	return strings.TrimSpace(query + " " + service)
}

func hasAllTags(tags []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[tag] = struct{}{}
	}
	for _, requiredTag := range required {
		if _, ok := tagSet[requiredTag]; !ok {
			return false
		}
	}
	return true
}

type taggedItem interface {
	itemTags() []string
	itemService() string
}

type taggedSearchResult struct{ r *memory.SearchResult }

func (t taggedSearchResult) itemTags() []string  { return t.r.Memory.Tags }
func (t taggedSearchResult) itemService() string  { return "" }

type taggedCanonical struct{ e *memory.CanonicalKnowledge }

func (t taggedCanonical) itemTags() []string  { return t.e.Tags }
func (t taggedCanonical) itemService() string  { return t.e.Service }

func filterByTags[T any](items []T, wrap func(T) (taggedItem, bool), service string, tags []string, limit int) []T {
	requiredTags := append([]string(nil), tags...)
	if service != "" {
		requiredTags = append(requiredTags, "service:"+strings.TrimSpace(service))
	}
	filtered := make([]T, 0, min(len(items), max(limit, 16)))
	for _, item := range items {
		tagged, ok := wrap(item)
		if !ok {
			continue
		}
		svc := tagged.itemService()
		if svc != "" && service != "" && strings.TrimSpace(svc) != strings.TrimSpace(service) {
			continue
		}
		if !hasAllTags(tagged.itemTags(), requiredTags) {
			continue
		}
		filtered = append(filtered, item)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func filterMemorySearchResults(results []*memory.SearchResult, service string, tags []string, limit int) []*memory.SearchResult {
	return filterByTags(results, func(r *memory.SearchResult) (taggedItem, bool) {
		if r == nil || r.Memory == nil {
			return nil, false
		}
		return taggedSearchResult{r}, true
	}, service, tags, limit)
}

func filterCanonicalEntries(entries []*memory.CanonicalKnowledge, service string, tags []string, limit int) []*memory.CanonicalKnowledge {
	return filterByTags(entries, func(e *memory.CanonicalKnowledge) (taggedItem, bool) {
		if e == nil {
			return nil, false
		}
		return taggedCanonical{e}, true
	}, service, tags, limit)
}

func filterCanonicalSearchResults(results []*memory.CanonicalSearchResult, service string, tags []string, limit int) []*memory.CanonicalSearchResult {
	return filterByTags(results, func(r *memory.CanonicalSearchResult) (taggedItem, bool) {
		if r == nil || r.Entry == nil {
			return nil, false
		}
		return taggedCanonical{r.Entry}, true
	}, service, tags, limit)
}

func (s *MCPServer) listMemories(ctx context.Context, filters memory.Filters, limit int) []*memory.Memory {
	if s.memoryStore == nil {
		return nil
	}
	memories, err := s.memoryStore.List(ctx, filters, limit)
	if err != nil {
		return nil
	}
	return memories
}

func (s *MCPServer) recallMemories(ctx context.Context, query string, filters memory.Filters, limit int) []*memory.SearchResult {
	if s.memoryStore == nil {
		return nil
	}
	results, err := s.memoryStore.Recall(ctx, query, filters, limit)
	if err != nil {
		return nil
	}
	return results
}

func toMemories(results []*memory.SearchResult) []*memory.Memory {
	memories := make([]*memory.Memory, 0, len(results))
	for _, result := range results {
		if result == nil || result.Memory == nil {
			continue
		}
		memories = append(memories, result.Memory)
	}
	return memories
}

