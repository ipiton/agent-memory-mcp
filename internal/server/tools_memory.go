package server

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func (s *MCPServer) callStoreMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	type params struct {
		Content    string   `json:"content"`
		Title      string   `json:"title"`
		Type       string   `json:"type"`
		Context    string   `json:"context"`
		Importance *float64 `json:"importance"`
	}
	p, err := parseParams[params](args)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "failed to parse parameters", Data: err.Error()}
	}

	content := strings.TrimSpace(p.Content)
	if content == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "content parameter is required"}
	}
	if err := userio.ValidateMemoryContent(content); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	mem := &memory.Memory{
		Content:    content,
		Type:       memory.TypeSemantic,
		Importance: 0.5,
	}

	if p.Title != "" {
		mem.Title = strings.TrimSpace(p.Title)
	}

	if p.Type != "" {
		parsedType, err := userio.ParseMemoryType(p.Type, memory.TypeSemantic, false)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		mem.Type = parsedType
	}

	if p.Context != "" {
		mem.Context = strings.TrimSpace(p.Context)
	}
	mem.Tags = userio.NormalizeTags(getStringSlice(args, "tags"))

	if p.Importance != nil {
		normalizedImportance, err := userio.NormalizeImportance(*p.Importance, memory.DefaultImportance)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		mem.Importance = normalizedImportance
	}

	if err := s.memoryStore.Store(context.Background(), mem); err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to store memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory stored:\n- ID: %s\n- Type: %s\n- Title: %s",
		mem.ID, mem.Type, mem.Title)), nil
}

func (s *MCPServer) callRecallMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	type params struct {
		Query   string `json:"query"`
		Type    string `json:"type"`
		Context string `json:"context"`
		Limit   int    `json:"limit"`
	}
	p, err := parseParams[params](args)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "failed to parse parameters", Data: err.Error()}
	}

	query := strings.TrimSpace(p.Query)
	if query == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "query parameter is required"}
	}
	if err := userio.ValidateQuery(query); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	limit := p.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	filters := memory.Filters{}
	if p.Type != "" && p.Type != "all" {
		parsedType, err := userio.ParseMemoryType(p.Type, "", true)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	if p.Context != "" {
		filters.Context = strings.TrimSpace(p.Context)
	}
	filters.Tags = userio.NormalizeTags(getStringSlice(args, "tags"))

	results, err := s.memoryStore.Recall(context.Background(), query, filters, limit)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "memory recall failed", Data: err.Error()}
	}

	return toolResultText(s.formatMemoryResults(query, results)), nil
}

func (s *MCPServer) callUpdateMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	type params struct {
		ID         string   `json:"id"`
		Content    string   `json:"content"`
		Title      string   `json:"title"`
		Importance *float64 `json:"importance"`
	}
	p, err := parseParams[params](args)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "failed to parse parameters", Data: err.Error()}
	}

	if p.ID == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "id parameter is required"}
	}

	updates := memory.Update{}
	if p.Content != "" {
		content := strings.TrimSpace(p.Content)
		if err := userio.ValidateMemoryContent(content); err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		updates.Content = content
	}
	if p.Title != "" {
		updates.Title = strings.TrimSpace(p.Title)
	}
	if tags := getStringSlice(args, "tags"); tags != nil {
		updates.Tags = userio.NormalizeTags(tags)
	}
	if p.Importance != nil {
		normalizedImportance, err := userio.NormalizeImportance(*p.Importance, 0)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		updates.Importance = &normalizedImportance
	}

	err = s.memoryStore.Update(context.Background(), p.ID, updates)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to update memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory updated (ID: %s)", p.ID)), nil
}

func (s *MCPServer) callDeleteMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	type params struct {
		ID string `json:"id"`
	}
	p, err := parseParams[params](args)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "failed to parse parameters", Data: err.Error()}
	}

	if p.ID == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "id parameter is required"}
	}

	err = s.memoryStore.Delete(context.Background(), p.ID)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to delete memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory deleted (ID: %s)", p.ID)), nil
}

func (s *MCPServer) callListMemories(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	type params struct {
		Type    string `json:"type"`
		Context string `json:"context"`
		Limit   int    `json:"limit"`
	}
	p, err := parseParams[params](args)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "failed to parse parameters", Data: err.Error()}
	}

	limit := p.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	filters := memory.Filters{}
	if p.Type != "" && p.Type != "all" {
		parsedType, err := userio.ParseMemoryType(p.Type, "", true)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	if p.Context != "" {
		filters.Context = strings.TrimSpace(p.Context)
	}

	memories, err := s.memoryStore.List(context.Background(), filters, limit)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to list memories", Data: err.Error()}
	}

	return toolResultText(s.formatMemoryList(memories)), nil
}

