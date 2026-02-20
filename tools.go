package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.uber.org/zap"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *MCPServer) handleToolsList(_ json.RawMessage) (any, *RPCError) {
	tools := []Tool{
		{
			Name:        "repo_list",
			Description: "List files and folders under an allowlisted path",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to list (empty for root)",
					},
					"max_depth": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"description": "Maximum directory depth to traverse (0 = unlimited)",
					},
				},
			},
		},
		{
			Name:        "repo_read",
			Description: "Read a file from the allowlisted paths",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to the file to read",
					},
					"offset": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"description": "Byte offset to start reading from (default: 0)",
					},
					"max_bytes": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"description": "Maximum number of bytes to read (default: 2MB)",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "repo_search",
			Description: "Search for a query string in allowlisted paths",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query string",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Relative path to search within (empty for all allowed paths)",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"description": "Maximum number of search results to return (default: 200)",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "semantic_search",
			Description: "Semantic search across indexed documents and archives",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language search query",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results (default: 10)",
						"default":     10,
						"minimum":     1,
						"maximum":     50,
					},
					"doc_type": map[string]any{
						"type":        "string",
						"enum":        []string{"docs", "tasks", "memory", "all"},
						"description": "Document type to search",
						"default":     "all",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Document category (api, architecture, deployment, etc.)",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "find_similar_tasks",
			Description: "Find similar tasks from archive",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description": map[string]any{
						"type":        "string",
						"description": "Description of the current task",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     5,
						"minimum":     1,
						"maximum":     20,
					},
				},
				"required": []string{"description"},
			},
		},
		{
			Name:        "get_relevant_docs",
			Description: "Get relevant documentation by topic",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{
						"type":        "string",
						"description": "Topic or keyword to search documentation for",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     10,
						"minimum":     1,
						"maximum":     30,
					},
				},
				"required": []string{"topic"},
			},
		},
		{
			Name:        "index_documents",
			Description: "Re-index documents for RAG search",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{},
			},
		},
		// Memory tools
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
			Description: "Recall information from memory by semantic query",
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
	}
	return map[string]any{"tools": tools}, nil
}

