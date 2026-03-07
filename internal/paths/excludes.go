package paths

// DefaultExcludeDirs contains directory names that should be excluded
// from both search and RAG indexing by default.
var DefaultExcludeDirs = map[string]struct{}{
	".git":              {},
	".idea":             {},
	".vscode":           {},
	".next":             {},
	".terraform":        {},
	".agent-memory":     {},
	"node_modules":      {},
	"vendor":            {},
	"bin":               {},
	"dist":              {},
	"build":             {},
	"coverage":          {},
	"logs":              {},
	"artifacts":         {},
	"test-results":      {},
	"playwright-report": {},
}

// ShouldSkipDir reports whether the named directory should be excluded.
func ShouldSkipDir(name string) bool {
	_, ok := DefaultExcludeDirs[name]
	return ok
}
