package rag

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

const (
	DefaultContextChars    = 12000
	MaxContextChars        = 32000
	DefaultContextSections = 4
	MaxContextSections     = 8
)

// ContextOptions chooses indexed sections within one document. A query and
// explicit section keys are mutually exclusive; neither selects document order.
type ContextOptions struct {
	// AuthorizedRoot is the resolved index root used by the server's path guard.
	// Keep that identity for the entire read even if the configured alias moves.
	AuthorizedRoot string   `json:"-"`
	Query          string   `json:"query,omitempty"`
	SectionKeys    []string `json:"section_keys,omitempty"`
	MaxChars       int      `json:"max_chars,omitempty"`
	MaxSections    int      `json:"max_sections,omitempty"`
}

type ContextSection struct {
	SectionKey string `json:"section_key"`
	Text       string `json:"text"`
	CharsTotal int    `json:"chars_total"`
	Truncated  bool   `json:"truncated"`
}

// DocumentContext contains indexed text, not a live read or a complete answer.
// MaxChars counts Unicode code points in section keys plus text. JSON metadata
// is outside that source-text budget.
type DocumentContext struct {
	Path              string           `json:"path"`
	Source            string           `json:"source"`
	Selection         string           `json:"selection"`
	MaxChars          int              `json:"max_chars"`
	CharsReturned     int              `json:"chars_returned"`
	SectionsAvailable int              `json:"sections_available"`
	SectionsOmitted   int              `json:"sections_omitted"`
	Truncated         bool             `json:"truncated"`
	LastModified      time.Time        `json:"last_modified"`
	Sections          []ContextSection `json:"sections"`
}

func (options ContextOptions) Validate() error {
	if options.MaxChars != 0 && (options.MaxChars < 256 || options.MaxChars > MaxContextChars) {
		return fmt.Errorf("max_chars must be between 256 and %d", MaxContextChars)
	}
	if options.MaxSections < 0 || options.MaxSections > MaxContextSections {
		return fmt.Errorf("max_sections must be between 1 and %d", MaxContextSections)
	}
	if len(options.SectionKeys) > MaxContextSections {
		return fmt.Errorf("too many section_keys")
	}
	if strings.TrimSpace(options.Query) != "" && len(options.SectionKeys) > 0 {
		return fmt.Errorf("query and section_keys are mutually exclusive")
	}
	for _, key := range options.SectionKeys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("section_keys must not contain empty keys")
		}
	}
	return nil
}

type contextCandidate struct {
	key   string
	text  string
	score float64
	order int
}

// DocumentContext reads several sections from a single indexed document. Query
// selection ranks sections by their best chunk, without the global document cap.
func (re *Engine) DocumentContext(ctx context.Context, docPath string, options ContextOptions) (*DocumentContext, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if re == nil || re.vecService == nil || re.vecService.store == nil {
		return nil, fmt.Errorf("RAG engine not available")
	}
	if strings.TrimSpace(docPath) == "" {
		return nil, fmt.Errorf("path is required")
	}
	if options.MaxChars == 0 {
		options.MaxChars = DefaultContextChars
	}
	if options.MaxSections == 0 {
		options.MaxSections = DefaultContextSections
	}
	chunks, model, err := re.readContextChunks(docPath, options.AuthorizedRoot, strings.TrimSpace(options.Query) != "")
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("document is not indexed: %s", docPath)
	}

	var queryVector []float32
	selection := "document_order"
	if strings.TrimSpace(options.Query) != "" {
		queryVector, err = re.contextQueryVector(ctx, options.Query, model)
		if err != nil {
			return nil, err
		}
		selection = "semantic"
	} else if len(options.SectionKeys) > 0 {
		selection = "explicit"
	}

	indexed := collectIndexedSections(chunks, docPath)

	var candidates []contextCandidate
	positions := make(map[string]int, len(indexed))
	for i, section := range indexed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := contextCandidate{key: section.key, text: section.text(), order: i}
		if queryVector != nil {
			candidate.score = -1
			for _, vector := range section.vectors {
				if len(vector) != len(queryVector) {
					return nil, fmt.Errorf("document vectors are missing or incompatible; run index_documents")
				}
				candidate.score = max(candidate.score, vectorstore.CosineSimilarity(queryVector, vector))
			}
		}
		positions[candidate.key] = i
		candidates = append(candidates, candidate)
	}
	available := len(candidates)
	if selection == "semantic" {
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	} else if selection == "explicit" {
		wanted := map[string]bool{}
		for _, key := range options.SectionKeys {
			if _, ok := positions[key]; !ok {
				return nil, fmt.Errorf("section is not indexed: %s", key)
			}
			wanted[key] = true
		}
		filtered := candidates[:0]
		for _, c := range candidates {
			if wanted[c.key] {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}
	if len(candidates) > options.MaxSections {
		candidates = candidates[:options.MaxSections]
	}
	sections, chars := boundedContextSections(candidates, options.MaxChars)
	response := &DocumentContext{Path: docPath, Source: "index", Selection: selection, MaxChars: options.MaxChars,
		SectionsAvailable: available, SectionsOmitted: available - len(sections), Sections: sections,
		CharsReturned: chars, LastModified: chunks[0].LastModified}
	response.Truncated = response.SectionsOmitted > 0
	for _, section := range sections {
		response.Truncated = response.Truncated || section.Truncated
	}
	return response, nil
}

func boundedContextSections(candidates []contextCandidate, maxChars int) ([]ContextSection, int) {
	// Keep every selected heading intact. If even headings exceed the budget,
	// retain the highest-ranked sections first and mark all omitted context.
	headings := 0
	selected := candidates[:0]
	for _, c := range candidates {
		n := utf8.RuneCountInString(c.key)
		if headings+n <= maxChars {
			selected = append(selected, c)
			headings += n
		}
	}
	candidates = selected
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].order < candidates[j].order })
	// Share the budget fairly: a large first section cannot hide every later
	// section. Reassign unused shares from short sections before truncating.
	allocations := make([]int, len(candidates))
	remaining := maxChars - headings
	for remaining > 0 {
		active := 0
		for i, c := range candidates {
			if allocations[i] < utf8.RuneCountInString(c.text) {
				active++
			}
		}
		if active == 0 {
			break
		}
		share := max(remaining/active, 1)
		for i, c := range candidates {
			n := min(share, utf8.RuneCountInString(c.text)-allocations[i], remaining)
			allocations[i] += n
			remaining -= n
		}
	}
	sections, chars := []ContextSection{}, 0
	for i, c := range candidates {
		text := []rune(c.text)
		part := ContextSection{SectionKey: c.key, Text: string(text[:allocations[i]]), CharsTotal: len(text), Truncated: allocations[i] < len(text)}
		chars += utf8.RuneCountInString(c.key) + allocations[i]
		sections = append(sections, part)
	}
	return sections, chars
}