func (s *MCPServer) handleToolsCall(params json.RawMessage) (any, *RPCError) {
	start := time.Now()
	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		rpcErr := &RPCError{Code: -32602, Message: "invalid params", Data: err.Error()}
		s.logToolEvent("", nil, start, rpcErr)
		return nil, rpcErr
	}
	if req.Name == "" {
		rpcErr := &RPCError{Code: -32602, Message: "tool name is required"}
		s.logToolEvent("", req.Arguments, start, rpcErr)
		return nil, rpcErr
	}

	switch req.Name {
	case "repo_list":
		result, rpcErr := s.callRepoList(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "repo_read":
		result, rpcErr := s.callRepoRead(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "repo_search":
		result, rpcErr := s.callRepoSearch(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "semantic_search":
		result, rpcErr := s.callSemanticSearch(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "find_similar_tasks":
		result, rpcErr := s.callFindSimilarTasks(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "get_relevant_docs":
		result, rpcErr := s.callGetRelevantDocs(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "index_documents":
		result, rpcErr := s.callIndexDocuments(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	// Memory tools
	case "store_memory":
		result, rpcErr := s.callStoreMemory(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "recall_memory":
		result, rpcErr := s.callRecallMemory(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "update_memory":
		result, rpcErr := s.callUpdateMemory(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "delete_memory":
		result, rpcErr := s.callDeleteMemory(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "list_memories":
		result, rpcErr := s.callListMemories(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	case "memory_stats":
		result, rpcErr := s.callMemoryStats(req.Arguments)
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return result, rpcErr
	default:
		rpcErr := &RPCError{Code: -32601, Message: fmt.Sprintf("unknown tool: %s", req.Name)}
		s.logToolEvent(req.Name, req.Arguments, start, rpcErr)
		return nil, rpcErr
	}
}

func (s *MCPServer) logToolEvent(name string, args map[string]any, start time.Time, rpcErr *RPCError) {
	if s.stats == nil {
		return
	}
	event := StatsEvent{
		Event:      "tool_call",
		Method:     "tools/call",
		Tool:       name,
		DurationMs: time.Since(start).Milliseconds(),
		Success:    rpcErr == nil,
	}
	if rpcErr != nil {
		event.Error = rpcErr.Message
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

func getString(args map[string]any, key string) (string, bool) {
	val, ok := args[key]
	if !ok {
		return "", false
	}
	switch typed := val.(type) {
	case string:
		return typed, true
	default:
		return fmt.Sprintf("%v", typed), true
	}
}

func getInt(args map[string]any, key string) (int, bool) {
	val, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := val.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func getInt64(args map[string]any, key string) (int64, bool) {
	val, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := val.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// RAG tool implementations

func (s *MCPServer) callSemanticSearch(args map[string]any) (any, *RPCError) {
	if s.ragEngine == nil {
		// Always log this - it's critical for debugging
		if s.fileLogger != nil {
			s.fileLogger.Warn("semantic_search called but RAG engine is not available",
				zap.Bool("rag_enabled_in_config", s.config.RAGEnabled),
				zap.String("rag_index_path", s.config.RAGIndexPath),
			)
		} else {
			// Fallback to stderr if file logger is not available
			fmt.Fprintf(os.Stderr, "WARN: semantic_search called but RAG engine is nil (RAG enabled: %v)\n", s.config.RAGEnabled)
		}
		return nil, &RPCError{Code: -32000, Message: "RAG engine not available"}
	}

	query, ok := getString(args, "query")
	if !ok || query == "" {
		return nil, &RPCError{Code: -32602, Message: "query parameter is required"}
	}

	limit := 10
	if l, ok := getInt(args, "limit"); ok && l > 0 {
		limit = l
	}

	docType, _ := getString(args, "doc_type")
	category, _ := getString(args, "category")

	results, err := s.ragEngine.Search(query, limit, docType, category)
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: fmt.Sprintf("search failed: %v", err)}
	}

	return toolResultText(s.formatSearchResults(results)), nil
}

func (s *MCPServer) callFindSimilarTasks(args map[string]any) (any, *RPCError) {
	if s.ragEngine == nil {
		if s.fileLogger != nil {
			s.fileLogger.Warn("find_similar_tasks called but RAG engine is not available",
				zap.Bool("rag_enabled_in_config", s.config.RAGEnabled),
			)
		} else {
			fmt.Fprintf(os.Stderr, "WARN: find_similar_tasks called but RAG engine is nil\n")
		}
		return nil, &RPCError{Code: -32000, Message: "RAG engine not available"}
	}

	description, ok := getString(args, "description")
	if !ok || description == "" {
		return nil, &RPCError{Code: -32602, Message: "description parameter is required"}
	}

	limit := 5
	if l, ok := getInt(args, "limit"); ok && l > 0 {
		limit = l
	}

	results, err := s.ragEngine.Search(description, limit, "tasks", "")
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "task search failed", Data: err.Error()}
	}

	return toolResultText(s.formatTaskResults(results)), nil
}

func (s *MCPServer) callGetRelevantDocs(args map[string]any) (any, *RPCError) {
	if s.ragEngine == nil {
		if s.fileLogger != nil {
			s.fileLogger.Warn("get_relevant_docs called but RAG engine is not available",
				zap.Bool("rag_enabled_in_config", s.config.RAGEnabled),
			)
		} else {
			fmt.Fprintf(os.Stderr, "WARN: get_relevant_docs called but RAG engine is nil\n")
		}
		return nil, &RPCError{Code: -32000, Message: "RAG engine not available"}
	}

	topic, ok := getString(args, "topic")
	if !ok || topic == "" {
		return nil, &RPCError{Code: -32602, Message: "topic parameter is required"}
	}

	limit := 10
	if l, ok := getInt(args, "limit"); ok && l > 0 {
		limit = l
	}

	results, err := s.ragEngine.Search(topic, limit, "docs", "")
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "documentation search failed", Data: err.Error()}
	}

	return toolResultText(s.formatDocResults(results)), nil
}

func (s *MCPServer) callIndexDocuments(args map[string]any) (any, *RPCError) {
	if s.ragEngine == nil {
		if s.fileLogger != nil {
			s.fileLogger.Warn("index_documents called but RAG engine is not available")
		}
		return nil, &RPCError{Code: -32000, Message: "RAG engine not available"}
	}

	// Get document count before indexing
	docs, err := s.ragEngine.docService.CollectDocuments()
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "failed to collect documents", Data: err.Error()}
	}

	err = s.ragEngine.IndexDocuments()
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "document indexing failed", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Documents indexed successfully. Processed %d documents.", len(docs))), nil
}

// Result formatting functions

func (s *MCPServer) formatSearchResults(results *RAGSearchResponse) string {
	if len(results.Results) == 0 {
		return fmt.Sprintf("No results found for '%s'.", results.Query)
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("Found %d results for '%s':\n\n", len(results.Results), results.Query))

	for i, result := range results.Results {
		buf.WriteString(fmt.Sprintf("%d. **%s** (relevance: %.2f)\n", i+1, result.Title, result.Score))
		buf.WriteString(fmt.Sprintf("   Path: %s\n", result.Path))
		buf.WriteString(fmt.Sprintf("   Type: %s", result.Type))
		if result.Category != "" {
			buf.WriteString(fmt.Sprintf(" | Category: %s", result.Category))
		}
		buf.WriteString("\n")
		buf.WriteString(fmt.Sprintf("   Snippet: %s\n\n", result.Snippet))
	}

	buf.WriteString(fmt.Sprintf("Search time: %d ms", results.SearchTime))
	return buf.String()
}

func (s *MCPServer) formatTaskResults(results *RAGSearchResponse) string {
	if len(results.Results) == 0 {
		return "No similar tasks found."
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("Found %d similar tasks:\n\n", len(results.Results)))

	for i, result := range results.Results {
		buf.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, result.Title))
		buf.WriteString(fmt.Sprintf("   Task: %s\n", result.TaskSlug))
		buf.WriteString(fmt.Sprintf("   Phase: %s\n", result.TaskPhase))
		buf.WriteString(fmt.Sprintf("   Relevance: %.2f\n", result.Score))
		buf.WriteString(fmt.Sprintf("   Path: %s\n", result.Path))
		buf.WriteString(fmt.Sprintf("   Description: %s\n\n", result.Snippet))
	}

	return buf.String()
}

func (s *MCPServer) formatDocResults(results *RAGSearchResponse) string {
	if len(results.Results) == 0 {
		return "No relevant documentation found."
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("Found relevant documentation (%d results):\n\n", len(results.Results)))

	for i, result := range results.Results {
		buf.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, result.Title))
		buf.WriteString(fmt.Sprintf("   Category: %s\n", result.Category))
		buf.WriteString(fmt.Sprintf("   Relevance: %.2f\n", result.Score))
		buf.WriteString(fmt.Sprintf("   Path: %s\n", result.Path))
		buf.WriteString(fmt.Sprintf("   Content: %s\n\n", result.Snippet))
	}

	return buf.String()
}

// Memory tool implementations

func (s *MCPServer) callStoreMemory(args map[string]any) (any, *RPCError) {
	if s.memoryStore == nil {
		return nil, &RPCError{Code: -32000, Message: "Memory store not available"}
	}

	content, ok := getString(args, "content")
	if !ok || content == "" {
		return nil, &RPCError{Code: -32602, Message: "content parameter is required"}
	}

	memory := &Memory{
		Content:    content,
		Type:       MemoryTypeSemantic, // Default
		Importance: 0.5,
	}

	// Parse optional parameters
	if title, ok := getString(args, "title"); ok {
		memory.Title = title
	}

	if memType, ok := getString(args, "type"); ok {
		switch memType {
		case "episodic":
			memory.Type = MemoryTypeEpisodic
		case "semantic":
			memory.Type = MemoryTypeSemantic
		case "procedural":
			memory.Type = MemoryTypeProcedural
		case "working":
			memory.Type = MemoryTypeWorking
		}
	}

	if context, ok := getString(args, "context"); ok {
		memory.Context = context
	}

	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			if tagStr, ok := t.(string); ok {
				memory.Tags = append(memory.Tags, tagStr)
			}
		}
	}

	if importance, ok := args["importance"].(float64); ok && importance >= 0 && importance <= 1 {
		memory.Importance = importance
	}

	err := s.memoryStore.Store(memory)
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "failed to store memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory stored:\n- ID: %s\n- Type: %s\n- Title: %s",
		memory.ID, memory.Type, memory.Title)), nil
}

