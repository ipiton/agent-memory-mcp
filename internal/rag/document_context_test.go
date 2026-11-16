package rag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/embedder"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

func newDocumentContextTestEngine(t *testing.T, chunks []vectorstore.Chunk) *Engine {
	t.Helper()
	e := newRerankTestEngine(t, chunks)
	e.repoRoot = t.TempDir()
	metadata := map[string]string{indexRootMetadataKey: e.repoRoot, indexStateMetadataKey: indexStateReady}
	for _, chunk := range chunks {
		path := filepath.Join(e.repoRoot, chunk.DocPath)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("Fixture source."), 0600); err != nil {
			t.Fatal(err)
		}
		physical, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		metadata[vectorstore.SourcePathMetadataPrefix+chunk.DocPath] = physical
	}
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

func TestDocumentContextKeepsTheAuthorizedSourceAcrossSymlinkRetargets(t *testing.T) {
	e := newDocumentContextTestEngine(t, []vectorstore.Chunk{breadcrumbChunk("docs/a.md-0", "docs/a.md", "Guide", []string{"Guide"}, "Indexed source A")})
	a := filepath.Join(t.TempDir(), "a")
	link := filepath.Join(e.repoRoot, "docs")
	if err := os.Rename(link, a); err != nil {
		t.Fatal(err)
	}
	physicalA, err := filepath.EvalSymlinks(filepath.Join(a, "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.vecService.store.SetMetadata(vectorstore.SourcePathMetadataPrefix+"docs/a.md", physicalA); err != nil {
		t.Fatal(err)
	}
	b := t.TempDir()
	if err := os.WriteFile(filepath.Join(b, "a.md"), []byte("Source B"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	guard, err := paths.NewGuard(config.Config{RootPath: e.repoRoot, AllowedPaths: []string{"docs"}})
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := guard.Resolve("docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{AuthorizedPath: authorized}); err == nil {
		t.Fatal("source A disclosed using source B authorization")
	}
}

type contextSnapshotOnlyStore struct{ vectorstore.Store }

func (s contextSnapshotOnlyStore) RefreshIfStale() (bool, error) {
	return false, errors.New("global vector cache unavailable")
}

func TestDocumentContextDoesNotRequireTheGlobalVectorCache(t *testing.T) {
	e := newDocumentContextTestEngine(t, []vectorstore.Chunk{breadcrumbChunk("docs/a.md-0", "docs/a.md", "Guide", []string{"Guide"}, "Read from the document snapshot")})
	e.vecService.store = contextSnapshotOnlyStore{Store: e.vecService.store}
	result, err := e.DocumentContext(context.Background(), "docs/a.md", ContextOptions{SectionKeys: []string{"Guide"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sections) != 1 || result.Sections[0].Text != "Read from the document snapshot" {
		t.Fatalf("context=%+v", result)
	}
}
