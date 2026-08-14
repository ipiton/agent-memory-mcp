package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/lifecycle"
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

func (s *MCPServer) runSessionAnalysis(args map[string]any, options sessionAnalysisOptions) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	summaryText, rsErr := requiredString(args, "summary")
	if rsErr != nil {
		return nil, rsErr
	}

	modeValue := mustString(args, "mode")
	mode := memory.SessionMode("")
	if strings.TrimSpace(modeValue) != "" {
		validatedMode, err := memory.ValidateSessionMode(modeValue, "")
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		mode = validatedMode
	}

	startedAt, err := parseOptionalRFC3339(args, "started_at")
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	endedAt, err := parseOptionalRFC3339(args, "ended_at")
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
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
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "save_raw requires dry_run=false"}
	}
	if dryRun && autoApplyLowRisk {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "auto_apply_low_risk requires dry_run=false"}
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
		return nil, &rpcError{Code: rpcErrServerError, Message: "session analysis failed", Data: analyzeErr.Error()}
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

// callRecountReferences exposes Store.RecountReferences as an MCP tool.
// One-time backfill to bootstrap the referenced_by_count counter from
// existing data (avoided_dead_end_id metadata + superseded_by column).
// Idempotent — re-running reports Updated=0 once counters match.
func (s *MCPServer) callRecountReferences(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	dryRun, _ := getBool(args, "dry_run")
	result, err := s.memoryStore.RecountReferences(context.Background(), dryRun)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "recount references failed", Data: err.Error()}
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	return renderFormatted(format, result, func() string {
		mode := "live"
		if result.DryRun {
			mode = "dry-run"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Recount references (%s):\n", mode)
		fmt.Fprintf(&b, "- Scanned: %d\n", result.Scanned)
		fmt.Fprintf(&b, "- Updated: %d\n", result.Updated)
		if len(result.Counts) > 0 && result.Updated <= 20 {
			b.WriteString("\nChanges:\n")
			for id, count := range result.Counts {
				fmt.Fprintf(&b, "- %s → %d\n", id, count)
			}
		}
		return b.String()
	}), nil
}

func (s *MCPServer) callSearchRunbooks(args map[string]any) (any, *rpcError) {
	query, rsErr := requiredString(args, "query")
	if rsErr != nil {
		return nil, rsErr
	}

	ctx := context.Background()
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")
	limit := boundedLimit(args, 5, 20)
	debug, _ := getBool(args, "debug")
	var memoryResults []*memory.SearchResult
	if s.memoryStore != nil {
		results, err := s.memoryStore.Recall(ctx, query, memory.Filters{
			Type:    memory.TypeProcedural,
			Context: memContext,
			Tags:    []string{"runbook"},
		}, min(limit*3, 50))
		if err != nil {
			return nil, &rpcError{Code: rpcErrServerError, Message: "runbook recall failed", Data: err.Error()}
		}
		memoryResults = filterMemorySearchResults(results, service, requiredTags, limit)
	}

	var docResults *rag.SearchResponse
	if re := s.getRagEngine(); re != nil {
		searchQuery := mergeQueryWithService(query, service)
		results, err := re.Search(ctx, searchQuery, limit, "runbook", debug)
		if err != nil {
			return nil, &rpcError{Code: rpcErrServerError, Message: "runbook document search failed", Data: err.Error()}
		}
		docResults = results
	}

	return toolResultText(s.formatWorkflowSearch("Runbook search", query, memContext, service, memoryResults, docResults, "Memory runbooks", "Indexed runbook docs")), nil
}

func (s *MCPServer) callRecallSimilarIncidents(args map[string]any) (any, *rpcError) {
	query, rsErr := requiredString(args, "query")
	if rsErr != nil {
		return nil, rsErr
	}

	ctx := context.Background()
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")
	limit := boundedLimit(args, 5, 20)
	debug, _ := getBool(args, "debug")

	var memoryResults []*memory.SearchResult
	if s.memoryStore != nil {
		results, err := s.memoryStore.Recall(ctx, query, memory.Filters{
			Type:    memory.TypeEpisodic,
			Context: memContext,
			Tags:    []string{"incident", "postmortem"},
		}, min(limit*3, 50))
		if err != nil {
			return nil, &rpcError{Code: rpcErrServerError, Message: "incident recall failed", Data: err.Error()}
		}
		memoryResults = filterMemorySearchResults(results, service, requiredTags, limit)
	}

	var docResults *rag.SearchResponse
	if re := s.getRagEngine(); re != nil {
		searchQuery := mergeQueryWithService(query, service)
		results, err := re.Search(ctx, searchQuery, limit, "postmortem", debug)
		if err != nil {
			return nil, &rpcError{Code: rpcErrServerError, Message: "postmortem document search failed", Data: err.Error()}
		}
		docResults = results
	}

	return toolResultText(s.formatWorkflowSearch("Similar incidents", query, memContext, service, memoryResults, docResults, "Incident memories", "Indexed postmortems")), nil
}