// Indexing marks the store dirty before writes and advances its publication
// stamps at the end of a run. Bracket the ordinary document read to detect a
// publication by the writer while loading its chunks and metadata.
func (re *Engine) readContextChunks(docPath, expectedRoot string, semantic bool) ([]vectorstore.Chunk, string, error) {
	store := re.vecService.store
	metadata := func(key string) (string, error) {
		value, err := store.GetMetadata(key)
		if errors.Is(err, vectorstore.ErrMetadataNotFound) {
			return "", nil
		}
		return value, err
	}
	version := func() ([2]string, error) {
		generation, err := metadata("index_generation")
		if err != nil {
			return [2]string{}, err
		}
		stamp, err := metadata("last_indexed")
		return [2]string{generation, stamp}, err
	}
	before, err := version()
	if err != nil {
		return nil, "", err
	}
	state, err := store.GetMetadata(indexStateMetadataKey)
	if err != nil || state != indexStateReady {
		return nil, "", fmt.Errorf("index is incomplete or rebuilding; finish index_documents before reading context")
	}
	recordedRoot, err := store.GetMetadata(indexRootMetadataKey)
	if err != nil || recordedRoot == "" {
		return nil, "", fmt.Errorf("index source root is unknown; run index_documents")
	}
	if expectedRoot == "" {
		expectedRoot, err = filepath.EvalSymlinks(re.repoRoot)
		if err != nil {
			return nil, "", fmt.Errorf("resolve index root: %w", err)
		}
	}
	if filepath.Clean(recordedRoot) != expectedRoot {
		return nil, "", fmt.Errorf("index source root does not match this repository; use a separate index or re-index a legacy symlinked root")
	}

	var model string
	if semantic {
		model, err = store.GetMetadata("embedding_model")
		if err != nil || model == "" {
			return nil, "", fmt.Errorf("semantic context requires a vector index; run index_documents or use section_keys")
		}
	}
	chunks, err := store.ChunksByDocPath(docPath)
	if err != nil {
		return nil, "", fmt.Errorf("load indexed document: %w", err)
	}

	state, err = store.GetMetadata(indexStateMetadataKey)
	if err != nil || state != indexStateReady {
		return nil, "", fmt.Errorf("index is incomplete or rebuilding; finish index_documents before reading context")
	}
	after, err := version()
	if err != nil {
		return nil, "", err
	}
	if before != after {
		return nil, "", fmt.Errorf("index changed while reading context; retry")
	}
	return chunks, model, nil
}

func (re *Engine) contextQueryVector(ctx context.Context, query, model string) ([]float32, error) {
	result, err := re.vecService.config.Embedder.EmbedQueryDetailed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed context query: %w", err)
	}
	if result == nil || len(result.Embedding) == 0 {
		return nil, fmt.Errorf("context query has no embedding")
	}
	if len(result.Fallbacks) > 0 {
		return nil, fmt.Errorf("document context does not allow embedding fallback")
	}
	if model == "" || model != result.ModelID {
		return nil, fmt.Errorf("context embedding model mismatch or unknown; run index_documents")
	}
	return result.Embedding, nil
}
