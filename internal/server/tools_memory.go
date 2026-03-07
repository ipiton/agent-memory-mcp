package server

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/memory"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func (s *MCPServer) callStoreMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	content, ok := getString(args, "content")
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "content parameter is required"}
	}
	content = strings.TrimSpace(content)
	if err := userio.ValidateMemoryContent(content); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}

	mem := &memory.Memory{
		Content:    content,
		Type:       memory.TypeSemantic,
		Importance: 0.5,
	}

	if title, ok := getString(args, "title"); ok {
		mem.Title = strings.TrimSpace(title)
	}

	if memType, ok := getString(args, "type"); ok {
		parsedType, err := userio.ParseMemoryType(memType, memory.TypeSemantic, false)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		mem.Type = parsedType
	}

	if context, ok := getString(args, "context"); ok {
		mem.Context = strings.TrimSpace(context)
	}
	mem.Tags = userio.NormalizeTags(getStringSlice(args, "tags"))

	if importance, ok := args["importance"].(float64); ok {
		normalizedImportance, err := userio.NormalizeImportance(importance, memory.DefaultImportance)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		mem.Importance = normalizedImportance
	}

	err := s.memoryStore.Store(mem)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to store memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory stored:\n- ID: %s\n- Type: %s\n- Title: %s",
		mem.ID, mem.Type, mem.Title)), nil
}

func (s *MCPServer) callRecallMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	query, ok := getString(args, "query")
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "query parameter is required"}
	}
	query = strings.TrimSpace(query)
	if err := userio.ValidateQuery(query); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}

	limit := boundedLimit(args, 10, 50)

	filters := memory.Filters{}

	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	if context, ok := getString(args, "context"); ok {
		filters.Context = strings.TrimSpace(context)
	}
	filters.Tags = userio.NormalizeTags(getStringSlice(args, "tags"))

	results, err := s.memoryStore.Recall(query, filters, limit)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "memory recall failed", Data: err.Error()}
	}

	return toolResultText(s.formatMemoryResults(query, results)), nil
}

func (s *MCPServer) callUpdateMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	id, ok := getString(args, "id")
	if !ok || id == "" {
		return nil, &rpcError{Code: -32602, Message: "id parameter is required"}
	}

	updates := memory.Update{}

	if content, ok := getString(args, "content"); ok {
		content = strings.TrimSpace(content)
		if content != "" {
			if err := userio.ValidateMemoryContent(content); err != nil {
				return nil, &rpcError{Code: -32602, Message: err.Error()}
			}
		}
		updates.Content = content
	}
	if title, ok := getString(args, "title"); ok {
		updates.Title = strings.TrimSpace(title)
	}
	updates.Tags = userio.NormalizeTags(getStringSlice(args, "tags"))
	if importance, ok := args["importance"].(float64); ok {
		normalizedImportance, err := userio.NormalizeImportance(importance, 0)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		updates.Importance = &normalizedImportance
	}

	err := s.memoryStore.Update(id, updates)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to update memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory updated (ID: %s)", id)), nil
}

func (s *MCPServer) callDeleteMemory(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	id, ok := getString(args, "id")
	if !ok || id == "" {
		return nil, &rpcError{Code: -32602, Message: "id parameter is required"}
	}

	err := s.memoryStore.Delete(id)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to delete memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory deleted (ID: %s)", id)), nil
}

func (s *MCPServer) callListMemories(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	limit := boundedLimit(args, 20, 100)

	filters := memory.Filters{}

	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	if context, ok := getString(args, "context"); ok {
		filters.Context = strings.TrimSpace(context)
	}

	memories, err := s.memoryStore.List(filters, limit)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to list memories", Data: err.Error()}
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

	return toolResultText(buf.String()), nil
}

func (s *MCPServer) callMergeDuplicates(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	primaryID, ok := getString(args, "primary_id")
	if !ok || strings.TrimSpace(primaryID) == "" {
		return nil, &rpcError{Code: -32602, Message: "primary_id parameter is required"}
	}
	duplicateIDs := getStringSlice(args, "duplicate_ids")
	if len(duplicateIDs) == 0 {
		return nil, &rpcError{Code: -32602, Message: "duplicate_ids parameter is required"}
	}

	result, err := s.memoryStore.MergeDuplicates(primaryID, duplicateIDs)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to merge duplicates", Data: err.Error()}
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
		return nil, &rpcError{Code: -32602, Message: "id parameter is required"}
	}
	reason, _ := getString(args, "reason")
	supersededBy, _ := getString(args, "superseded_by")

	result, err := s.memoryStore.MarkOutdated(id, reason, supersededBy)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to mark memory outdated", Data: err.Error()}
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
		return nil, &rpcError{Code: -32602, Message: "id parameter is required"}
	}
	owner, _ := getString(args, "owner")

	result, err := s.memoryStore.PromoteToCanonical(id, owner)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to promote memory to canonical", Data: err.Error()}
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

	limit := boundedLimit(args, 10, 50)
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")

	filters := memory.Filters{Context: context}
	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	report, err := s.memoryStore.ConflictsReport(filters, limit*3)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to build conflicts report", Data: err.Error()}
	}

	filtered := make([]memory.ConflictReportItem, 0, len(report))
	for _, item := range report {
		if service != "" && strings.TrimSpace(item.Service) != strings.TrimSpace(service) {
			continue
		}
		if len(requiredTags) > 0 && !hasAllTags(item.Tags, requiredTags) {
			continue
		}
		filtered = append(filtered, item)
		if len(filtered) >= limit {
			break
		}
	}

	return toolResultText(formatConflictReport(filtered, context, service)), nil
}

func (s *MCPServer) callListCanonicalKnowledge(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	limit := boundedLimit(args, 10, 50)
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")

	filters := memory.Filters{Context: context}
	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	entries, err := s.memoryStore.ListCanonical(filters, limit*3)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to list canonical knowledge", Data: err.Error()}
	}

	filtered := filterCanonicalEntries(entries, service, requiredTags, limit)
	return toolResultText(formatCanonicalKnowledgeList(filtered, context, service)), nil
}

func (s *MCPServer) callRecallCanonicalKnowledge(args map[string]any) (any, *rpcError) {
	if err := s.requireMemoryStore(); err != nil {
		return nil, err
	}

	query, ok := getString(args, "query")
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "query parameter is required"}
	}
	query = strings.TrimSpace(query)
	if err := userio.ValidateQuery(query); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	limit := boundedLimit(args, 10, 50)
	context, _ := getString(args, "context")
	service, _ := getString(args, "service")
	requiredTags := getStringSlice(args, "tags")

	filters := memory.Filters{Context: context}
	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		parsedType, err := userio.ParseMemoryType(memType, "", true)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		filters.Type = parsedType
	}

	results, err := s.memoryStore.RecallCanonical(query, filters, limit*3)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "failed to recall canonical knowledge", Data: err.Error()}
	}

	filtered := filterCanonicalSearchResults(results, service, requiredTags, limit)
	return toolResultText(formatCanonicalKnowledgeRecall(query, filtered, context, service)), nil
}