func (s *MCPServer) callMemoryStats(_ map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	total := s.memoryStore.Count()
	byType := s.memoryStore.CountByType()
	byEmbeddingModel := s.memoryStore.CountByEmbeddingModel()
	loadErrors := s.memoryStore.LoadErrors()

	var buf bytes.Buffer
	buf.WriteString("**Agent Memory Statistics**\n\n")
	fmt.Fprintf(&buf, "Total memories: **%d**\n\n", total)
	if loadErrors > 0 {
		fmt.Fprintf(&buf, "Load errors: **%d** (check logs for details)\n\n", loadErrors)
	}
	buf.WriteString("By type:\n")

	typeNames := map[memory.Type]string{
		memory.TypeEpisodic:   "Episodic (events)",
		memory.TypeSemantic:   "Semantic (facts)",
		memory.TypeProcedural: "Procedural (patterns)",
		memory.TypeWorking:    "Working (current context)",
	}

	for memType, name := range typeNames {
		count := byType[memType]
		fmt.Fprintf(&buf, "- %s: %d\n", name, count)
	}
	if len(byEmbeddingModel) > 0 {
		buf.WriteString("\nBy embedding model:\n")
		for modelID, count := range byEmbeddingModel {
			fmt.Fprintf(&buf, "- %s: %d\n", modelID, count)
		}
	}

	if dedupSkips := s.memoryStore.DedupSkippedByReason(); len(dedupSkips) > 0 {
		buf.WriteString("\nHook dedup skips:\n")
		if v := dedupSkips["similar"]; v > 0 {
			fmt.Fprintf(&buf, "- similar: %d\n", v)
		}
		if v := dedupSkips["empty"]; v > 0 {
			fmt.Fprintf(&buf, "- empty: %d\n", v)
		}
	}

	return toolResultText(buf.String()), nil
}

func (s *MCPServer) callMergeDuplicates(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	primaryID, ok := getString(args, "primary_id")
	if !ok || strings.TrimSpace(primaryID) == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "primary_id parameter is required"}
	}
	duplicateIDs := getStringSlice(args, "duplicate_ids")
	if len(duplicateIDs) == 0 {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "duplicate_ids parameter is required"}
	}

	result, err := s.memoryStore.MergeDuplicates(context.Background(), primaryID, duplicateIDs)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to merge duplicates", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf(
		"Duplicates merged:\n- Primary: %s\n- Merged duplicates: %v\n- Archived duplicates: %v",
		result.PrimaryID,
		result.DuplicateIDs,
		result.ArchivedDuplicateIDs,
	)), nil
}

func (s *MCPServer) callMarkOutdated(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	id, ok := getString(args, "id")
	if !ok || strings.TrimSpace(id) == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "id parameter is required"}
	}
	reason, _ := getString(args, "reason")
	supersededBy, _ := getString(args, "superseded_by")

	result, err := s.memoryStore.MarkOutdated(context.Background(), id, reason, supersededBy)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to mark memory outdated", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf(
		"Memory marked outdated:\n- ID: %s\n- Status: %s\n- Superseded by: %s\n- Importance: %.2f",
		result.ID,
		result.Status,
		userio.ValueOrUnknown(result.SupersededBy),
		result.Importance,
	)), nil
}

func (s *MCPServer) callPromoteToCanonical(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	id, ok := getString(args, "id")
	if !ok || strings.TrimSpace(id) == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "id parameter is required"}
	}
	owner, _ := getString(args, "owner")

	result, err := s.memoryStore.PromoteToCanonical(context.Background(), id, owner)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to promote memory to canonical", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf(
		"Memory promoted to canonical:\n- ID: %s\n- Layer: %s\n- Owner: %s\n- Status: %s\n- Importance: %.2f",
		result.ID,
		result.Layer,
		userio.ValueOrUnknown(result.Owner),
		result.Status,
		result.Importance,
	)), nil
}

func (s *MCPServer) callConflictsReport(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	limit := boundedLimit(args, 10, 50)
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")

	filters := memory.Filters{Context: memContext}
	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	report, err := s.memoryStore.ConflictsReport(ctx, filters, limit*3)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to build conflicts report", Data: err.Error()}
	}

	filtered := make([]memory.ConflictReportItem, 0, len(report))
	for _, item := range report {
		if service != "" && strings.TrimSpace(item.Service) != strings.TrimSpace(service) {
			continue
		}
		if len(requiredTags) > 0 && !memory.HasAllTags(item.Tags, requiredTags) {
			continue
		}
		filtered = append(filtered, item)
		if len(filtered) >= limit {
			break
		}
	}

	return toolResultText(formatConflictReport(filtered, memContext, service)), nil
}

func (s *MCPServer) callListCanonicalKnowledge(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	limit := boundedLimit(args, 10, 50)
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")

	filters := memory.Filters{Context: memContext}
	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	entries, err := s.memoryStore.ListCanonical(ctx, filters, limit*3)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to list canonical knowledge", Data: err.Error()}
	}

	filtered := filterCanonicalEntries(entries, service, requiredTags, limit)
	return toolResultText(formatCanonicalKnowledgeList(filtered, memContext, service)), nil
}

func (s *MCPServer) callRecallCanonicalKnowledge(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	query, ok := getString(args, "query")
	if !ok {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "query parameter is required"}
	}
	query = strings.TrimSpace(query)
	if err := userio.ValidateQuery(query); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}
	limit := boundedLimit(args, 10, 50)
	memContext, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")

	filters := memory.Filters{Context: memContext}
	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	results, err := s.memoryStore.RecallCanonical(ctx, query, filters, limit*3)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "failed to recall canonical knowledge", Data: err.Error()}
	}

	filtered := filterCanonicalSearchResults(results, service, requiredTags, limit)
	return toolResultText(formatCanonicalKnowledgeRecall(query, filtered, memContext, service)), nil
}
