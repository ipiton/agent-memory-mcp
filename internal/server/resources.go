package server

import (
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/search"
	"github.com/ipiton/agent-memory-mcp/internal/stats"
)

type resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type resourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

func (s *MCPServer) handleResourcesList(_ json.RawMessage) (any, *rpcError) {
	roots := s.pathGuard.AllowedRoots()
	resources := make([]resource, 0, len(roots))
	for _, root := range roots {
		uri := buildURI(root.Rel)
		name := filepath.Base(root.Rel)
		if name == "." || name == "" {
			name = root.Rel
		}
		resources = append(resources, resource{
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

func (s *MCPServer) handleResourcesRead(params json.RawMessage) (any, *rpcError) {
	start := time.Now()
	var req struct {
		URI string `json:"uri"`
	}
	var rErr *rpcError
	var relPath string
	defer func() {
		if s.stats == nil {
			return
		}
		event := stats.Event{
			EventName:  "resource_read",
			Method:     "resources/read",
			Path:       relPath,
			DurationMs: time.Since(start).Milliseconds(),
			Success:    rErr == nil,
		}
		if rErr != nil {
			event.Error = rErr.Message
		}
		s.stats.Log(event)
	}()
	if err := json.Unmarshal(params, &req); err != nil {
		rErr = &rpcError{Code: rpcErrInvalidParams, Message: "invalid params", Data: err.Error()}
		return nil, rErr
	}
	if req.URI == "" {
		rErr = &rpcError{Code: rpcErrInvalidParams, Message: "uri is required"}
		return nil, rErr
	}
	path, err := parseURI(req.URI)
	if err != nil {
		rErr = &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		return nil, rErr
	}
	relPath = path

	abs, err := s.pathGuard.Resolve(path)
	if err != nil {
		rErr = &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		return nil, rErr
	}

	info, err := os.Stat(abs)
	if err != nil {
		rErr = &rpcError{Code: rpcErrInternalError, Message: "failed to stat path", Data: err.Error()}
		return nil, rErr
	}

	if info.IsDir() {
		listing, err := listDirectory(abs, s.config.MaxDepth, s.pathGuard)
		if err != nil {
			rErr = &rpcError{Code: rpcErrInternalError, Message: "failed to list directory", Data: err.Error()}
			return nil, rErr
		}
		return map[string]any{"contents": []resourceContent{
			{
				URI:      req.URI,
				MimeType: "text/plain",
				Text:     listing,
			},
		}}, nil
	}

	content, _, err := search.ReadTextFile(abs, 0, s.config.MaxFileBytes, info.Size())
	if err != nil {
		rErr = &rpcError{Code: rpcErrInternalError, Message: err.Error()}
		return nil, rErr
	}

	mimeType := mime.TypeByExtension(filepath.Ext(abs))
	return map[string]any{"contents": []resourceContent{
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
