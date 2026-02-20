package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

func (s *MCPServer) handleResourcesList(_ json.RawMessage) (any, *RPCError) {
	roots := s.pathGuard.AllowedRoots()
	resources := make([]Resource, 0, len(roots))
	for _, root := range roots {
		uri := buildURI(root.Rel)
		name := filepath.Base(root.Rel)
		if name == "." || name == "" {
			name = root.Rel
		}
		resources = append(resources, Resource{
			URI:         uri,
			Name:        name,
			Description: fmt.Sprintf("Repository path: %s", root.Rel),
		})
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].URI < resources[j].URI
	})
	return map[string]any{"resources": resources}, nil
}

func (s *MCPServer) handleResourcesRead(params json.RawMessage) (any, *RPCError) {
	start := time.Now()
	var req struct {
		URI string `json:"uri"`
	}
	var rpcErr *RPCError
	var relPath string
	defer func() {
		if s.stats == nil {
			return
		}
		event := StatsEvent{
			Event:      "resource_read",
			Method:     "resources/read",
			Path:       relPath,
			DurationMs: time.Since(start).Milliseconds(),
			Success:    rpcErr == nil,
		}
		if rpcErr != nil {
			event.Error = rpcErr.Message
		}
		s.stats.Log(event)
	}()
	if err := json.Unmarshal(params, &req); err != nil {
		rpcErr = &RPCError{Code: -32602, Message: "invalid params", Data: err.Error()}
		return nil, rpcErr
	}
	if req.URI == "" {
		rpcErr = &RPCError{Code: -32602, Message: "uri is required"}
		return nil, rpcErr
	}
	path, err := parseURI(req.URI)
	if err != nil {
		rpcErr = &RPCError{Code: -32602, Message: err.Error()}
		return nil, rpcErr
	}
	relPath = path

	abs, err := s.pathGuard.Resolve(path)
	if err != nil {
		rpcErr = &RPCError{Code: -32602, Message: err.Error()}
		return nil, rpcErr
	}

	info, err := os.Stat(abs)
	if err != nil {
		rpcErr = &RPCError{Code: -32603, Message: "failed to stat path", Data: err.Error()}
		return nil, rpcErr
	}

	if info.IsDir() {
		listing, err := listDirectory(abs, s.config.MaxDepth, s.pathGuard)
		if err != nil {
			rpcErr = &RPCError{Code: -32603, Message: "failed to list directory", Data: err.Error()}
			return nil, rpcErr
		}
		return map[string]any{"contents": []ResourceContent{
			{
				URI:      req.URI,
				MimeType: "text/plain",
				Text:     listing,
			},
		}}, nil
	}

	content, _, err := readTextFile(abs, 0, s.config.MaxFileBytes, info.Size())
	if err != nil {
		rpcErr = &RPCError{Code: -32603, Message: err.Error()}
		return nil, rpcErr
	}

	mimeType := mime.TypeByExtension(filepath.Ext(abs))
	return map[string]any{"contents": []ResourceContent{
		{
			URI:      req.URI,
			MimeType: mimeType,
			Text:     content,
		},
	}}, nil
}

func buildURI(rel string) string {
	clean := filepath.ToSlash(rel)
	return "memory://" + strings.TrimPrefix(clean, "/")
}

func parseURI(uri string) (string, error) {
	if !strings.HasPrefix(uri, "memory://") {
		return "", fmt.Errorf("unsupported uri: %s", uri)
	}
	path := strings.TrimPrefix(uri, "memory://")
	path = strings.TrimPrefix(path, "/")
	return filepath.Clean(path), nil
}