func (s *MCPServer) callRecallMemory(args map[string]any) (any, *RPCError) {
	if s.memoryStore == nil {
		return nil, &RPCError{Code: -32000, Message: "Memory store not available"}
	}

	query, ok := getString(args, "query")
	if !ok || query == "" {
		return nil, &RPCError{Code: -32602, Message: "query parameter is required"}
	}

	limit := 10
	if l, ok := getInt(args, "limit"); ok && l > 0 {
		limit = l
	}

	filters := MemoryFilters{}

	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		filters.Type = MemoryType(memType)
	}

	if context, ok := getString(args, "context"); ok {
		filters.Context = context
	}

	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			if tagStr, ok := t.(string); ok {
				filters.Tags = append(filters.Tags, tagStr)
			}
		}
	}

	results, err := s.memoryStore.Recall(query, filters, limit)
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "memory recall failed", Data: err.Error()}
	}

	return toolResultText(s.formatMemoryResults(query, results)), nil
}

func (s *MCPServer) callUpdateMemory(args map[string]any) (any, *RPCError) {
	if s.memoryStore == nil {
		return nil, &RPCError{Code: -32000, Message: "Memory store not available"}
	}

	id, ok := getString(args, "id")
	if !ok || id == "" {
		return nil, &RPCError{Code: -32602, Message: "id parameter is required"}
	}

	updates := MemoryUpdate{}

	if content, ok := getString(args, "content"); ok {
		updates.Content = content
	}
	if title, ok := getString(args, "title"); ok {
		updates.Title = title
	}
	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			if tagStr, ok := t.(string); ok {
				updates.Tags = append(updates.Tags, tagStr)
			}
		}
	}
	if importance, ok := args["importance"].(float64); ok {
		updates.Importance = &importance
	}

	err := s.memoryStore.Update(id, updates)
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "failed to update memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory updated (ID: %s)", id)), nil
}

