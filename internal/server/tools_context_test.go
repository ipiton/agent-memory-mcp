package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
	"github.com/ipiton/agent-memory-mcp/internal/rag"
	"github.com/ipiton/agent-memory-mcp/internal/vectorstore"
	"go.uber.org/zap"
)

func TestDocumentContextIsAnAvailableRetrievalTool(t *testing.T) {
	s := newTestServer(t, "")
	if _, ok := s.toolHandlers["document_context"]; !ok {
		t.Fatal("document_context has no MCP handler")
	}
	for _, definition := range ragToolDefs() {
		if definition.Name == "document_context" {
			return
		}
	}
	t.Fatal("document_context has no discoverable schema")
}

func newContextTestServer(t *testing.T) *MCPServer {
	t.Helper()
	s := newTestServer(t, "")
	directory := t.TempDir()
	store, err := vectorstore.NewSQLiteStore(filepath.Join(directory, "vectors.db"), 2, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	err = store.Upsert([]vectorstore.Chunk{
		{ID: "doc.md-0", DocPath: "doc.md", Title: "Guide", Content: "[Guide > Plan]\n\n" + strings.Repeat("界", 400), Embedding: []float32{1, 0}},
		{ID: "doc.md-1", DocPath: "doc.md", Title: "Guide", Content: "[Guide > Result]\n\nThe result is not confirmed.", Embedding: []float32{0, 1}},
	})
	if err := os.WriteFile(filepath.Join(s.config.RootPath, "doc.md"), []byte("An indexed source."), 0600); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(filepath.Join(s.config.RootPath, "doc.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitIndexState(vectorstore.IndexStateUpdate{Metadata: map[string]string{"index_root": s.config.RootPath, "index_state": "ready", vectorstore.SourcePathMetadataPrefix + "doc.md": physical}}); err != nil {
		t.Fatal(err)
	}
	closeErr := store.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	s.config.RAG = config.RAGConfig{Enabled: true, IndexPath: directory}
	s.config.Embeddings.Mode = "local-only"
	s.config.Embeddings.Dimension = 2
	s.config.Embeddings.OllamaBaseURL = "http://127.0.0.1:1"
	s.ragEngine = rag.NewEngine(s.config, nil)
	if s.ragEngine == nil {
		t.Fatal("engine unavailable")
	}
	t.Cleanup(s.ragEngine.Stop)
	return s
}

func readContextTool(t *testing.T, s *MCPServer, name string, args map[string]any) rag.DocumentContext {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	response, rErr := s.handleToolsCall(params, stdioClientKey)
	if rErr != nil {
		t.Fatalf("MCP: %+v", rErr)
	}
	var body rag.DocumentContext
	if err := json.Unmarshal([]byte(response.(toolResult).Content[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDocumentContextReadsSeveralSectionsThroughMCP(t *testing.T) {
	s := newContextTestServer(t)
	s.config.ToolProfile = config.ToolProfileCore
	if !listedTools(t, s)["document_context"] {
		t.Fatal("context tool missing from core profile")
	}
	body := readContextTool(t, s, "document_context", map[string]any{"path": "doc.md", "section_keys": []string{"Guide > Plan", "Guide > Result"}, "max_chars": 256})
	if len(body.Sections) != 2 || body.CharsReturned > 256 || !body.Truncated {
		t.Fatalf("body=%+v", body)
	}
	if body.Sections[1].Text != "The result is not confirmed." {
		t.Fatalf("lost qualification: %+v", body)
	}
	for _, section := range body.Sections {
		if !utf8.ValidString(section.Text) {
			t.Fatal("invalid UTF-8")
		}
	}
	s.config.ToolGrouping = true
	body = readContextTool(t, s, "search", map[string]any{"action": "context", "path": "doc.md", "section_keys": []string{"Guide > Result"}})
	if len(body.Sections) != 1 || body.Sections[0].Text != "The result is not confirmed." {
		t.Fatalf("grouped body=%+v", body)
	}
}

func TestDocumentContextCannotBypassAllowlistOrReadAnotherIndexRoot(t *testing.T) {
	s := newContextTestServer(t)
	guard, err := paths.NewGuard(config.Config{RootPath: s.config.RootPath, AllowedPaths: []string{"allowed"}})
	if err != nil {
		t.Fatal(err)
	}
	s.pathGuard = guard
	for _, path := range []string{"doc.md", "../doc.md", "/etc/passwd"} {
		if err := callTool(t, s, "document_context", map[string]any{"path": path}); err == nil || err.Code != rpcErrInvalidParams {
			t.Fatalf("path=%s error=%+v", path, err)
		}
	}
	guard, err = paths.NewGuard(config.Config{RootPath: s.config.RootPath})
	if err != nil {
		t.Fatal(err)
	}
	s.pathGuard = guard
	s.config.RAG.IndexRoot = t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("Outside allowed root."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.config.RootPath, "escape.md")); err != nil {
		t.Fatal(err)
	}
	if err := callTool(t, s, "document_context", map[string]any{"path": "escape.md"}); err == nil || err.Code != rpcErrInvalidParams {
		t.Fatalf("symlink escape: %+v", err)
	}
	if err := os.WriteFile(filepath.Join(s.config.RAG.IndexRoot, "doc.md"), []byte("Other root."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := callTool(t, s, "document_context", map[string]any{"path": "doc.md"}); err == nil || err.Code != rpcErrInvalidParams {
		t.Fatalf("cross-root: %+v", err)
	}
}

func TestDocumentContextRejectsInvalidArgumentsThroughMCP(t *testing.T) {
	s := newTestServer(t, "")
	for _, args := range []map[string]any{
		{"path": "doc.md", "max_chars": 1},
		{"path": "doc.md", "max_chars": 40000},
		{"path": "doc.md", "max_chars": 256.5},
		{"path": "doc.md", "max_sections": 9},
		{"path": "doc.md", "max_chars": nil},
		{"path": "doc.md", "section_keys": []string{}},
		{"path": "doc.md", "query": "why", "section_keys": []string{"Guide"}},
	} {
		params, err := json.Marshal(map[string]any{"name": "document_context", "arguments": args})
		if err != nil {
			t.Fatal(err)
		}
		_, rErr := s.handleToolsCall(params, stdioClientKey)
		if rErr == nil || rErr.Code != rpcErrInvalidParams {
			t.Fatalf("args=%v error=%+v", args, rErr)
		}
	}
}

func TestDocumentContextRejectsDatabaseFromAnotherRoot(t *testing.T) {
	s := newContextTestServer(t)
	otherRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherRoot, "doc.md"), []byte("Different source with the same name."), 0600); err != nil {
		t.Fatal(err)
	}
	s.config.RootPath = otherRoot
	guard, err := paths.NewGuard(s.config)
	if err != nil {
		t.Fatal(err)
	}
	s.pathGuard = guard
	s.ragEngine.Stop()
	s.ragEngine = rag.NewEngine(s.config, nil)
	if s.ragEngine == nil {
		t.Fatal("engine unavailable")
	}
	t.Cleanup(s.ragEngine.Stop)
	if err := callTool(t, s, "document_context", map[string]any{"path": "doc.md"}); err == nil {
		t.Fatal("indexed text from another root was disclosed")
	}
}

func TestDocumentContextBindsConfiguredSymlinksToTheirIndexedPhysicalSource(t *testing.T) {
	s := newTestServer(t, "")
	a, b := t.TempDir(), t.TempDir()
	for path, text := range map[string]string{a: "Original indexed text.", b: "Replacement indexed text."} {
		if err := os.WriteFile(filepath.Join(path, "file.md"), []byte("# Guide\n\n## Result\n\n"+text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(s.config.RootPath, "docs")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	s.config.AllowedPaths = []string{"docs"}
	s.config.RAG = config.RAGConfig{Enabled: true, IndexPath: t.TempDir(), IndexDirs: []string{"docs/file.md"}, KeywordFallback: true}
	s.config.Embeddings.Mode = "local-only"
	s.config.Embeddings.Dimension = 2
	s.config.Embeddings.OllamaBaseURL = "http://127.0.0.1:1"
	guard, err := paths.NewGuard(s.config)
	if err != nil {
		t.Fatal(err)
	}
	s.pathGuard = guard
	s.ragEngine = rag.NewEngine(s.config, nil)
	if s.ragEngine == nil {
		t.Fatal("engine unavailable")
	}
	t.Cleanup(s.ragEngine.Stop)
	if err := s.ragEngine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := readContextTool(t, s, "document_context", map[string]any{"path": "docs/file.md"})
	if len(body.Sections) == 0 || !strings.Contains(body.Sections[0].Text, "Original indexed text.") {
		t.Fatalf("unchanged symlink refused: %+v", body)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	guard, err = paths.NewGuard(s.config)
	if err != nil {
		t.Fatal(err)
	}
	s.pathGuard = guard
	if err := callTool(t, s, "document_context", map[string]any{"path": "docs/file.md"}); err == nil {
		t.Fatal("old source text disclosed after symlink retarget")
	}
	if err := s.ragEngine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	body = readContextTool(t, s, "document_context", map[string]any{"path": "docs/file.md"})
	if len(body.Sections) == 0 || !strings.Contains(body.Sections[0].Text, "Replacement indexed text.") {
		t.Fatalf("new source unavailable after indexing: %+v", body)
	}
}
