package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"github.com/ipiton/agent-memory-mcp/internal/search"
)

func (s *MCPServer) callRepoList(args map[string]any) (any, *rpcError) {
	path, _ := getString(args, "path")
	maxDepth := s.config.MaxDepth
	if val, ok := getInt(args, "max_depth"); ok {
		if val >= 0 {
			maxDepth = val
		}
	}

	if path == "" {
		roots := s.pathGuard.AllowedRoots()
		if len(roots) == 0 {
			path = "."
		} else {
			entries := make([]map[string]any, 0, len(roots))
			for _, root := range roots {
				entries = append(entries, map[string]any{
					"path": root.Rel,
					"type": typeLabel(!root.IsFile),
				})
			}
			return toolResultJSON(entries), nil
		}
	}

	abs, err := s.pathGuard.Resolve(path)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	entries, err := walkList(abs, maxDepth, s.pathGuard)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInternalError, Message: "failed to list", Data: err.Error()}
	}

	return toolResultJSON(entries), nil
}

func (s *MCPServer) callRepoRead(args map[string]any) (any, *rpcError) {
	path, ok := getString(args, "path")
	if !ok || path == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "path is required"}
	}
	maxBytes := s.config.MaxFileBytes
	if val, ok := getInt64(args, "max_bytes"); ok {
		if val > 0 {
			maxBytes = val
		}
	}
	offset := int64(0)
	if val, ok := getInt64(args, "offset"); ok {
		if val >= 0 {
			offset = val
		}
	}

	abs, err := s.pathGuard.Resolve(path)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInternalError, Message: "failed to stat file", Data: err.Error()}
	}
	if info.IsDir() {
		listing, err := listDirectory(abs, s.config.MaxDepth, s.pathGuard)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInternalError, Message: "failed to list directory", Data: err.Error()}
		}
		return map[string]any{"path": path, "content": listing}, nil
	}
	content, _, err := search.ReadTextFile(abs, offset, maxBytes, info.Size())
	if err != nil {
		return nil, &rpcError{Code: rpcErrInternalError, Message: err.Error()}
	}
	return toolResultText(fmt.Sprintf("path: %s\n\n%s", path, content)), nil
}

func (s *MCPServer) callRepoSearch(args map[string]any) (any, *rpcError) {
	query, ok := getString(args, "query")
	if !ok || query == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "query is required"}
	}
	path, _ := getString(args, "path")
	maxResults := s.config.MaxSearchResults
	if val, ok := getInt(args, "max_results"); ok {
		if val > 0 {
			maxResults = val
		}
	}
	matches, err := search.Repo(s.pathGuard, query, path, maxResults, s.config.MaxFileBytes)
	if err != nil {
		return nil, &rpcError{Code: rpcErrInternalError, Message: "search failed", Data: err.Error()}
	}
	return toolResultText(search.FormatMatches(matches)), nil
}

func walkList(abs string, maxDepth int, guard *paths.Guard) ([]map[string]any, error) {
	entries := make([]map[string]any, 0, 64)
	rootDepth := depth(abs)

	err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != abs && search.ShouldSkipDir(d.Name(), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		currentDepth := depth(path) - rootDepth
		if maxDepth >= 0 && currentDepth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == abs {
			return nil
		}
		rel, err := guard.RelPath(path)
		if err != nil {
			return nil
		}
		entry := map[string]any{
			"path": rel,
			"type": typeLabel(d.IsDir()),
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				entry["size"] = info.Size()
			}
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return entries, nil
}

func depth(path string) int {
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) {
		return 0
	}
	return len(strings.Split(clean, string(filepath.Separator)))
}

func typeLabel(isDir bool) string {
	if isDir {
		return "dir"
	}
	return "file"
}

func listDirectory(abs string, maxDepth int, guard *paths.Guard) (string, error) {
	entries, err := walkList(abs, maxDepth, guard)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		line := fmt.Sprintf("%s (%s)", entry["path"], entry["type"])
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "(empty)", nil
	}
	return strings.Join(lines, "\n"), nil
}