func (s *MCPServer) callDeleteMemory(args map[string]any) (any, *RPCError) {
	if s.memoryStore == nil {
		return nil, &RPCError{Code: -32000, Message: "Memory store not available"}
	}

	id, ok := getString(args, "id")
	if !ok || id == "" {
		return nil, &RPCError{Code: -32602, Message: "id parameter is required"}
	}

	err := s.memoryStore.Delete(id)
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "failed to delete memory", Data: err.Error()}
	}

	return toolResultText(fmt.Sprintf("Memory deleted (ID: %s)", id)), nil
}

func (s *MCPServer) callListMemories(args map[string]any) (any, *RPCError) {
	if s.memoryStore == nil {
		return nil, &RPCError{Code: -32000, Message: "Memory store not available"}
	}

	limit := 20
	if l, ok := getInt(args, "limit"); ok && l > 0 {
		limit = l
	}

	filters := MemoryFilters{}

	if memType, ok := getString(args, "type"); ok && memType != "" && memType != "all" {
		filters.Type = MemoryType(memType)
	}

	if context, ok := getString(args, "context"); ok {
		filters.Context = context
	}

	memories, err := s.memoryStore.List(filters, limit)
	if err != nil {
		return nil, &RPCError{Code: -32000, Message: "failed to list memories", Data: err.Error()}
	}

	return toolResultText(s.formatMemoryList(memories)), nil
}

