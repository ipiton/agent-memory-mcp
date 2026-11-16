package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/userio"
)

func (s *MCPServer) callDocumentContext(args map[string]any) (any, *rpcError) {
	var invalid = func(message string) (any, *rpcError) {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: message}
	}
	for key := range args {
		if args[key] == nil {
			return invalid(key + " must not be null")
		}
		switch key {
		case "path", "query", "section_keys", "max_chars", "max_sections":
		default:
			return invalid("unknown document_context argument: " + key)
		}
	}
	params, err := parseParams[struct {
		Path        string   `json:"path"`
		Query       string   `json:"query"`
		SectionKeys []string `json:"section_keys"`
		MaxChars    *int     `json:"max_chars"`
		MaxSections *int     `json:"max_sections"`
	}](args)
	if err != nil {
		return invalid(err.Error())
	}
	if strings.TrimSpace(params.Path) == "" {
		return invalid("path is required")
	}
	options := rag.ContextOptions{Query: params.Query, SectionKeys: params.SectionKeys}
	if _, present := args["section_keys"]; present && len(params.SectionKeys) == 0 {
		return invalid("section_keys must not be empty")
	}
	if params.MaxChars != nil {
		options.MaxChars = *params.MaxChars
		if options.MaxChars == 0 {
			return invalid("max_chars must be positive")
		}
	}
	if params.MaxSections != nil {
		options.MaxSections = *params.MaxSections
		if options.MaxSections == 0 {
			return invalid("max_sections must be positive")
		}
	}
	if err := options.Validate(); err != nil {
		return invalid(err.Error())
	}
	if params.Query != "" {
		if err := userio.ValidateQuery(params.Query); err != nil {
			return invalid(err.Error())
		}
	}
	abs, err := s.pathGuard.Resolve(params.Path)
	if err != nil {
		return invalid(err.Error())
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return invalid("path must identify an existing allowed regular file")
	}
	indexRoot := s.config.RAG.IndexRoot
	if indexRoot == "" {
		indexRoot = s.config.RootPath
	}
	logicalPath := filepath.ToSlash(filepath.Clean(params.Path))
	indexedPath, err := filepath.EvalSymlinks(filepath.Join(indexRoot, logicalPath))
	if err != nil || indexedPath != abs {
		return invalid("indexed source does not match the allowed file")
	}
	if err := s.requireRAGEngine(); err != nil {
		return nil, err
	}
	options.AuthorizedPath = abs
	response, err := s.getRagEngine().DocumentContext(context.Background(), logicalPath, options)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: err.Error()}
	}
	return toolResultJSON(response), nil
}
