package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ipiton/agent-memory-mcp/internal/config"
	"github.com/ipiton/agent-memory-mcp/internal/paths"
)

func newTestServer(t *testing.T, authToken string) *MCPServer {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		RootPath:      root,
		HTTPAuthToken: authToken,
		OutputMode:    "line",
	}
	guard, err := paths.NewGuard(cfg)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	return New(cfg, guard)
}

func buildMux(s *MCPServer) http.Handler {
	mux := http.NewServeMux()
	authToken := s.config.HTTPAuthToken

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if authToken != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+authToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp := errorResponse(nil, -32600, "Parse error", err.Error())
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		resp := s.handle(req)
		if resp == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})

	return mux
}

func TestHTTPAuthRequired(t *testing.T) {
	s := newTestServer(t, "secret-token-123")
	mux := buildMux(s)

	body, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	// No auth header → 401
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Wrong token → 401
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", rec.Code)
	}

	// Correct token → 200
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token-123")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", rec.Code)
	}
}

func TestHTTPNoAuthConfigured(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	body, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without auth config, got %d", rec.Code)
	}
}

func TestHTTPMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHTTPOptions(t *testing.T) {
	s := newTestServer(t, "")
	mux := buildMux(s)

	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", rec.Code)
	}

	// CORS header should deny by default
	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "" {
		t.Fatalf("expected empty CORS origin, got %q", origin)
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t, "secret")
	mux := buildMux(s)

	// Health should be accessible without auth
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", result["status"])
	}
}

func TestDispatchTableCompleteness(t *testing.T) {
	s := newTestServer(t, "")
	handlers := s.toolHandlers()

	expectedTools := []string{
		"repo_list",
		"repo_read",
		"repo_search",
		"semantic_search",
		"index_documents",
		"store_memory",
		"recall_memory",
		"update_memory",
		"delete_memory",
		"list_memories",
		"memory_stats",
	}

	for _, tool := range expectedTools {
		if _, ok := handlers[tool]; !ok {
			t.Errorf("missing handler for tool %q", tool)
		}
	}

	if len(handlers) != len(expectedTools) {
		t.Errorf("handler count mismatch: got %d, want %d", len(handlers), len(expectedTools))
	}
}

func TestHandleUnknownTool(t *testing.T) {
	s := newTestServer(t, "")

	params, _ := json.Marshal(map[string]any{
		"name":      "nonexistent_tool",
		"arguments": map[string]any{},
	})

	result, rErr := s.handleToolsCall(json.RawMessage(params))
	if rErr == nil {
		t.Fatal("expected error for unknown tool")
	}
	if rErr.Code != -32601 {
		t.Fatalf("expected -32601, got %d", rErr.Code)
	}
	if result != nil {
		t.Fatal("expected nil result")
	}
}

func TestReadMessageSizeLimit(t *testing.T) {
	// Test that readMessage rejects oversized content-length
	input := "Content-Length: 999999999\r\n\r\n"
	reader := bufio.NewReader(bytes.NewBufferString(input))

	_, _, err := readMessage(reader)
	if err == nil {
		t.Fatal("expected error for oversized content-length")
	}
}
