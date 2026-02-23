// Package paths provides path resolution and allowlist-based access control.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/config"
)

// AllowedPath represents a resolved allowlisted path with its metadata.
type AllowedPath struct {
	Rel    string
	Abs    string
	IsFile bool
}

// Guard enforces path-based access control against a configured allowlist.
type Guard struct {
	root    string
	allowed []AllowedPath
}

// NewGuard creates a Guard from the configured root and allowed paths.
// If no allowed paths are configured, the root path is used as the default allowlist.
func NewGuard(cfg config.Config) (*Guard, error) {
	paths := cfg.AllowedPaths
	if len(paths) == 0 {
		// Default: allow only the project root, not the entire filesystem
		paths = []string{"."}
	}

	allowed := make([]AllowedPath, 0, len(paths))
	for _, rel := range paths {
		abs := filepath.Join(cfg.RootPath, rel)
		abs = filepath.Clean(abs)
		if realPath, err := filepath.EvalSymlinks(abs); err == nil {
			abs = realPath
		}
		info, err := os.Stat(abs)
		isFile := false
		if err == nil {
			isFile = !info.IsDir()
		}
		allowed = append(allowed, AllowedPath{
			Rel:    filepath.Clean(rel),
			Abs:    abs,
			IsFile: isFile,
		})
	}
	return &Guard{root: cfg.RootPath, allowed: allowed}, nil
}

// Resolve converts a relative path to an absolute path and validates access.
func (g *Guard) Resolve(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("absolute paths are not allowed")
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") {
		return "", errors.New("path traversal is not allowed")
	}
	abs := filepath.Join(g.root, clean)
	abs = filepath.Clean(abs)
	resolved := abs
	if _, err := os.Lstat(abs); err == nil {
		if realPath, err := filepath.EvalSymlinks(abs); err == nil {
			resolved = realPath
		}
	}
	if !g.IsAllowed(resolved) {
		return "", fmt.Errorf("path not allowed: %s", clean)
	}
	return resolved, nil
}

// IsAllowed reports whether the absolute path is within the allowlist.
func (g *Guard) IsAllowed(abs string) bool {
	if len(g.allowed) == 0 {
		return true
	}

	for _, allowed := range g.allowed {
		if allowed.IsFile {
			if abs == allowed.Abs {
				return true
			}
			continue
		}
		if abs == allowed.Abs {
			return true
		}
		if strings.HasPrefix(abs, allowed.Abs+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AllowedRoots returns a copy of the configured allowed paths.
func (g *Guard) AllowedRoots() []AllowedPath {
	return append([]AllowedPath{}, g.allowed...)
}

// RelPath returns the slash-separated path relative to the root.
func (g *Guard) RelPath(abs string) (string, error) {
	rel, err := filepath.Rel(g.root, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
