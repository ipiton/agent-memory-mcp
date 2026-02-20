package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AllowedPath struct {
	Rel    string
	Abs    string
	IsFile bool
}

type PathGuard struct {
	root    string
	allowed []AllowedPath
}

func NewPathGuard(cfg Config) (*PathGuard, error) {
	allowed := make([]AllowedPath, 0, len(cfg.AllowedPaths))
	for _, rel := range cfg.AllowedPaths {
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
	return &PathGuard{root: cfg.RootPath, allowed: allowed}, nil
}

func (g *PathGuard) Resolve(rel string) (string, error) {
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

func (g *PathGuard) IsAllowed(abs string) bool {
	// If no allowed paths specified, allow everything
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

func (g *PathGuard) AllowedRoots() []AllowedPath {
	return append([]AllowedPath{}, g.allowed...)
}

func (g *PathGuard) RelPath(abs string) (string, error) {
	rel, err := filepath.Rel(g.root, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
