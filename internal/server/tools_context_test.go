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
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.config.RootPath, "doc.md"), []byte("An indexed source."), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(s.config.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitIndexState(vectorstore.IndexStateUpdate{
		Metadata: map[string]string{"index_root": root, "index_state": "ready"},
	}); err != nil {
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
	if listedTools(t, s)["document_context"] {
		t.Fatal("document_context listed in core profile")
	}
	args := map[string]any{"path": "doc.md"}
	if err := callTool(t, s, "document_context", args); err == nil || err.Code != rpcErrMethodNotFound {
		t.Fatalf("core profile accepted direct context: %+v", err)
	}
	s.config.ToolGrouping = true
	if err := callTool(t, s, "search", map[string]any{"action": "context", "path": "doc.md"}); err == nil {
		t.Fatal("core profile accepted grouped context")
	}
	s.config.ToolProfile = config.ToolProfileFull
	s.config.ToolGrouping = false
	if !listedTools(t, s)["document_context"] {
		t.Fatal("document_context missing from full profile")
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

func TestDocumentContextCannotBypassAllowlist(t *testing.T) {
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

func TestDocumentContextUsesSharedIndexRootFromLinkedCheckout(t *testing.T) {
	checkout, indexRoot := t.TempDir(), t.TempDir()
	makeFile := func(root, name, text string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	makeFile(indexRoot, "docs/main-only.md", "# Operations\n\n## Rollback\n\nUse rollback for the previous revision.")
	makeFile(checkout, "docs/local.md", "A checkout-only file.")
	makeFile(indexRoot, "blocked/private.md", "# Outside allowlist\n\nDo not return this.")
	outside := t.TempDir()
	makeFile(outside, "external.md", "# External\n\nDo not return this.")
	if err := os.Symlink(filepath.Join(outside, "external.md"), filepath.Join(indexRoot, "docs/escape.md")); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		RootPath: checkout, AllowedPaths: []string{"docs"}, ToolProfile: config.ToolProfileFull,
		RAG: config.RAGConfig{Enabled: true, IndexRoot: indexRoot, IndexPath: t.TempDir(),
			IndexDirs: []string{"docs", "blocked"}, ChunkSize: 2000, MaxResults: 10, KeywordFallback: true},
		Embeddings: config.EmbeddingsConfig{Mode: "local-only", Dimension: 2,
			OllamaBaseURL: "http://127.0.0.1:1", MaxRetries: 1},
	}
	guard, err := paths.NewGuard(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, guard)
	t.Cleanup(s.ragEngine.Stop)
	if err := s.ragEngine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	search, rErr := s.callSemanticSearch(map[string]any{"query": "rollback"})
	if rErr != nil {
		t.Fatal(rErr.Message)
	}
	result := search.(toolResult).Content[0].Text
	if !strings.Contains(result, "Path: docs/main-only.md") || !strings.Contains(result, "Section key: Operations > Rollback") {
		t.Fatalf("search did not expose the context pointer: %s", result)
	}
	body := readContextTool(t, s, "document_context", map[string]any{
		"path": "docs/main-only.md", "section_keys": []string{"Operations > Rollback"},
	})
	if len(body.Sections) != 1 || body.Sections[0].Text != "Use rollback for the previous revision." {
		t.Fatalf("shared-index context = %+v", body)
	}
	for _, path := range []string{"blocked/private.md", "docs/escape.md", "../docs/main-only.md", "docs/local.md"} {
		if err := callTool(t, s, "document_context", map[string]any{"path": path}); err == nil || err.Code != rpcErrInvalidParams {
			t.Fatalf("path %s escaped index-root authorization: %+v", path, err)
		}
	}
	if err := callTool(t, s, "repo_read", map[string]any{"path": "docs/main-only.md"}); err == nil {
		t.Fatal("repository reads must still use the linked checkout")
	}
}

func TestDocumentContextRejectsIndexAfterRootSymlinkRetarget(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for root, content := range map[string]string{first: "First tree content.", second: "Second tree content."} {
		if err := os.WriteFile(filepath.Join(root, "guide.md"), []byte("# Guide\n\n"+content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		RootPath: alias,
		RAG: config.RAGConfig{
			Enabled: true, IndexPath: t.TempDir(), IndexDirs: []string{"guide.md"},
			ChunkSize: 2000, MaxResults: 10, KeywordFallback: true,
		},
		Embeddings: config.EmbeddingsConfig{
			Mode: "local-only", Dimension: 2, OllamaBaseURL: "http://127.0.0.1:1", MaxRetries: 1,
		},
	}
	start := func() *MCPServer {
		t.Helper()
		guard, err := paths.NewGuard(cfg)
		if err != nil {
			t.Fatal(err)
		}
		s := New(cfg, guard)
		t.Cleanup(s.ragEngine.Stop)
		return s
	}
	original := start()
	if err := original.ragEngine.IndexDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := readContextTool(t, original, "document_context", map[string]any{"path": "guide.md"})
	if len(got.Sections) != 1 || got.Sections[0].Text != "First tree content." {
		t.Fatalf("context from original symlink root = %+v", got)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	restarted := start()
	if err := callTool(t, restarted, "document_context", map[string]any{"path": "guide.md"}); err == nil || !strings.Contains(err.Message, "index source root does not match") {
		t.Fatalf("retargeted root should refuse the old database: %+v", err)
	}
}