func (s *MCPServer) callSummarizeProjectContext(args map[string]any) (any, *rpcError) {
	if s.memoryStore == nil && s.getRagEngine() == nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "no memory or RAG backend available"}
	}

	ctx := context.Background()
	memContext, _ := getString(args, "context")
	focus, _ := getString(args, "focus")
	service, _ := getString(args, "service")
	limit := boundedLimit(args, 5, 20)

	var canonicalEntries []*memory.CanonicalKnowledge
	var decisions []*memory.Memory
	var runbooks []*memory.Memory
	var incidents []*memory.Memory

	if s.memoryStore != nil {
		fetchLimit := min(limit*5, 100)
		filters := memory.Filters{Context: memContext}

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
			if serviceTag != "" && !memory.HasAllTags(m.Tags, []string{serviceTag}) {
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
	if re := s.getRagEngine(); focus != "" && re != nil {
		searchQuery := mergeQueryWithService(focus, service)
		results, err := re.Search(ctx, searchQuery, limit, "", false)
		if err != nil {
			return nil, &rpcError{Code: rpcErrServerError, Message: "project context search failed", Data: err.Error()}
		}
		relatedDocs = results
	}

	return toolResultText(s.formatProjectContextSummary(memContext, focus, service, canonicalEntries, decisions, runbooks, incidents, relatedDocs)), nil
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
		return "", &rpcError{Code: rpcErrInvalidParams, Message: "format must be text or json"}
	}
	return f, nil
}

// renderFormatted dispatches on a format string ("text" or "json") returning
// the appropriate toolResult. textFn is invoked lazily so callers don't pay
// the formatting cost when the client requested JSON.
func renderFormatted(format string, jsonValue any, textFn func() string) any {
	if format == "json" {
		return toolResultJSON(jsonValue)
	}
	return toolResultText(textFn())
}

// requiredString extracts a required string argument and trims whitespace.
// Returns a JSON-RPC InvalidParams error if missing or blank, with a stable
// message format used across all tools.
func requiredString(args map[string]any, key string) (string, *rpcError) {
	value, _ := getString(args, key)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", &rpcError{Code: rpcErrInvalidParams, Message: fmt.Sprintf("%s parameter is required", key)}
	}
	return value, nil
}

func defaultTitle(title string, fallback string) string {
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}
	// Rune-aware (T87): byte-slicing a Cyrillic/CJK fallback at 80 bytes can
	// split a rune and yield an invalid-UTF-8 title the write boundary rejects.
	return memory.TruncateRunes(strings.TrimSpace(fallback), 80)
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

