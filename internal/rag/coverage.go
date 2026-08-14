package rag

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// RootCoverage is what one configured index root contributed to the corpus.
type RootCoverage struct {
	Root   string `json:"root"`
	Files  int    `json:"files"`
	Chunks int    `json:"chunks"`
}

// CoverageReport describes what is actually in the index, per configured root.
//
// T97: `semantic_search` has no error state. A query against a corpus that
// never included the documents you meant returns "no results", which reads as
// "there is nothing like that" — and that is how a gap between
// MCP_INDEX_DIRS and what the canon promised its readers went unnoticed for
// months, with 853 archived tasks outside the corpus and four queries coming
// back empty. `index_documents` answered "Documents indexed successfully." and
// nothing else, so nobody could check without reading the log of a background
// process.
//
// The number that matters most is not the total; it is Empty — a root that was
// configured and produced no files at all. That is the state that used to be
// invisible, because a root contributing nothing looks exactly like a root that
// was never mentioned.
type CoverageReport struct {
	Roots  []RootCoverage `json:"roots"`
	Empty  []string       `json:"empty_roots,omitempty"`
	Other  RootCoverage   `json:"other,omitempty"`
	Files  int            `json:"total_files"`
	Chunks int            `json:"total_chunks"`
}

// Coverage attributes every indexed file to the configured root it came from.
func (re *Engine) Coverage() (*CoverageReport, error) {
	if re == nil || re.vecService == nil || re.docService == nil {
		return nil, fmt.Errorf("RAG engine not available")
	}

	indexed, err := re.vecService.store.GetAllIndexedFiles()
	if err != nil {
		return nil, fmt.Errorf("read indexed files: %w", err)
	}

	roots := re.docService.config.IndexDirs
	prefixes := make([]string, len(roots))
	for i, root := range roots {
		prefixes[i] = re.docService.indexRootPrefix(root)
	}

	perRoot := make(map[string]*RootCoverage, len(roots))
	for _, root := range roots {
		perRoot[root] = &RootCoverage{Root: root}
	}

	report := &CoverageReport{}
	for path, info := range indexed {
		chunks := 0
		if info != nil {
			chunks = info.ChunkCount
		}
		report.Files++
		report.Chunks += chunks

		// Longest prefix wins so nested roots ("docs" and "docs/adr") each get
		// credited for what is really theirs.
		best, bestLen := "", -1
		for i, prefix := range prefixes {
			if prefix == "" {
				continue
			}
			if matchesRootPrefix(path, prefix) && len(prefix) > bestLen {
				best, bestLen = roots[i], len(prefix)
			}
		}
		if best == "" {
			report.Other.Root = "(outside the configured roots)"
			report.Other.Files++
			report.Other.Chunks += chunks
			continue
		}
		perRoot[best].Files++
		perRoot[best].Chunks += chunks
	}

	for _, root := range roots {
		rc := perRoot[root]
		if rc.Files == 0 {
			report.Empty = append(report.Empty, root)
			continue
		}
		report.Roots = append(report.Roots, *rc)
	}
	sort.Slice(report.Roots, func(i, j int) bool { return report.Roots[i].Files > report.Roots[j].Files })

	return report, nil
}

// indexRootPrefix converts a configured index root into the repo-relative,
// slash-separated form that indexed file paths use.
func (ds *documentService) indexRootPrefix(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if filepath.IsAbs(root) {
		root = ds.relPath(root)
	}
	return strings.Trim(filepath.ToSlash(filepath.Clean(root)), "/")
}

// matchesRootPrefix reports whether an indexed path lives under prefix. The
// separator check keeps "docs" from claiming "docs-archive/x.md".
func matchesRootPrefix(path, prefix string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	if prefix == "." {
		return true
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// FormatCoverage renders the report for the index_documents tool response.
func FormatCoverage(r *CoverageReport) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Indexed %d files / %d chunks.\n", r.Files, r.Chunks)
	for _, rc := range r.Roots {
		fmt.Fprintf(&b, "  %s — %d files, %d chunks\n", rc.Root, rc.Files, rc.Chunks)
	}
	if r.Other.Files > 0 {
		fmt.Fprintf(&b, "  %s — %d files, %d chunks\n", r.Other.Root, r.Other.Files, r.Other.Chunks)
	}
	for _, root := range r.Empty {
		fmt.Fprintf(&b, "  ⚠️  %s — configured but contributed nothing\n", root)
	}
	return strings.TrimRight(b.String(), "\n")
}
