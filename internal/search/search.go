// Package search provides text search across repository files.
package search

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/paths"
)

// Match represents a single search hit with file path, line number, and text.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

var skipDirs = map[string]struct{}{
	".git":              {},
	".idea":             {},
	".vscode":           {},
	"node_modules":      {},
	"dist":              {},
	"build":             {},
	".next":             {},
	"vendor":            {},
	"bin":               {},
	"coverage":          {},
	"test-results":      {},
	"artifacts":         {},
	"logs":              {},
	".agent-memory":     {},
	"playwright-report": {},
}

// ShouldSkipDir reports whether the directory should be excluded from search.
func ShouldSkipDir(name string, isDir bool) bool {
	if !isDir {
		return false
	}
	_, ok := skipDirs[name]
	return ok
}

// Repo searches for a query string across allowed repository files.
func Repo(guard *paths.Guard, query, relPath string, maxResults int, maxBytes int64) ([]Match, error) {
	roots := []string{}
	if relPath != "" {
		abs, err := guard.Resolve(relPath)
		if err != nil {
			return nil, err
		}
		roots = append(roots, abs)
	} else {
		for _, allowed := range guard.AllowedRoots() {
			if allowed.IsFile {
				roots = append(roots, allowed.Abs)
				continue
			}
			roots = append(roots, allowed.Abs)
		}
	}

	matches := make([]Match, 0, min(maxResults, 256))
	for _, root := range roots {
		if len(matches) >= maxResults {
			break
		}
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if info.IsDir() {
			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if path != root && ShouldSkipDir(d.Name(), d.IsDir()) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if d.IsDir() {
					return nil
				}
				if len(matches) >= maxResults {
					return io.EOF
				}
				if err := ScanFile(path, guard, query, maxBytes, maxResults, &matches); err != nil {
					if err == io.EOF {
						return io.EOF
					}
				}
				return nil
			})
			if err == io.EOF {
				break
			}
			continue
		}
		if err := ScanFile(root, guard, query, maxBytes, maxResults, &matches); err != nil {
			if err == io.EOF {
				break
			}
		}
	}

	return matches, nil
}

// ScanFile searches a single file for lines matching the query string.
func ScanFile(path string, guard *paths.Guard, query string, maxBytes int64, maxResults int, matches *[]Match) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if info.Size() > maxBytes {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	sample := make([]byte, 512)
	n, err := file.Read(sample)
	if err != nil && err != io.EOF {
		return nil
	}
	if IsBinary(sample[:n]) {
		return nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil
	}

	rel, err := guard.RelPath(path)
	if err != nil {
		return nil
	}

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		text := scanner.Text()
		if strings.Contains(text, query) {
			*matches = append(*matches, Match{
				Path: rel,
				Line: lineNum,
				Text: text,
			})
			if len(*matches) >= maxResults {
				return io.EOF
			}
		}
	}
	return nil
}

// IsBinary reports whether data contains null bytes, indicating a binary file.
func IsBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) != -1 {
		return true
	}
	return false
}

// FormatMatches formats search results as "path:line text" lines.
func FormatMatches(matches []Match) string {
	if len(matches) == 0 {
		return "(no matches)"
	}
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		lines = append(lines, fmt.Sprintf("%s:%d %s", match.Path, match.Line, match.Text))
	}
	return strings.Join(lines, "\n")
}
