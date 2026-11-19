package rag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

func newDocumentContextTestEngine(t *testing.T, chunks []vectorstore.Chunk) *Engine {
	t.Helper()
	e := newRerankTestEngine(t, chunks)
	e.repoRoot = resolvedTestRoot(t, t.TempDir())
	metadata := map[string]string{indexRootMetadataKey: e.repoRoot, indexStateMetadataKey: indexStateReady}
	if err := e.vecService.store.CommitIndexState(vectorstore.IndexStateUpdate{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDocumentContextReadsSeveralSectionsWithinOneUnicodeBudget(t *testing.T) {
	engine := newDocumentContextTestEngine(t, []vectorstore.Chunk{
		breadcrumbChunk("docs/guide.md-0", "docs/guide.md", "Guide", []string{"Guide", "Plan"}, strings.Repeat("界", 400)),
		breadcrumbChunk("docs/guide.md-1", "docs/guide.md", "Guide", []string{"Guide", "Result"}, "The measurement is still pending."),
		breadcrumbChunk("docs/other.md-0", "docs/other.md", "Other", []string{"Other"}, "Must not leak"),
	})
	result, err := engine.DocumentContext(context.Background(), "docs/guide.md", ContextOptions{
		SectionKeys: []string{"Guide > Plan", "Guide > Result"}, MaxChars: 256, MaxSections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sections) != 2 || !result.Truncated {
		t.Fatalf("context = %+v", result)
	}
	if result.Sections[1].Text != "The measurement is still pending." {
		t.Fatalf("short section lost: %+v", result.Sections)
	}
	count := 0
	for _, section := range result.Sections {
		if !utf8.ValidString(section.Text) {
			t.Fatal("invalid UTF-8")
		}
		count += utf8.RuneCountInString(section.SectionKey) + utf8.RuneCountInString(section.Text)
	}
	if count != result.CharsReturned || count > 256 {
		t.Fatalf("count=%d, result=%+v", count, result)
	}
}

type contextTestEmbedder struct {
	embedder.Service
	result *embedder.EmbeddingResult
	err    error
	before func()
}

func (e contextTestEmbedder) EmbedQueryDetailed(context.Context, string) (*embedder.EmbeddingResult, error) {
	if e.before != nil {
		e.before()
	}
	return e.result, e.err
}

func semanticContextEngine(t *testing.T) *Engine {
	t.Helper()
	chunks := []vectorstore.Chunk{
		breadcrumbChunk("docs/a.md-0", "docs/a.md", "Guide", []string{"Guide", "Unrelated"}, "Unrelated context"),
		breadcrumbChunk("docs/a.md-1", "docs/a.md", "Guide", []string{"Guide", "Decision"}, "The decision"),
		breadcrumbChunk("docs/a.md-2", "docs/a.md", "Guide", []string{"Guide", "Limits"}, "The limitation"),
	}
	chunks[0].Embedding = []float32{0, 1}
	chunks[1].Embedding = []float32{1, 0}
	chunks[2].Embedding = []float32{0.8, 0.2}
	e := newDocumentContextTestEngine(t, chunks)
	e.vecService.config.MaxChunksPerDoc = 1
	e.vecService.config.Embedder = contextTestEmbedder{result: &embedder.EmbeddingResult{Embedding: []float32{1, 0}, ModelID: "fixture-model"}}
	if err := e.vecService.store.SetMetadata("embedding_model", "fixture-model"); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDocumentContextRanksSeveralSectionsWithinTheSelectedFile(t *testing.T) {
	e := semanticContextEngine(t)
	result, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{Query: "decision and limit", MaxSections: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Selection != "semantic" || len(result.Sections) != 2 || result.Sections[0].Text != "The decision" || result.Sections[1].Text != "The limitation" {
		t.Fatalf("context=%+v", result)
	}
	if result.SectionsOmitted != 1 || !result.Truncated {
		t.Fatalf("omission hidden: %+v", result)
	}
}

func TestDocumentContextRejectsIncompatibleOrFailedSemanticSelection(t *testing.T) {
	for _, test := range []struct {
		name   string
		result *embedder.EmbeddingResult
		err    error
	}{
		{"mismatched model", &embedder.EmbeddingResult{Embedding: []float32{1, 0}, ModelID: "other-model"}, nil},
		{"empty embedding", &embedder.EmbeddingResult{ModelID: "fixture-model"}, nil},
		{"dimension mismatch", &embedder.EmbeddingResult{Embedding: []float32{1, 0, 0}, ModelID: "fixture-model"}, nil},
		{"provider fallback", &embedder.EmbeddingResult{Embedding: []float32{1, 0}, ModelID: "fixture-model", Fallbacks: []string{"failed-provider"}}, nil},
		{"provider unavailable", nil, errors.New("unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := semanticContextEngine(t)
			e.vecService.config.Embedder = contextTestEmbedder{result: test.result, err: test.err}
			if _, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{Query: "question"}); err == nil {
				t.Fatal("invalid semantic selection accepted")
			}
			if _, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{SectionKeys: []string{"Guide > Limits"}}); err != nil {
				t.Fatalf("explicit read requires provider: %v", err)
			}
		})
	}
}

func TestDocumentContextPreservesNumericChunkOrderAndExplicitSelection(t *testing.T) {
	engine := newDocumentContextTestEngine(t, []vectorstore.Chunk{
		breadcrumbChunk("docs/a.md-10", "docs/a.md", "Guide", []string{"Guide", "Result"}, "third"),
		breadcrumbChunk("docs/a.md-2", "docs/a.md", "Guide", []string{"Guide", "Result"}, "second"),
		breadcrumbChunk("docs/a.md-1", "docs/a.md", "Guide", []string{"Guide", "Result"}, "first"),
		breadcrumbChunk("docs/a.md-11", "docs/a.md", "Guide", []string{"Guide", "Other"}, "unrequested"),
	})
	result, err := engine.DocumentContext(context.Background(), "docs/a.md", ContextOptions{SectionKeys: []string{"Guide > Result", "Guide > Result"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sections) != 1 || result.Sections[0].Text != "first\n\nsecond\n\nthird" || result.SectionsOmitted != 1 {
		t.Fatalf("result=%+v", result)
	}
	if _, err := engine.DocumentContext(context.Background(), "docs/a.md", ContextOptions{SectionKeys: []string{"Unknown"}}); err == nil {
		t.Fatal("unknown section accepted")
	}
}

func TestDocumentContextBindsVectorsToTheCapturedModelDuringRebuild(t *testing.T) {
	e := semanticContextEngine(t)
	e.vecService.config.Embedder = contextTestEmbedder{
		result: &embedder.EmbeddingResult{Embedding: []float32{0, 1}, ModelID: "replacement-model"},
		before: func() {
			if err := e.vecService.store.SetMetadata("embedding_model", "replacement-model"); err != nil {
				t.Fatal(err)
			}
		},
	}
	if _, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{Query: "question"}); err == nil {
		t.Fatal("old vectors accepted a replacement model query")
	}
}

func TestDocumentContextRefusesAnIncompleteIndex(t *testing.T) {
	e := semanticContextEngine(t)
	if err := e.vecService.store.SetMetadata(indexStateMetadataKey, indexStateDirty); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{SectionKeys: []string{"Guide > Limits"}}); err == nil {
		t.Fatal("incomplete index accepted")
	}
}

type contextNoCacheStore struct{ vectorstore.Store }

func (s contextNoCacheStore) RefreshIfStale() (bool, error) {
	return false, errors.New("global vector cache unavailable")
}

func TestDocumentContextDoesNotRequireTheGlobalVectorCache(t *testing.T) {
	e := newDocumentContextTestEngine(t, []vectorstore.Chunk{breadcrumbChunk("docs/a.md-0", "docs/a.md", "Guide", []string{"Guide"}, "Read from the document index")})
	e.vecService.store = contextNoCacheStore{Store: e.vecService.store}
	result, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{SectionKeys: []string{"Guide"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sections) != 1 || result.Sections[0].Text != "Read from the document index" {
		t.Fatalf("context=%+v", result)
	}
}

type contextChangingStore struct{ vectorstore.Store }

func (s contextChangingStore) ChunksByDocPath(path string) ([]vectorstore.Chunk, error) {
	chunks, err := s.Store.ChunksByDocPath(path)
	if err != nil {
		return nil, err
	}
	return chunks, s.Store.CommitIndexState(vectorstore.IndexStateUpdate{
		Metadata: map[string]string{"last_indexed": "fixture publication", indexStateMetadataKey: indexStateReady},
	})
}

func TestDocumentContextRejectsIndexPublicationDuringRead(t *testing.T) {
	e := semanticContextEngine(t)
	e.vecService.store = contextChangingStore{Store: e.vecService.store}
	_, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{Query: "question"})
	if err == nil || !strings.Contains(err.Error(), "index changed") {
		t.Fatalf("publication during document read = %v", err)
	}
}

func TestDocumentContextAgreesWithExpandSection(t *testing.T) {
	e := newDocumentContextTestEngine(t, []vectorstore.Chunk{
		breadcrumbChunk("a.md-10", "a.md", "Guide", []string{"Guide", "Steps"}, "after ten"),
		breadcrumbChunk("a.md-2", "a.md", "Guide", []string{"Guide", "Steps"}, "after two"),
	})
	const key = "Guide > Steps"
	expansion, err := e.ExpandSection(context.Background(), "a.md", key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.DocumentContext(context.Background(), "a.md", ContextOptions{SectionKeys: []string{key}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 1 || got.Sections[0].Text != expansion.FullText || expansion.FullText != "after two\n\nafter ten" {
		t.Fatalf("context = %+v, expansion = %+v", got, expansion)
	}
}

func TestDocumentContextIncludesChunksWithoutBreadcrumbsInMixedFile(t *testing.T) {
	const path = "guide.md"
	e := newDocumentContextTestEngine(t, []vectorstore.Chunk{
		breadcrumbChunk(path+"-0", path, "Guide", []string{path}, "First structured section."),
		{ID: path + "-1", DocPath: path, Title: "Guide", Content: "Headingless qualification.", Embedding: []float32{0, 1}},
	})
	got, err := e.DocumentContext(context.Background(), path, ContextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 2 || got.Sections[0].SectionKey == got.Sections[1].SectionKey ||
		got.Sections[1].Text != "Headingless qualification." || got.SectionsOmitted != 0 {
		t.Fatalf("mixed context = %+v", got)
	}
	selected, err := e.DocumentContext(context.Background(), path, ContextOptions{
		SectionKeys: []string{got.Sections[1].SectionKey},
	})
	if err != nil || len(selected.Sections) != 1 || selected.Sections[0].Text != "Headingless qualification." {
		t.Fatalf("explicit fallback key: context = %+v, error = %v", selected, err)
	}
}

type contextLegacyPublishingStore struct{ vectorstore.Store }

func (s contextLegacyPublishingStore) ChunksByDocPath(path string) ([]vectorstore.Chunk, error) {
	chunks, err := s.Store.ChunksByDocPath(path)
	if err != nil {
		return nil, err
	}
	return chunks, s.Store.SetMetadata("last_indexed", "legacy publication")
}

func TestDocumentContextDetectsPublicationByLegacyWriter(t *testing.T) {
	e := semanticContextEngine(t)
	if err := e.vecService.store.SetMetadata("index_generation", "1"); err != nil {
		t.Fatal(err)
	}
	e.vecService.store = contextLegacyPublishingStore{Store: e.vecService.store}
	_, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{Query: "question"})
	if err == nil || !strings.Contains(err.Error(), "index changed") {
		t.Fatalf("legacy publication during document read = %v", err)
	}
}

func TestDocumentContextKeepsAuthorizedRootForTheRead(t *testing.T) {
	e := semanticContextEngine(t)
	authorized := resolvedTestRoot(t, t.TempDir())
	alias := filepath.Join(t.TempDir(), "moving-root")
	// The alias now points to a different root than the one the guard checked.
	if err := os.Symlink(e.repoRoot, alias); err != nil {
		t.Fatal(err)
	}
	e.repoRoot = alias
	_, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{
		AuthorizedRoot: authorized,
		SectionKeys:    []string{"Guide > Limits"},
	})
	if err == nil || !strings.Contains(err.Error(), "index source root does not match") {
		t.Fatalf("index authorized through a moved alias: %v", err)
	}
}