func (s *MCPServer) callMemoryStats(args map[string]any) (any, *RPCError) {
	if s.memoryStore == nil {
		return nil, &RPCError{Code: -32000, Message: "Memory store not available"}
	}

	total := s.memoryStore.Count()
	byType := s.memoryStore.CountByType()

	var buf bytes.Buffer
	buf.WriteString("**Agent Memory Statistics**\n\n")
	buf.WriteString(fmt.Sprintf("Total memories: **%d**\n\n", total))
	buf.WriteString("By type:\n")

	typeNames := map[MemoryType]string{
		MemoryTypeEpisodic:   "Episodic (events)",
		MemoryTypeSemantic:   "Semantic (facts)",
		MemoryTypeProcedural: "Procedural (patterns)",
		MemoryTypeWorking:    "Working (current context)",
	}

	for memType, name := range typeNames {
		count := byType[memType]
		buf.WriteString(fmt.Sprintf("- %s: %d\n", name, count))
	}

	return toolResultText(buf.String()), nil
}

// Memory result formatting

func (s *MCPServer) formatMemoryResults(query string, results []*MemorySearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No memories found for '%s'.", query)
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("Found %d memories for '%s':\n\n", len(results), query))

	for i, r := range results {
		m := r.Memory
		buf.WriteString(fmt.Sprintf("%d. **%s** (relevance: %.2f)\n", i+1, getMemoryTitle(m), r.Score))
		buf.WriteString(fmt.Sprintf("   ID: `%s`\n", m.ID))
		buf.WriteString(fmt.Sprintf("   Type: %s\n", formatMemoryType(m.Type)))

		if m.Context != "" {
			buf.WriteString(fmt.Sprintf("   Context: %s\n", m.Context))
		}
		if len(m.Tags) > 0 {
			buf.WriteString(fmt.Sprintf("   Tags: %v\n", m.Tags))
		}

		snippet := m.Content
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		buf.WriteString(fmt.Sprintf("   Content: %s\n", snippet))
		buf.WriteString(fmt.Sprintf("   Importance: %.1f | Access count: %d\n\n", m.Importance, m.AccessCount))
	}

	return buf.String()
}

func (s *MCPServer) formatMemoryList(memories []*Memory) string {
	if len(memories) == 0 {
		return "No memories found."
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("Memories (%d):\n\n", len(memories)))

	for i, m := range memories {
		buf.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, getMemoryTitle(m)))
		buf.WriteString(fmt.Sprintf("   ID: `%s`\n", m.ID))
		buf.WriteString(fmt.Sprintf("   Type: %s | Importance: %.1f\n", formatMemoryType(m.Type), m.Importance))

		if m.Context != "" {
			buf.WriteString(fmt.Sprintf("   Context: %s\n", m.Context))
		}

		snippet := m.Content
		if len(snippet) > 150 {
			snippet = snippet[:150] + "..."
		}
		buf.WriteString(fmt.Sprintf("   %s\n", snippet))
		buf.WriteString(fmt.Sprintf("   Created: %s\n\n", m.CreatedAt.Format("2006-01-02 15:04")))
	}

	return buf.String()
}

func getMemoryTitle(m *Memory) string {
	if m.Title != "" {
		return m.Title
	}
	// Generate title from first line of content
	firstLine := m.Content
	if idx := findNewline(firstLine); idx > 0 {
		firstLine = firstLine[:idx]
	}
	if len(firstLine) > 50 {
		firstLine = firstLine[:50] + "..."
	}
	return firstLine
}

func findNewline(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return i
		}
	}
	return -1
}

func formatMemoryType(t MemoryType) string {
	switch t {
	case MemoryTypeEpisodic:
		return "Episodic"
	case MemoryTypeSemantic:
		return "Semantic"
	case MemoryTypeProcedural:
		return "Procedural"
	case MemoryTypeWorking:
		return "Working"
	default:
		return string(t)
	}
}