// getImportance returns the caller-supplied importance, or an rpcError when the
// key is present but unusable.
//
// T89 M9: it used to fold "absent" and "present but out of range" into a single
// ok=false, so `store_memory` rejected importance=5 with an invalid-params
// error while `store_decision` and its siblings accepted the same argument and
// silently substituted their own default. One contract, two answers, and the
// engineering path's answer was the one that discarded caller intent without
// saying so. Absent still means "use the default"; invalid is now an error on
// both paths.
func getImportance(args map[string]any) (float64, bool, *rpcError) {
	raw, present := args["importance"]
	if !present || raw == nil {
		return 0, false, nil
	}
	importance, ok := raw.(float64)
	if !ok {
		return 0, false, &rpcError{Code: rpcErrInvalidParams, Message: "importance must be a number between 0.0 and 1.0"}
	}
	normalized, err := userio.NormalizeImportance(importance, 0)
	if err != nil {
		return 0, false, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	return normalized, true, nil
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

type taggedItem interface {
	itemTags() []string
	itemService() string
}

type taggedSearchResult struct{ r *memory.SearchResult }

func (t taggedSearchResult) itemTags() []string  { return t.r.Memory.Tags }
func (t taggedSearchResult) itemService() string { return "" }

type taggedCanonical struct{ e *memory.CanonicalKnowledge }

func (t taggedCanonical) itemTags() []string  { return t.e.Tags }
func (t taggedCanonical) itemService() string { return t.e.Service }

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
		if !memory.HasAllTags(tagged.itemTags(), requiredTags) {
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

// callEndTask is the MCP tool entry point for explicit single-slug consolidation.
func (srv *MCPServer) callEndTask(args map[string]any) (any, *rpcError) {
	if err := srv.requireMemoryStore(); err != nil {
		return nil, err
	}
	slug, rsErr := requiredString(args, "context_slug")
	if rsErr != nil {
		return nil, rsErr
	}
	dryRun, _ := getBool(args, "dry_run")

	sweepCfg, rErr := srv.buildSweepConfigFromArgs(args, dryRun)
	if rErr != nil {
		return nil, rErr
	}

	sweeper := lifecycle.NewSweeper(srv.memoryStore)
	result, err := sweeper.EndTask(context.Background(), slug, sweepCfg)
	if err != nil {
		return nil, mapSweepError("end_task", err)
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	return renderFormatted(format, result, func() string { return lifecycle.FormatSweepResult(result) }), nil
}

// callSweepArchive is the MCP tool entry point for pull-mode archive sweeps.
func (srv *MCPServer) callSweepArchive(args map[string]any) (any, *rpcError) {
	if err := srv.requireMemoryStore(); err != nil {
		return nil, err
	}
	dryRun, _ := getBool(args, "dry_run")

	sweepCfg, rErr := srv.buildSweepConfigFromArgs(args, dryRun)
	if rErr != nil {
		return nil, rErr
	}

	sweeper := lifecycle.NewSweeper(srv.memoryStore)
	result, err := sweeper.SweepArchive(context.Background(), sweepCfg)
	if err != nil {
		return nil, mapSweepError("sweep_archive", err)
	}

	format, fmtErr := parseFormat(args)
	if fmtErr != nil {
		return nil, fmtErr
	}
	return renderFormatted(format, result, func() string { return lifecycle.FormatSweepResult(result) }), nil
}

// mapSweepError translates lifecycle/sweep errors into typed JSON-RPC errors so
// MCP clients see actionable messages instead of generic -32000 "X failed".
// Falls back to the legacy server-error envelope for unknown root causes.
func mapSweepError(op string, err error) *rpcError {
	if err == nil {
		return nil
	}
	if errors.Is(err, lifecycle.ErrNoRoots) {
		return &rpcError{
			Code:    rpcErrInvalidParams,
			Message: op + ": archive roots not configured",
			Data:    "Set MCP_TASK_ARCHIVE_ROOTS in service config or pass roots[] explicitly",
		}
	}
	return &rpcError{Code: rpcErrServerError, Message: op + " failed", Data: err.Error()}
}

// buildSweepConfigFromArgs resolves the ArchiveSweepConfig from MCP args,
// falling back to the server's loaded config for roots and slug pattern.
//
// T63: auto_promote defaults to true (zero-ops consolidation — the T77
// provenance gate keeps it safe), and roots fall back to the
// <root>/tasks/archive convention when unset. Callers may still override both.
func (srv *MCPServer) buildSweepConfigFromArgs(args map[string]any, dryRun bool) (lifecycle.ArchiveSweepConfig, *rpcError) {
	autoPromote := true
	if v, ok := getBool(args, "auto_promote"); ok {
		autoPromote = v
	}
	snapshot := srv.configSnapshot()
	sweepCfg := lifecycle.ArchiveSweepConfig{
		Roots:              resolveArchiveSweepRoots(snapshot),
		SlugPattern:        snapshot.Lifecycle.TaskSlugPattern,
		DryRun:             dryRun,
		PromotionThreshold: lifecycle.DefaultPromotionThreshold,
		KeepTag:            lifecycle.KeepAfterArchiveTag,
		AutoPromote:        autoPromote,
	}
	if argRoots := getStringSlice(args, "roots"); len(argRoots) > 0 {
		sweepCfg.Roots = argRoots
	}
	if pat := strings.TrimSpace(mustString(args, "slug_pattern")); pat != "" {
		re, err := regexp.Compile(pat)
		if err != nil {
			return sweepCfg, &rpcError{Code: rpcErrInvalidParams, Message: fmt.Sprintf("invalid slug_pattern: %v", err)}
		}
		sweepCfg.SlugPattern = re
	}
	if v, ok := args["promotion_threshold"].(float64); ok && v > 0 {
		sweepCfg.PromotionThreshold = v
	}
	if kt := strings.TrimSpace(mustString(args, "keep_tag")); kt != "" {
		sweepCfg.KeepTag = kt
	}
	return sweepCfg, nil
}
