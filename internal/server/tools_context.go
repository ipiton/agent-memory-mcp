package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
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
	engine, guard, indexRoot := s.documentContextReader()
	if guard == nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "index path guard not available"}
	}
	root, err := filepath.EvalSymlinks(indexRoot)
	if err != nil {
		return invalid("index root is unavailable")
	}
	if rel, err := guard.RelPath(root); err != nil || rel != "." {
		return invalid("index root changed since path authorization was configured; restart the server")
	}
	abs, err := guard.Resolve(params.Path)
	if err != nil {
		return invalid(err.Error())
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return invalid("path must identify an existing allowed regular file")
	}
	if engine == nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: "RAG engine not available"}
	}
	options.AuthorizedRoot = root
	logicalPath := filepath.ToSlash(filepath.Clean(params.Path))
	response, err := engine.DocumentContext(context.Background(), logicalPath, options)
	if err != nil {
		return nil, &rpcError{Code: rpcErrServerError, Message: err.Error()}
	}
	return toolResultJSON(response), nil
}

// Use the same configured allowlist relative to the tree that owns the index.
// Preserve the original rules on reload, just as repository file reads do.
func newIndexPathGuard(indexRoot string, repoGuard *paths.Guard) *paths.Guard {
	if indexRoot == "" || repoGuard == nil {
		return nil
	}
	roots := repoGuard.AllowedRoots()
	cfg := config.Config{RootPath: indexRoot, AllowedPaths: make([]string, 0, len(roots))}
	for _, root := range roots {
		cfg.AllowedPaths = append(cfg.AllowedPaths, root.Rel)
	}
	guard, _ := paths.NewGuard(cfg)
	return guard
}

func (s *MCPServer) documentContextReader() (*rag.Engine, *paths.Guard, string) {
	s.ragMu.RLock()
	defer s.ragMu.RUnlock()
	if s.config.RAG.IndexRoot != "" {
		return s.ragEngine, s.indexPathGuard, s.config.RAG.IndexRoot
	}
	return s.ragEngine, s.pathGuard, s.config.RootPath
}
