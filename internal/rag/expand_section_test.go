package rag

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
)

func breadcrumbChunk(id, docPath, title string, sectionPath []string, body string) vectorstore.Chunk {
	prefix := "[" + strings.Join(sectionPath, " > ") + "]\n\n"
	return vectorstore.Chunk{
		ID:           id,
		DocPath:      docPath,
		Title:        title,
		Content:      prefix + body,
		LastModified: time.Now(),
		Embedding:    []float32{0.1, 0.2},
	}
}

func TestExpandSection_ReturnsAllChunksOfSectionInOrder(t *testing.T) {
	chunks := []vectorstore.Chunk{
		breadcrumbChunk("docs/runbook.md-0", "docs/runbook.md", "Deploy Runbook",
			[]string{"Deploy Runbook"}, "preamble body"),
		breadcrumbChunk("docs/runbook.md-1", "docs/runbook.md", "Deploy Runbook",
			[]string{"Deploy Runbook", "Rollback"}, "first part of rollback"),
		breadcrumbChunk("docs/runbook.md-2", "docs/runbook.md", "Deploy Runbook",
			[]string{"Deploy Runbook", "Rollback"}, "second part of rollback"),
		breadcrumbChunk("docs/runbook.md-3", "docs/runbook.md", "Deploy Runbook",
			[]string{"Deploy Runbook", "Verification"}, "verification body"),
	}
	engine := newRerankTestEngine(t, chunks)

	expansion, err := engine.ExpandSection(context.Background(), "docs/runbook.md", "Deploy Runbook > Rollback")
	if err != nil {
		t.Fatalf("ExpandSection: %v", err)
	}
	if got, want := len(expansion.Chunks), 2; got != want {
		t.Fatalf("len(Chunks) = %d, want %d (got: %v)", got, want, expansion.Chunks)
	}
	if expansion.Chunks[0] != "first part of rollback" {
		t.Errorf("Chunks[0] = %q, want %q", expansion.Chunks[0], "first part of rollback")
	}
	if expansion.Chunks[1] != "second part of rollback" {
		t.Errorf("Chunks[1] = %q, want %q", expansion.Chunks[1], "second part of rollback")
	}
	if expansion.SectionKey != "Deploy Runbook > Rollback" {
		t.Errorf("SectionKey = %q", expansion.SectionKey)
	}
	if want := []string{"Deploy Runbook", "Rollback"}; !slicesEqual(expansion.SectionPath, want) {
		t.Errorf("SectionPath = %v, want %v", expansion.SectionPath, want)
	}
	if !strings.Contains(expansion.FullText, "first part of rollback") || !strings.Contains(expansion.FullText, "second part of rollback") {
		t.Errorf("FullText missing expected pieces: %q", expansion.FullText)
	}
}

func TestExpandSection_NoMatchReturnsEmptyResult(t *testing.T) {
	chunks := []vectorstore.Chunk{
		breadcrumbChunk("docs/runbook.md-0", "docs/runbook.md", "Deploy Runbook",
			[]string{"Deploy Runbook", "Rollback"}, "rollback body"),
	}
	engine := newRerankTestEngine(t, chunks)

	expansion, err := engine.ExpandSection(context.Background(), "docs/runbook.md", "Nonexistent > Section")
	if err != nil {
		t.Fatalf("ExpandSection unexpected error: %v", err)
	}
	if expansion == nil {
		t.Fatalf("expansion = nil, want non-nil empty result")
	}
	if len(expansion.Chunks) != 0 {
		t.Fatalf("Chunks = %v, want empty slice", expansion.Chunks)
	}
	if expansion.FullText != "" {
		t.Errorf("FullText = %q, want empty", expansion.FullText)
	}
}

func TestExpandSection_DifferentDocPathExcludedFromResult(t *testing.T) {
	chunks := []vectorstore.Chunk{
		breadcrumbChunk("docs/runbook.md-0", "docs/runbook.md", "Deploy Runbook",
			[]string{"Deploy Runbook", "Rollback"}, "rollback body in doc A"),
		breadcrumbChunk("docs/other.md-0", "docs/other.md", "Other Doc",
			[]string{"Other Doc", "Rollback"}, "rollback body in doc B"),
	}
	engine := newRerankTestEngine(t, chunks)

	expansion, err := engine.ExpandSection(context.Background(), "docs/runbook.md", "Deploy Runbook > Rollback")
	if err != nil {
		t.Fatalf("ExpandSection: %v", err)
	}
	if len(expansion.Chunks) != 1 {
		t.Fatalf("expected exactly 1 chunk from doc A, got %d", len(expansion.Chunks))
	}
	if !strings.Contains(expansion.Chunks[0], "doc A") {
		t.Errorf("expected only doc A content, got %q", expansion.Chunks[0])
	}
}

func TestExpandSection_ChunksWithoutBreadcrumbAreIgnored(t *testing.T) {
	chunks := []vectorstore.Chunk{
		// Legacy chunk without breadcrumb (e.g., non-Markdown source)
		{
			ID:           "docs/legacy.txt-0",
			DocPath:      "docs/legacy.txt",
			Title:        "Legacy",
			Content:      "no breadcrumb at all in this content",
			LastModified: time.Now(),
			Embedding:    []float32{0.1, 0.2},
		},
	}
	engine := newRerankTestEngine(t, chunks)

	expansion, err := engine.ExpandSection(context.Background(), "docs/legacy.txt", "Anything")
	if err != nil {
		t.Fatalf("ExpandSection: %v", err)
	}
	if len(expansion.Chunks) != 0 {
		t.Errorf("legacy chunks must be skipped (no breadcrumb to match), got Chunks=%v", expansion.Chunks)
	}
}

func TestExpandSection_ValidatesInputs(t *testing.T) {
	engine := newRerankTestEngine(t, nil)

	if _, err := engine.ExpandSection(context.Background(), "", "section"); err == nil {
		t.Error("expected error when doc_path empty")
	}
	if _, err := engine.ExpandSection(context.Background(), "doc.md", ""); err == nil {
		t.Error("expected error when section_key empty")
	}
}
