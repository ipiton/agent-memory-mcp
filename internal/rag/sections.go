package rag

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

// SectionExpansion is the result of ExpandSection: the resolved doc and
// section, plus every chunk that belongs to the section in document order
// (with the breadcrumb prefix stripped from each chunk so callers see only
// the section body). FullText is the chunks joined by a blank line — useful
// for an LLM call that wants the entire section in one go.
type SectionExpansion struct {
	DocPath     string   `json:"doc_path"`
	SectionPath []string `json:"section_path"`
	SectionKey  string   `json:"section_key"`
	Chunks      []string `json:"chunks"`
	FullText    string   `json:"full_text"`
}

// ExpandSection returns every chunk belonging to (docPath, sectionKey) in
// document order. sectionKey is the canonical " > "-joined breadcrumb path
// surfaced by SearchResult.SectionKey — pass it back unchanged.
//
// Intended use: caller runs Search, finds a relevant chunk, then calls
// ExpandSection to load the whole containing section before handing context
// to the LLM. This is the "pointer-based context" half of T49: search
// returns a pointer (chunk + section), expansion materialises the full
// section.
//
// Returns an error if the engine has no vector store. Returns a result with
// Chunks==nil when no chunks match — callers should treat that as a
// not-found, not an error.
func (re *Engine) ExpandSection(ctx context.Context, docPath, sectionKey string) (*SectionExpansion, error) {
	if re == nil || re.vecService == nil || re.vecService.store == nil {
		return nil, fmt.Errorf("RAG engine not available")
	}
	docPath = strings.TrimSpace(docPath)
	sectionKey = strings.TrimSpace(sectionKey)
	if docPath == "" || sectionKey == "" {
		return nil, fmt.Errorf("doc_path and section_key are required")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	chunks, err := re.vecService.store.ChunksByDocPath(docPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load chunks for %s: %w", docPath, err)
	}
	for _, section := range collectIndexedSections(chunks, "") {
		if section.key == sectionKey {
			return &SectionExpansion{
				DocPath: docPath, SectionKey: sectionKey, SectionPath: section.path,
				Chunks: section.bodies, FullText: section.text(),
			}, nil
		}
	}
	return &SectionExpansion{DocPath: docPath, SectionKey: sectionKey}, nil
}

type indexedSection struct {
	key     string
	path    []string
	bodies  []string
	vectors [][]float32
}

func (section indexedSection) text() string { return strings.Join(section.bodies, "\n\n") }

// An empty unsectionedKey preserves ExpandSection's breadcrumb-only contract.
func collectIndexedSections(chunks []vectorstore.Chunk, unsectionedKey string) []indexedSection {
	// IDs use unpadded sequence numbers, so lexical order alone puts 10 before 2.
	sequence := func(id string) (int, bool) {
		n, err := strconv.Atoi(id[strings.LastIndex(id, "-")+1:])
		return n, err == nil
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		a, okA := sequence(chunks[i].ID)
		b, okB := sequence(chunks[j].ID)
		if okA != okB {
			return okA
		}
		if okA && a != b {
			return a < b
		}
		return chunks[i].ID < chunks[j].ID
	})

	var sections []indexedSection
	positions := make(map[string]int)
	unsectioned := -1
	for _, chunk := range chunks {
		path, body, ok := ExtractBreadcrumb(chunk.Content)
		var pos int
		if ok {
			key := SectionKey(path)
			var exists bool
			pos, exists = positions[key]
			if !exists {
				pos = len(sections)
				positions[key] = pos
				sections = append(sections, indexedSection{key: key, path: path})
			}
		} else {
			if unsectionedKey == "" {
				continue
			}
			if unsectioned == -1 {
				unsectioned = len(sections)
				sections = append(sections, indexedSection{})
			}
			pos = unsectioned
		}
		sections[pos].bodies = append(sections[pos].bodies, body)
		sections[pos].vectors = append(sections[pos].vectors, chunk.Embedding)
	}
	if unsectioned != -1 {
		key := unsectionedKey
		for suffix := 1; ; suffix++ {
			if _, exists := positions[key]; !exists {
				break
			}
			key = fmt.Sprintf("%s (unsectioned %d)", unsectionedKey, suffix)
		}
		sections[unsectioned].key = key
	}
	return sections
}
